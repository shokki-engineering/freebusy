// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package search connects the availability read model (allocation.v1.
// ResourceDay) to its optional OpenSearch copy, all of it generated
// (internal/database/opensearch): the index is ensured at startup, the
// change-feed indexer follows every committed projection write, a reindex
// reloads it from Postgres behind an alias, and SearchAvailability reads it
// with a fallback to the Postgres table whenever OpenSearch errs or is slow.
package search

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oh-tarnished/opensearch-go-sdk/opensearch"
	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/opensearch/freebusy/allocation"
	"github.com/shokki-engineering/freebusy/internal/database/opensearch/osx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/the-protobuf-project/runtime-go/telemetry"
)

// daySearcher is what SearchAvailability reads candidates through (the
// allocation package's DaySearcher).
type daySearcher interface {
	SearchDays(ctx context.Context, in repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error)
	FacetDays(ctx context.Context, in repox.SearchInput, field string, limit int) ([]repox.Bucket, error)
}

// OpenSearch is the read model's copy in an OpenSearch cluster.
type OpenSearch struct {
	client  *opensearch.Client
	timeout time.Duration
	queue   int
	indexer *osx.Indexer[*allocationpbv1.ResourceDay]
}

// Open connects to the cluster cfg names and ensures the index: its template,
// a first versioned index and the alias, when they are missing.
func Open(ctx context.Context, cfg config.OpenSearchConfig) (*OpenSearch, error) {
	o, err := Connect(cfg)
	if err != nil {
		return nil, err
	}
	if _, err := allocation.ResourceDayIndex.Ensure(ctx, o.client); err != nil {
		return nil, fmt.Errorf("search: ensure index %s: %w", allocation.ResourceDayIndex.Spec.Alias, err)
	}
	return o, nil
}

// Connect builds the client for the cluster cfg names without touching it.
func Connect(cfg config.OpenSearchConfig) (*OpenSearch, error) {
	if cfg.URL == "" {
		return nil, errors.New("search: [search.opensearch] url is required with provider = \"opensearch\"")
	}
	c, err := opensearch.New(opensearch.Config{URL: cfg.URL, Username: cfg.Username, Password: cfg.Password, Timeout: 30 * time.Second, MaxRetries: -1})
	if err != nil {
		return nil, fmt.Errorf("search: opensearch client: %w", err)
	}
	return &OpenSearch{client: c, timeout: cfg.Timeout(), queue: cfg.IndexQueue}, nil
}

// Feed is the indexer following every committed write to the read model,
// for repox.Projections.Feed. A change it fails to index goes to onError;
// Reindex repairs what it missed.
func (o *OpenSearch) Feed(onError func(ctx context.Context, err error)) repox.ProjectionFeed {
	if o.indexer == nil {
		o.indexer = allocation.ResourceDayIndex.Indexer(o.client, osx.IndexerOptions{Queue: o.queue, OnError: onError})
	}
	return o.indexer
}

// Close flushes the indexer.
func (o *OpenSearch) Close(ctx context.Context) error {
	if o.indexer == nil {
		return nil
	}
	return o.indexer.Close(ctx)
}

// Reindex loads a new index from the read model's table read through pg,
// swaps the alias to it, re-applies rows changed during the load, and drops
// the old index.
func (o *OpenSearch) Reindex(ctx context.Context, pg repox.Conn) (osx.ReindexResult, error) {
	ix := allocation.ResourceDayIndex
	return ix.Reindex(ctx, o.client, ix.Source(ctx, pg), osx.ReindexOptions{})
}

// Empty reports whether the index holds no documents: just created by Open,
// or emptied around freebusy.
func (o *OpenSearch) Empty(ctx context.Context) (bool, error) {
	rows, _, err := allocation.ResourceDayIndex.Search(ctx, o.client, repox.SearchInput{PageSize: 1, ReadMask: []string{"day"}})
	return len(rows) == 0, err
}

// Searcher reads the index, falling back to fallback (the Postgres table)
// when OpenSearch fails or takes longer than its timeout, each fallback
// counted in freebusy.search.fallbacks{reason}.
func (o *OpenSearch) Searcher(fallback daySearcher, m telemetry.Meter) *Fallback {
	if m == nil {
		m = telemetry.NoopMeter
	}
	return &Fallback{os: o, pg: fallback, warn: warnFallback, now: time.Now, fallbacks: m.Counter("freebusy.search.fallbacks", telemetry.WithUnit("1"),
		telemetry.WithDescription("SearchAvailability reads served from Postgres because OpenSearch failed, by reason (error, timeout)."))}
}

// Fallback is a searcher over OpenSearch with Postgres behind it.
type Fallback struct {
	os        *OpenSearch
	pg        daySearcher
	fallbacks telemetry.Counter
	// warn and now log a fallback (warnFallback, time.Now; swapped in tests).
	warn func(msg string, kv ...any)
	now  func() time.Time

	mu         sync.Mutex
	nextLog    time.Time // when the next fallback may be logged
	suppressed int       // fallbacks since the last line that were not logged
}

// fallbackLogEvery is how often a fallback is logged at most. The others are
// only counted, and the next line says how many there were, so an
// OpenSearch outage under load is one warning every few seconds rather than
// one per search.
const fallbackLogEvery = 10 * time.Second

// SearchDays reads a page from OpenSearch, or from Postgres when that fails.
// A page token is the backend's own: a token OpenSearch refuses (one Postgres
// issued after an earlier fallback) is retried on Postgres, and a token
// Postgres cannot read either fails the call.
func (f *Fallback) SearchDays(ctx context.Context, in repox.SearchInput) ([]*allocationpbv1.ResourceDay, string, error) {
	tctx, cancel := context.WithTimeout(ctx, f.os.timeout)
	rows, next, err := allocation.ResourceDayIndex.Search(tctx, f.os.client, in)
	cancel()
	if err == nil {
		return rows, next, nil
	}
	if errors.Is(err, repox.ErrInvalidArgument) && in.PageToken == "" {
		// The filter itself is wrong: Postgres would say the same.
		return nil, "", err
	}
	f.record(ctx, tctx, "search", err)
	prows, pnext, perr := f.pg.SearchDays(ctx, in)
	if perr != nil {
		return nil, "", errors.Join(err, perr)
	}
	return prows, pnext, nil
}

// FacetDays counts on OpenSearch, or on Postgres when that fails.
func (f *Fallback) FacetDays(ctx context.Context, in repox.SearchInput, field string, limit int) ([]repox.Bucket, error) {
	tctx, cancel := context.WithTimeout(ctx, f.os.timeout)
	out, err := allocation.ResourceDayIndex.Facet(tctx, f.os.client, in, field, limit)
	cancel()
	if err == nil {
		return out, nil
	}
	if errors.Is(err, repox.ErrInvalidArgument) {
		return nil, err
	}
	f.record(ctx, tctx, "facet", err)
	return f.pg.FacetDays(ctx, in, field, limit)
}

// record counts a fallback from op (search or facet) and logs it, at most
// once every fallbackLogEvery.
func (f *Fallback) record(ctx, tctx context.Context, op string, err error) {
	reason := "error"
	if errors.Is(tctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	}
	f.fallbacks.Add(ctx, 1, telemetry.Labels{"reason": reason})

	f.mu.Lock()
	now := f.now()
	if now.Before(f.nextLog) {
		f.suppressed++
		f.mu.Unlock()
		return
	}
	f.nextLog = now.Add(fallbackLogEvery)
	suppressed := f.suppressed
	f.suppressed = 0
	f.mu.Unlock()
	f.warn("SearchAvailability fell back to Postgres: OpenSearch failed",
		"op", op, "reason", reason, "error", err, "fallbacks_not_logged", suppressed)
}

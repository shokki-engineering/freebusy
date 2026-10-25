// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/opensearch/osx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
)

// readModel is what priming needs of the read model
// (allocation.DayProjection).
type readModel interface {
	Empty(ctx context.Context, conn repox.Conn) (bool, error)
	Rebuild(ctx context.Context, conn repox.Conn) (repox.RebuildResult, error)
}

// searchIndex is what priming needs of the OpenSearch copy
// (search.OpenSearch).
type searchIndex interface {
	Empty(ctx context.Context) (bool, error)
	Reindex(ctx context.Context, pg repox.Conn) (osx.ReindexResult, error)
}

// primer brings the read model, then its OpenSearch copy, up to date as the
// process starts.
type primer struct {
	conn      repox.Conn
	readModel readModel
	// index is nil without an OpenSearch copy.
	index searchIndex
	// direct reports whether the read model's table is read over Postgres,
	// which a reindex needs.
	direct bool
	// info logs what was done.
	info func(msg string, kv ...any)
}

func (s *Services) primer() primer {
	p := primer{conn: s.Conn.SearchConn(), readModel: s.ReadModel, direct: s.Conn.PgSQLConn != nil, info: telemetry.Info}
	if s.search != nil {
		p.index = s.search
	}
	return p
}

// run rebuilds the read model as rebuild says, then reindexes the copy as
// reindex says (both config.OnStart*, "" meaning config.OnStartEmpty). It
// reports what it did. Several replicas starting together may each do it: a
// rebuild only replaces rows, and the last reindex's alias swap wins.
func (p primer) run(ctx context.Context, rebuild, reindex string, logErr func(error)) (rebuilt, reindexed bool) {
	rebuild, reindex = cmp.Or(rebuild, config.OnStartEmpty), cmp.Or(reindex, config.OnStartEmpty)
	ok := func(err error, what string) bool {
		if err != nil {
			logErr(fmt.Errorf("%s: %w", what, err))
		}
		return err == nil
	}

	do := rebuild == config.OnStartAlways
	if rebuild == config.OnStartEmpty {
		empty, err := p.readModel.Empty(ctx, p.conn)
		if !ok(err, "check the read model") {
			return false, false
		}
		do = empty
	}
	if do {
		start := time.Now()
		res, err := p.readModel.Rebuild(ctx, p.conn)
		if !ok(err, "rebuild the read model") {
			return false, false
		}
		rebuilt = true
		p.info("Rebuilt the read model", "mode", rebuild, "duration", time.Since(start).Round(time.Millisecond).String(),
			"batches", res.Batches, "stored", res.Upserted, "pruned", res.Deleted)
	}

	if p.index == nil {
		return rebuilt, false
	}
	switch reindex {
	case config.OnStartAlways:
	case config.OnStartEmpty:
		// A rebuild went through the indexer, which follows every write.
		if rebuilt {
			return rebuilt, false
		}
		empty, err := p.index.Empty(ctx)
		if !ok(err, "check the OpenSearch index") || !empty {
			return rebuilt, false
		}
		none, err := p.readModel.Empty(ctx, p.conn)
		if !ok(err, "check the read model") || none {
			return rebuilt, false
		}
	default:
		return rebuilt, false
	}
	if !p.direct {
		logErr(errors.New(`reindex OpenSearch: needs [database] provider = "gorm": the read model is read from its table`))
		return rebuilt, false
	}
	start := time.Now()
	res, err := p.index.Reindex(ctx, p.conn)
	if !ok(err, "reindex OpenSearch") {
		return rebuilt, false
	}
	p.info("Reindexed OpenSearch", "mode", reindex, "index", res.Index, "duration", time.Since(start).Round(time.Millisecond).String(),
		"loaded", res.Loaded, "caught_up", res.CaughtUp, "pruned", res.Pruned)
	return rebuilt, true
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Every fallback is counted, and logged at most once per fallbackLogEvery
// with the error and how many fallbacks since the last line went unlogged.
func TestFallbackLogsRateLimited(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var lines [][]any
	o := &OpenSearch{}
	f := o.Searcher(nil, nil)
	f.now = func() time.Time { return now }
	f.warn = func(msg string, kv ...any) { lines = append(lines, append([]any{msg}, kv...)) }
	ctx := context.Background()

	pit := errors.New("search_phase_execution_exception: Trying to create too many point in time contexts")
	for range 5 {
		f.record(ctx, ctx, "search", pit)
	}
	if len(lines) != 1 {
		t.Fatalf("5 fallbacks in a burst logged %d lines, want 1", len(lines))
	}
	if got := fmt.Sprint(lines[0]); !strings.Contains(got, "too many point in time") || !strings.Contains(got, "reason error") {
		t.Errorf("first line = %s, want the error and its reason", got)
	}

	now = now.Add(fallbackLogEvery)
	timeout, cancel := context.WithTimeout(ctx, -time.Second)
	defer cancel()
	f.record(ctx, timeout, "facet", context.DeadlineExceeded)
	if len(lines) != 2 {
		t.Fatalf("a fallback after the interval logged %d lines in all, want 2", len(lines))
	}
	if got := fmt.Sprint(lines[1]); !strings.Contains(got, "fallbacks_not_logged 4") || !strings.Contains(got, "reason timeout") || !strings.Contains(got, "op facet") {
		t.Errorf("second line = %s, want 4 unlogged fallbacks, reason timeout, op facet", got)
	}
}

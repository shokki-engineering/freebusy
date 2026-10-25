// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database/opensearch/osx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
)

type fakeReadModel struct {
	empty    bool
	err      error
	rebuilds int
}

func (f *fakeReadModel) Empty(context.Context, repox.Conn) (bool, error) { return f.empty, f.err }

func (f *fakeReadModel) Rebuild(context.Context, repox.Conn) (repox.RebuildResult, error) {
	f.rebuilds++
	return repox.RebuildResult{}, nil
}

type fakeIndex struct {
	empty     bool
	reindexes int
}

func (f *fakeIndex) Empty(context.Context) (bool, error) { return f.empty, nil }

func (f *fakeIndex) Reindex(context.Context, repox.Conn) (osx.ReindexResult, error) {
	f.reindexes++
	return osx.ReindexResult{}, nil
}

// TestPrimer checks when a starting server rebuilds the read model and
// reindexes its OpenSearch copy.
func TestPrimer(t *testing.T) {
	for _, tc := range []struct {
		name                string
		rebuild, reindex    string
		tableEmpty          bool
		indexEmpty          bool
		noIndex, overHasura bool
		wantRebuilt         bool
		wantReindexed       bool
		wantErr             bool
	}{
		{name: "fresh deployment: rebuild, the indexer follows", tableEmpty: true, indexEmpty: true, wantRebuilt: true},
		{name: "default modes are empty", rebuild: config.OnStartEmpty, reindex: config.OnStartEmpty, tableEmpty: true, wantRebuilt: true},
		{name: "populated: nothing to do", wantRebuilt: false},
		{name: "new cluster: reindex", indexEmpty: true, wantReindexed: true},
		{name: "both empty and no resources yet", tableEmpty: true, indexEmpty: true, noIndex: true, wantRebuilt: true},
		{name: "always rebuilds a populated table", rebuild: config.OnStartAlways, wantRebuilt: true},
		{name: "always reindexes, after a rebuild too", rebuild: config.OnStartAlways, reindex: config.OnStartAlways, wantRebuilt: true, wantReindexed: true},
		{name: "never", rebuild: config.OnStartNever, reindex: config.OnStartNever, tableEmpty: false, indexEmpty: true},
		{name: "never rebuild, empty table: no reindex from nothing", rebuild: config.OnStartNever, tableEmpty: true, indexEmpty: true},
		{name: "reindex needs Postgres", indexEmpty: true, overHasura: true, wantErr: true},
		{name: "no OpenSearch", noIndex: true, indexEmpty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := &fakeReadModel{empty: tc.tableEmpty}
			ix := &fakeIndex{empty: tc.indexEmpty}
			p := primer{readModel: rm, direct: !tc.overHasura, info: func(string, ...any) {}}
			if !tc.noIndex {
				p.index = ix
			}
			var errs []error
			rebuilt, reindexed := p.run(context.Background(), tc.rebuild, tc.reindex, func(err error) { errs = append(errs, err) })
			if rebuilt != tc.wantRebuilt || reindexed != tc.wantReindexed {
				t.Errorf("rebuilt, reindexed = %v, %v; want %v, %v", rebuilt, reindexed, tc.wantRebuilt, tc.wantReindexed)
			}
			if (rm.rebuilds > 0) != tc.wantRebuilt || (ix.reindexes > 0) != tc.wantReindexed {
				t.Errorf("rebuilds=%d reindexes=%d", rm.rebuilds, ix.reindexes)
			}
			if (len(errs) > 0) != tc.wantErr {
				t.Errorf("errors = %v, want error: %v", errs, tc.wantErr)
			}
		})
	}

	t.Run("a failed check stops it", func(t *testing.T) {
		rm := &fakeReadModel{err: errors.New("down")}
		ix := &fakeIndex{empty: true}
		p := primer{readModel: rm, index: ix, direct: true, info: func(string, ...any) {}}
		var errs []error
		p.run(context.Background(), "", "", func(err error) { errs = append(errs, err) })
		if len(errs) != 1 || rm.rebuilds != 0 || ix.reindexes != 0 {
			t.Errorf("errs=%v rebuilds=%d reindexes=%d", errs, rm.rebuilds, ix.reindexes)
		}
	})
}

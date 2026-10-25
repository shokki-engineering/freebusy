// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/cache"
)

// inputsNamespace is the cache database the shared version lives in.
const inputsNamespace = "freebusy.allocation.inputs"

// versionKey is the Volatile key holding the shared version.
const versionKey = "version"

// versionTTL outlives any engine cache entry many times over (they last
// minutes), so a version expiring -- readers then see "0" -- can never match an
// entry cached under "0" before it was first set.
const versionTTL = 48 * time.Hour

// inputVersion is the version of the engine's static inputs: resources,
// sites, calendars and their availability. It is the same signal the list
// caches move on (repox.Conn.Changes), kept per process and, when the cache
// is on, in the cache too, so a write on one instance moves every instance's
// version.
//
// It is one version for the whole deployment, not one per site: a resource's
// name does not say which site it is at, and these records change rarely
// next to the holds and events that are read fresh on every request.
type inputVersion struct {
	local  atomic.Uint64
	shared *cache.DB
}

// Changed moves the version on when name is a static input.
func (v *inputVersion) Changed(ctx context.Context, name string) {
	if !staticInput(name) {
		return
	}
	v.local.Add(1)
	if v.shared == nil {
		return
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	if err := v.shared.Volatile.Set(ctx, versionKey, hex.EncodeToString(b[:]), cache.TTL(versionTTL)); err != nil {
		// Other instances keep their cached inputs until those expire. Holds
		// are not affected: they check what they take against the database.
		telemetry.Warn("Engine input version did not move in the cache", "name", name, "error", err)
	}
}

// current is the version now: this process's writes, and the shared version
// when there is one. An error means the shared version could not be read, and
// the caller should not trust what it has cached.
func (v *inputVersion) current(ctx context.Context) (string, error) {
	local := strconv.FormatUint(v.local.Load(), 10)
	if v.shared == nil {
		return local, nil
	}
	var s string
	switch err := v.shared.Volatile.Get(ctx, versionKey, &s); {
	case errors.Is(err, cache.ErrNotFound):
		s = "0"
	case err != nil:
		return "", err
	}
	return local + "/" + s, nil
}

// staticInput reports whether a written record's name is one the engine
// caches: a resource, a site or organization, a calendar, or a calendar's
// availability. Events, holds, allocations and reservations are read fresh and
// do not move it.
func staticInput(name string) bool {
	segs := strings.Split(name, "/")
	switch {
	case len(segs) < 2:
		return false
	case segs[0] == "resources", segs[0] == "organizations":
		return true
	case segs[0] == "organizationalUnits":
		return len(segs) == 2
	case segs[0] == "calendars":
		return len(segs) == 2 || segs[2] == "availabilities"
	}
	return false
}

// InputVersion is the version of the engine's static inputs; it changes on
// every write to a resource, site, organization, calendar or availability
// committed through this connection's repositories, on any instance sharing
// the cache.
func (s *Store) InputVersion(ctx context.Context) (string, error) {
	return s.version.current(ctx)
}

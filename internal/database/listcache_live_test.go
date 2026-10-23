// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database_test

// The reservation list cache on a real Redis, over both backends: a page is
// served from the cache until a write through the repositories reaches its
// site, and then never again.
//
//	FREEBUSY_TEST_REDIS_ADDR=127.0.0.1:6379 FREEBUSY_TEST_DATABASE_URL=... \
//	FREEBUSY_TEST_GRAPHQL_URL=http://localhost:3280/graphql \
//	  go test ./internal/database/ -run LiveListCache -v

import (
	"context"
	"slices"
	"testing"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/livetest"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/cache"
	"github.com/the-protobuf-project/runtime-go/cache/redis"
	"github.com/the-protobuf-project/runtime-go/ulid"
)

func TestLiveListCache(t *testing.T) {
	ctx := context.Background()
	addr := livetest.Env(t, livetest.RedisAddr)
	client, err := redis.NewClient(ctx, redis.Config{Address: addr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	for backend, raw := range liveConns(t) {
		t.Run(backend, func(t *testing.T) {
			// A prefix of its own, so nothing another run cached is read here.
			provider := redis.New(client, cache.Config{Prefix: "listcache-test-" + ulid.Generate().String(), RequireTTL: true})
			conn := &database.Connection{PgSQLConn: raw.Gorm, Hasura: raw.GraphQL, Provider: database.ProviderGorm}
			if raw.GraphQL != nil {
				conn.Provider = database.ProviderHasura
			}
			if err := conn.UseCaches(ctx, provider); err != nil {
				t.Fatal(err)
			}
			// Repositories built from the connection report their writes; one
			// built from a bare Conn is a writer the cache never hears from.
			observed := reservation.New(conn.Repox()).Reservations
			unseen := reservation.New(raw).Reservations
			lists := conn.Caches.Reservations.Wrap(observed)

			site := "organizationalUnits/listcache-" + ulid.Generate().String()
			// Every record this run makes carries tag, so a List across sites
			// can be narrowed to them.
			tag := "listcache-" + ulid.Generate().String()
			var made []string
			create := func(repo reservation.ReservationRepository) string {
				t.Helper()
				r, err := repo.Create(ctx, site, &reservationpbv1.Reservation{Hold: "holds/" + ulid.Generate().String(), Customer: tag})
				if err != nil {
					t.Fatal(err)
				}
				made = append(made, r.GetName())
				return r.GetName()
			}
			t.Cleanup(func() {
				for _, n := range made {
					_ = unseen.Delete(ctx, n)
				}
			})
			list := func(parent string) []string {
				t.Helper()
				in := repox.ListInput{PageSize: 50}
				if parent != site {
					in.Filter = `customer = "` + tag + `"`
				}
				page, _, err := lists.List(ctx, parent, in)
				if err != nil {
					t.Fatal(err)
				}
				var names []string
				for _, r := range page {
					names = append(names, r.GetName())
				}
				slices.Sort(names)
				return names
			}
			want := func(parent string, names ...string) {
				t.Helper()
				names = slices.Clone(names)
				slices.Sort(names)
				if got := list(parent); !slices.Equal(got, names) {
					t.Fatalf("List(%s) = %v, want %v", parent, got, names)
				}
			}

			a, b := create(observed), create(observed)
			want(site, a, b)

			// Behind the cache's back: the cached page is what is served.
			hidden := create(unseen)
			want(site, a, b)

			// A write the repositories report moves the site on, and the next
			// List is the database's -- the hidden row included.
			got, err := observed.Get(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			got.Customer = tag
			if _, err := observed.Update(ctx, got, []string{"customer"}); err != nil {
				t.Fatal(err)
			}
			want(site, a, b, hidden)

			if err := observed.Delete(ctx, b); err != nil {
				t.Fatal(err)
			}
			want(site, a, hidden)

			// A Batch on the connection reports what it committed.
			batch := reservation.NewBatch(conn.Repox())
			c, err := batch.CreateReservation(site, &reservationpbv1.Reservation{Hold: "holds/" + ulid.Generate().String(), Customer: tag})
			if err != nil {
				t.Fatal(err)
			}
			if err := batch.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			made = append(made, c)
			want(site, a, hidden, c)

			// Across sites (AIP-159 "-"): cached, and moved on by a write under
			// any site.
			const across = "organizationalUnits/-"
			want(across, a, hidden, c)
			hidden2 := create(unseen)
			want(across, a, hidden, c)
			d := create(observed)
			want(across, a, hidden, c, hidden2, d)
		})
	}
}

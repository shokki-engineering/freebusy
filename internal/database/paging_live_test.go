// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database_test

// Paging through the generated repositories on every backend. A list longer
// than one page needs a next-page token minted from the last row; on Hasura
// that once failed on every model ("has no field for sort column"), because
// the cursor read json tags and the GraphQL client's models carry graphql
// tags. Small pages make the second page cheap to reach.

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/oh-tarnished/runtime-go/ulid"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
)

func TestLivePaging(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	const total = 5
	for backend, conn := range liveConns(t) {
		t.Run(backend, func(t *testing.T) {
			cals := calendar.New(conn).Calendars
			events := event.New(conn).Events
			cal := "calendars/paging-" + ulid.GenerateString()
			if _, err := cals.Create(ctx, &calendarv1.Calendar{Name: cal, DisplayName: "paging"}); err != nil {
				t.Fatal(err)
			}
			var want []string
			for i := range total {
				e, err := events.Create(ctx, cal, &eventv1.Event{
					Name:    fmt.Sprintf("%s/events/e%d", cal, i),
					Summary: fmt.Sprintf("event %d", i),
					Start:   &eventv1.CalendarTime{Value: &eventv1.CalendarTime_DateTime{DateTime: dt(start.Add(time.Duration(i) * time.Hour))}},
				})
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, e.GetName())
			}
			t.Cleanup(func() {
				for _, n := range want {
					_ = events.Purge(ctx, n)
				}
				_ = cals.Purge(ctx, cal)
			})
			slices.Sort(want)

			// The default order, a camelCase timestamp, and a descending text
			// column: each pages on its own key plus the id tiebreaker.
			for _, order := range []string{"", "create_time desc", "name desc"} {
				t.Run("order "+order, func(t *testing.T) {
					var got []string
					token, pages := "", 0
					for {
						page, next, err := events.List(ctx, cal, repox.ListInput{PageSize: 2, PageToken: token, OrderBy: order})
						if err != nil {
							t.Fatalf("page %d: %v", pages+1, err)
						}
						pages++
						for _, e := range page {
							got = append(got, e.GetName())
						}
						if next == "" {
							break
						}
						if pages > total {
							t.Fatalf("still paging after %d pages", pages)
						}
						token = next
					}
					slices.Sort(got)
					if !slices.Equal(got, want) {
						t.Fatalf("paged %v, want each of %v once", got, want)
					}
					if pages != 3 {
						t.Fatalf("%d pages of 2 for %d events, want 3", pages, total)
					}
				})
			}
		})
	}
}

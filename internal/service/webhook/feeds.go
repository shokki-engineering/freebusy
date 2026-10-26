// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhook

import (
	"context"
	"slices"

	calendarv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/reservation/v1/reservationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/events"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// acrossCalendars is the AIP-159 wildcard parent of events and availability.
const acrossCalendars = "calendars/-"

// lifecycleKinds are the inventory records with no state of their own: their
// events are freebusy.<kind>.created, .updated and .deleted.
var lifecycleKinds = []string{"resource", "calendar", "event", "availability"}

// feeds are the records whose changes are sent. They keep the cursor names
// they have always had (feedCursors/holds, feedCursors/reservations, ...), so
// an upgrade resumes where it stopped: holds and reservations, whose
// feeds protoc-gen-service generates from their state events, and the RFC
// inventory, soft-deleted (AIP-164), whose lifecycle is the change.
func feeds(r Repositories) []events.Feed {
	holds := allocationpbv1svc.HoldFeedSpec(r.Holds)
	holds.ID = "holds"

	return []events.Feed{
		events.NewFeed(holds),
		events.NewFeed(reservationFeed(r.Reservations)),
		events.NewFeed(lifecycleFeed("resources", "resource", nil,
			func(ctx context.Context, q events.Query) ([]*resourcev1.Resource, string, error) {
				return r.Resources.List(ctx, inventory(q))
			},
			func(m *resourcev1.Resource) (string, events.Change) {
				return servicex.Lifecycle(m.GetCreateTime(), m.GetUpdateTime(), m.GetDeleteTime()),
					events.Change{Time: servicex.Time(m.GetUpdateTime())}
			})),
		events.NewFeed(lifecycleFeed("calendars", "calendar", nil,
			func(ctx context.Context, q events.Query) ([]*calendarv1.Calendar, string, error) {
				return r.Calendars.List(ctx, inventory(q))
			},
			func(m *calendarv1.Calendar) (string, events.Change) {
				return servicex.Lifecycle(m.GetCreateTime(), m.GetUpdateTime(), m.GetDeleteTime()),
					events.Change{Time: servicex.Time(m.GetUpdateTime())}
			})),
		events.NewFeed(lifecycleFeed("events", "event", nil,
			func(ctx context.Context, q events.Query) ([]*eventv1.Event, string, error) {
				return r.Events.List(ctx, acrossCalendars, inventory(q))
			},
			func(m *eventv1.Event) (string, events.Change) {
				return servicex.Lifecycle(m.GetCreateTime(), m.GetUpdateTime(), m.GetDeleteTime()),
					events.Change{Time: servicex.Time(m.GetUpdateTime())}
			})),
		events.NewFeed(lifecycleFeed("availabilities", "availability", nil,
			func(ctx context.Context, q events.Query) ([]*availabilityv1.Availability, string, error) {
				return r.Availabilities.List(ctx, acrossCalendars, inventory(q))
			},
			func(m *availabilityv1.Availability) (string, events.Change) {
				return servicex.Lifecycle(m.GetCreateTime(), m.GetUpdateTime(), m.GetDeleteTime()),
					events.Change{Time: servicex.Time(m.GetUpdateTime())}
			})),
	}
}

// reservationFeed is the generated reservation feed, whose change is a
// reservation's state, except for the one write that changes its details
// (UpdateReservation, last_change CHANGE_DETAILS): freebusy.reservation.updated.
func reservationFeed(repo reservation.ReservationRepository) events.FeedSpec[*reservationpbv1.Reservation] {
	spec := reservationpbv1svc.ReservationFeedSpec(repo)
	spec.ID = "reservations"
	spec.ReadMask = append(slices.Clone(spec.ReadMask), "last_change")
	stateChange := spec.Change
	spec.Change = func(m *reservationpbv1.Reservation) events.Change {
		c := stateChange(m)
		if m.GetLastChange() == reservationpbv1.Reservation_CHANGE_DETAILS {
			c.Type = reservationpbv1svc.ReservationUpdatedEvent
		}
		return c
	}
	return spec
}

// lifecycleFeed is a feed of soft-deleted inventory: kind's change is what
// last happened to the record, read as far as its name, etag, times and
// extra.
func lifecycleFeed[R interface {
	events.Record
	proto.Message
}](id, kind string, extra []string, list func(context.Context, events.Query) ([]R, string, error), change func(R) (string, events.Change)) events.FeedSpec[R] {
	return events.FeedSpec[R]{
		ID:       id,
		ReadMask: append([]string{"name", "etag", "create_time", "update_time", "delete_time"}, extra...),
		List:     list,
		Change: func(m R) events.Change {
			life, c := change(m)
			if life != "" {
				c.Type = "freebusy." + kind + "." + life
			}
			return c
		},
		Data: func(m R) ([]byte, error) { return protojson.Marshal(m) },
	}
}

// inventory reads q over soft-deleted records too: a delete is a change like
// any other.
func inventory(q events.Query) repox.ListInput {
	in := servicex.FeedInput(q)
	in.ShowDeleted = true
	return in
}

// EventTypes lists every event type a webhook can ask for: the state events
// of holds and reservations, and freebusy.reservation.updated, as the protos
// declare them, and the inventory's lifecycle events.
func EventTypes() []string {
	out := slices.Concat(allocationpbv1svc.EventTypes(), reservationpbv1svc.EventTypes())
	for _, kind := range lifecycleKinds {
		for _, l := range events.Lifecycles {
			out = append(out, "freebusy."+kind+"."+l)
		}
	}
	slices.Sort(out)
	return out
}

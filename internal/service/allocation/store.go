// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package allocation is the availability engine's gRPC layer: AllocationService
// (how much is free) and HoldService (take it, briefly). The arithmetic lives in
// the engine subpackage; this package gathers the inputs from a Store, hands
// them over, and maps the answers back onto allocation.v1.
//
// Nothing here is vertical. A resource is an RFC 9073 VRESOURCE, its offered
// time is RFC 7953 VAVAILABILITY on its calendar, its bookings are RFC 5545
// events on that calendar plus converted holds, and whatever rules one vertical
// needs that another does not arrive through RFC 9073 STRUCTURED-DATA.
package allocation

import (
	"context"
	"time"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	availabilityv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	resourcev1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
)

// ResourceQuery selects the resources a request is about. Exactly one of
// OrganizationalUnits, Organization or Calendars scopes it; Names and Types
// narrow it.
type ResourceQuery struct {
	// OrganizationalUnits restricts to resources at these sites.
	// Format: organizationalUnits/{organizational_unit}
	OrganizationalUnits []string
	// Organization restricts to resources at any site of this operator, used
	// by SearchAvailability. Format: organizations/{organization}
	Organization string
	// Calendars restricts to resources scheduled on these calendars, used by
	// QueryFreeBusy. Format: calendars/{calendar}
	Calendars []string
	// Names restricts to these resources. Format: resources/{resource}
	Names []string
	// Types restricts to resources carrying any of these RESOURCE-TYPE values.
	Types []string
}

// ResourceRecord is one RFC 9073 resource as stored, with the one fact about it
// that lives elsewhere: its calendar's time zone.
type ResourceRecord struct {
	Resource *resourcev1.Resource
	// TimeZone is the IANA zone of the resource's RFC 5545 calendar, empty
	// when it names no calendar or the calendar names no zone.
	TimeZone string
}

// Claims are the spans live allocation rows hold against each resource.
type Claims struct {
	// Held is time claimed by ACTIVE holds whose expiry has not passed.
	Held map[string]engine.Spans
	// Converted is time kept by CONVERTED holds, which never expire.
	Converted map[string]engine.Spans
}

// Store is everything the engine reads and writes. Implementations exist for
// GORM and Hasura; errors are the repositories' sentinels (repox).
type Store interface {
	// Resources returns the resources a query selects, including soft-deleted
	// ones, which the engine counts but never offers.
	Resources(ctx context.Context, q ResourceQuery) ([]ResourceRecord, error)

	// Availability returns the RFC 7953 components on each calendar, keyed by
	// calendar name. A calendar with none is absent from the map. Times are
	// read, in the calendar's zone, by the caller.
	Availability(ctx context.Context, calendars []string) (map[string][]*availabilityv1.Availability, error)

	// Events returns the RFC 5545 events on each calendar that may overlap
	// window, keyed by calendar name, with every override of a returned series
	// wherever it moved to -- cancelled and transparent ones too, since an
	// override frees the instance it replaces whatever it says itself. A
	// recurring event is returned once with its rule; which events are busy,
	// and expansion through Occurrences, are the caller's.
	Events(ctx context.Context, calendars []string, window engine.Span) (map[string][]*eventv1.Event, error)

	// Claims returns the live allocation spans overlapping window on each
	// resource, as of now.
	Claims(ctx context.Context, resources []string, window engine.Span, now time.Time) (Claims, error)

	// InsertHold writes hold and one allocation per entry of hold.Resources,
	// atomically, guarded: if any of those resources has a live claim
	// overlapping the hold's window when the write lands, nothing is written
	// and the error is repox.ErrConflict. This is the one place the race for
	// the last resource is decided, so it must hold under concurrency, not
	// just on a quiet database.
	//
	// A hold that replaces another (hold.Replaces) claims, on each resource,
	// only the part of its window the replaced hold's claims leave free; the
	// replaced hold's claims are no conflict.
	InsertHold(ctx context.Context, parent string, hold *allocationpbv1.Hold) (*allocationpbv1.Hold, error)

	// HandOver ends the converted hold from as of at, guarded by etag, and in
	// the same write gives hold to -- which replaces it -- the rest of its
	// window on each of its resources: from's claims there are deleted, its
	// other claims take the expiry at, and to's claims fill its window.
	HandOver(ctx context.Context, from, etag, to string, at time.Time) (*allocationpbv1.Hold, error)

	// GetHold returns a hold by name as stored, without applying expiry.
	GetHold(ctx context.Context, name string) (*allocationpbv1.Hold, error)

	// FindHold returns the hold at parent carrying idempotency key, or
	// repox.ErrNotFound.
	FindHold(ctx context.Context, parent, idempotencyKey string) (*allocationpbv1.Hold, error)

	// ListHolds returns a page of holds at parent.
	ListHolds(ctx context.Context, parent string, in repox.ListInput) ([]*allocationpbv1.Hold, string, error)

	// UpdateHold changes a hold's state and expiry, and its allocations'
	// expiry with it, if the stored etag still equals etag (empty skips the
	// check). repox.ErrConflict on a mismatch. A nil expire clears it, which
	// is how a converted hold keeps its resources for good.
	UpdateHold(ctx context.Context, name, etag string, state allocationpbv1.HoldState, expire *time.Time) (*allocationpbv1.Hold, error)

	// DeleteHold removes a hold and its allocations, guarded by etag as above.
	DeleteHold(ctx context.Context, name, etag string) error

	// ExpireHolds marks every ACTIVE hold whose expiry is before now EXPIRED
	// and returns how many. Bookkeeping only: reads already ignore them.
	ExpireHolds(ctx context.Context, now time.Time) (int64, error)
}

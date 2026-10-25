// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package directory serves the upstream protobuf-rfc resources the availability
// engine reads -- RFC 9073 resources, RFC 5545 calendars and events, RFC 7953
// availability -- so an operator publishes what is bookable through the same
// API the engine answers from.
//
// The servers are thin: persistence is the generated repositories, which
// already speak these protos on both backends, and the two methods that are
// more than CRUD (QueryFreeBusy, ExpandEvent) defer to the engine so that what
// they report is what a hold would see.
package directory

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/organization"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/orgunit"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// FreeBusyQuerier is the engine's view of one calendar's time, which
// QueryFreeBusy reports. *allocation.Server implements it.
type FreeBusyQuerier interface {
	FreeBusy(ctx context.Context, calendar, zone string, window engine.Span) (allocation.FreeBusy, error)
}

// Repositories are the generated repositories the servers persist through.
type Repositories struct {
	Organizations       organization.OrganizationRepository
	OrganizationalUnits orgunit.OrganizationalUnitRepository
	Resources           resource.ResourceRepository
	Calendars           calendar.CalendarRepository
	Availabilities      availability.AvailabilityRepository
	Events              event.EventRepository

	// conn and retention build Writes over the same connection, deleting
	// with the same retention as the repositories.
	conn      repox.Conn
	retention time.Duration
}

// NewRepositories opens every repository on conn, picking the backend the
// connection carries.
func NewRepositories(conn repox.Conn, retention time.Duration) Repositories {
	return Repositories{
		Organizations:       organization.New(conn, organization.WithRetention(retention)).Organizations,
		OrganizationalUnits: orgunit.New(conn, orgunit.WithRetention(retention)).OrganizationalUnits,
		Resources:           resource.New(conn, resource.WithRetention(retention)).Resources,
		Calendars:           calendar.New(conn, calendar.WithRetention(retention)).Calendars,
		Availabilities:      availability.New(conn, availability.WithRetention(retention)).Availabilities,
		Events:              event.New(conn, event.WithRetention(retention)).Events,
		conn:                conn,
		retention:           retention,
	}
}

// Writes is one atomic write across the calendar schemas: a series with its
// overrides, or a calendar with every object in it. Queue writes on each
// Batch, then Commit once: all of them land or none does. Queued writes run
// no repository hooks, and an etag on an updated record guards it -- a guard
// that fails fails the whole Commit with repox.ErrConflict.
type Writes struct {
	unit           *repox.Unit
	Calendars      calendar.Batch
	Availabilities availability.Batch
	Events         event.Batch
}

// Writes starts an atomic write on the repositories' connection.
func (r Repositories) Writes() Writes {
	u := repox.NewUnit(r.conn)
	return Writes{
		unit:           u,
		Calendars:      calendar.NewBatchIn(u, calendar.WithRetention(r.retention)),
		Availabilities: availability.NewBatchIn(u, availability.WithRetention(r.retention)),
		Events:         event.NewBatchIn(u, event.WithRetention(r.retention)),
	}
}

// Commit applies every write queued on w, in one transaction on GORM and one
// mutation request on GraphQL.
func (w Writes) Commit(ctx context.Context) error { return w.unit.Commit(ctx) }

// Overrides returns the live overrides of series, RFC 5545 section 3.8.4.4:
// the events in its calendar sharing its ical_uid that carry an
// original_start. Anything other than a recurring series has none.
func (r Repositories) Overrides(ctx context.Context, series *eventv1.Event) ([]*eventv1.Event, error) {
	if series.GetRecurrence() == nil || series.GetIcalUid() == "" || series.GetOriginalStart() != nil {
		return nil, nil
	}
	var found []*eventv1.Event
	for token := ""; ; {
		page, next, err := r.Events.List(ctx, calendarOf(series.GetName()), repox.ListInput{
			PageSize: 1000, PageToken: token,
			Where: []filterx.Condition{
				{Field: "ical_uid", Op: filterx.OpEq, Value: series.GetIcalUid()},
				filterx.IsNull("original_start", false),
			},
		})
		if err != nil {
			return nil, err
		}
		found = append(found, page...)
		if token = next; token == "" {
			return allocation.OverridesOf(series, found), nil
		}
	}
}

// resourceID is AIP-122's rule for a caller-chosen id: lower-case letters,
// digits and hyphens, starting with a letter, at most 63 characters.
var resourceID = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// checkID accepts an empty id (the server mints one) or a valid AIP-122 id.
func checkID(field, id string) error {
	if id == "" || resourceID.MatchString(id) {
		return nil
	}
	return servicex.BadField("INVALID_ID", field,
		fmt.Sprintf("%s %q is not a valid id", field, id),
		"An id is lower-case letters, digits and hyphens, starts with a letter, ends with a letter or digit and is at most 63 characters (AIP-122), "+
			"e.g. \"riverside-court-1\"; or leave it empty to have one assigned")
}

// PrepareCreate readies m for a create under name: everything the server owns
// is cleared, so a client cannot set a create_time, an etag or a delete_time
// by sending one; and every field the proto marks REQUIRED must be set.
func PrepareCreate(m proto.Message, name string) error {
	clearOutputOnly(m)
	m.ProtoReflect().Set(m.ProtoReflect().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString(name))
	return requiredSet(m)
}

// requiredSet refuses m when a field the proto marks REQUIRED (AIP-203) is
// unset, naming every such field at once -- so a caller fixes them in one
// round trip rather than one per error -- under the request field that
// carries m (organization.display_names).
//
// The RFC protos mark these with google.api.field_behavior alone, not with a
// buf.validate rule, so request validation lets them through; without this
// the database's not-null constraint is what refuses them, as an internal
// error naming a column.
func requiredSet(m proto.Message) error {
	r := m.ProtoReflect()
	prefix := snake(string(r.Descriptor().Name()))
	var missing []string
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if behaviorOf(fd, annotations.FieldBehavior_REQUIRED) && !r.Has(fd) {
			missing = append(missing, prefix+"."+string(fd.Name()))
		}
	}
	switch len(missing) {
	case 0:
		return nil
	case 1:
		return servicex.BadField("REQUIRED_FIELD", missing[0],
			fmt.Sprintf("%s is required", missing[0]), "Set it and send the request again")
	}
	return servicex.BadField("REQUIRED_FIELD", missing[0],
		fmt.Sprintf("%s are required", strings.Join(missing, ", ")), "Set each of them and send the request again")
}

// snake is a message name as its request field: OrganizationalUnit is
// organizational_unit.
func snake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// clearOutputOnly clears every top-level OUTPUT_ONLY field of m.
func clearOutputOnly(m proto.Message) {
	r := m.ProtoReflect()
	fields := r.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		if behaviorOf(fields.Get(i), annotations.FieldBehavior_OUTPUT_ONLY) {
			r.Clear(fields.Get(i))
		}
	}
}

func behaviorOf(fd protoreflect.FieldDescriptor, want annotations.FieldBehavior) bool {
	bs, _ := proto.GetExtension(fd.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, b := range bs {
		if b == want {
			return true
		}
	}
	return false
}

// windowOf checks a google.type.Interval window: both ends present, end after
// start, and no longer than allocation.MaxFreeBusyWindow.
func windowOf(iv *interval.Interval) (engine.Span, error) {
	if iv.GetStartTime() == nil || iv.GetEndTime() == nil {
		return engine.Span{}, servicex.BadField("WINDOW_INCOMPLETE", "window",
			"window needs both start_time and end_time",
			"Send both as RFC 3339 timestamps, e.g. start_time: \"2026-11-02T00:00:00Z\", end_time: \"2026-11-09T00:00:00Z\"")
	}
	w := allocation.SpanOf(iv)
	if w.Empty() {
		return engine.Span{}, servicex.BadField("WINDOW_BACKWARDS", "window.end_time",
			"window.end_time must be after window.start_time",
			"Swap them, or check the offsets: 18:00+01:00 is 17:00Z")
	}
	if w.Duration() > allocation.MaxFreeBusyWindow {
		return engine.Span{}, servicex.BadField("WINDOW_TOO_LONG", "window",
			fmt.Sprintf("window is %s long, and at most %s is answered in one call", w.Duration(), allocation.MaxFreeBusyWindow),
			"Split it into consecutive windows of at most that length and ask for each")
	}
	return w, nil
}

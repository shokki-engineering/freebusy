// Package rfcsvc serves the upstream protobuf-rfc resources the availability
// engine reads -- RFC 9073 resources, RFC 5545 calendars and events, RFC 7953
// availability -- so an operator publishes what is bookable through the same
// API the engine answers from.
//
// The servers are thin: persistence is the generated repositories, which
// already speak these protos on both backends, and the two methods that are
// more than CRUD (QueryFreeBusy, ExpandEvent) defer to the engine so that what
// they report is what a hold would see.
package rfcsvc

import (
	"context"
	"fmt"
	"regexp"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/availability"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/calendar"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/event"
	"github.com/shokki-engineering/freebusy/internal/database/repository/rfc/resource"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// FreeBusyQuerier is the engine's view of one calendar's time, which
// QueryFreeBusy reports. *allocation.Server implements it.
type FreeBusyQuerier interface {
	FreeBusy(ctx context.Context, calendar, zone string, window engine.Span) (allocation.FreeBusy, error)
}

// Repositories are the generated repositories the servers persist through.
type Repositories struct {
	Resources      resource.ResourceRepository
	Calendars      calendar.CalendarRepository
	Availabilities availability.AvailabilityRepository
	Events         event.EventRepository
}

// NewRepositories opens every repository on conn, picking the backend the
// connection carries.
func NewRepositories(conn repox.Conn) Repositories {
	return Repositories{
		Resources:      resource.New(conn).Resources,
		Calendars:      calendar.New(conn).Calendars,
		Availabilities: availability.New(conn).Availabilities,
		Events:         event.New(conn).Events,
	}
}

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
	return fmt.Errorf("%w: %s %q must match %s", types.ErrInvalidArgument, field, id, resourceID)
}

// PrepareCreate readies m for a create under name: everything the server owns
// is cleared, so a client cannot set a create_time, an etag or a delete_time
// by sending one.
func PrepareCreate(m proto.Message, name string) {
	clearOutputOnly(m)
	m.ProtoReflect().Set(m.ProtoReflect().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString(name))
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

// updatePaths resolves an AIP-134 update mask against m. A mask of "*"
// replaces every mutable field (the repositories' empty mask). An absent mask
// means the fields the caller populated. Paths naming no field of m are
// rejected rather than silently ignored.
func updatePaths(m proto.Message, mask *fieldmaskpb.FieldMask) ([]string, error) {
	if mask == nil {
		var paths []string
		m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
			if fd.Name() != "name" && !behaviorOf(fd, annotations.FieldBehavior_OUTPUT_ONLY) {
				paths = append(paths, string(fd.Name()))
			}
			return true
		})
		if len(paths) == 0 {
			return nil, fmt.Errorf("%w: nothing to update: no update_mask and no fields set", types.ErrInvalidArgument)
		}
		return paths, nil
	}
	if len(mask.GetPaths()) == 1 && mask.GetPaths()[0] == "*" {
		return nil, nil
	}
	if !mask.IsValid(m) {
		return nil, fmt.Errorf("%w: update_mask %v names a field %s does not have", types.ErrInvalidArgument, mask.GetPaths(), m.ProtoReflect().Descriptor().Name())
	}
	return mask.GetPaths(), nil
}

// listInput carries an AIP-132 request onto the repositories.
func listInput(size int32, token, filter string, showDeleted bool) repox.ListInput {
	return repox.ListInput{PageSize: size, PageToken: token, Filter: filter, ShowDeleted: showDeleted}
}

// windowOf checks a google.type.Interval window: both ends present, end after
// start, and no longer than allocation.MaxFreeBusyWindow.
func windowOf(iv *interval.Interval) (engine.Span, error) {
	if iv.GetStartTime() == nil || iv.GetEndTime() == nil {
		return engine.Span{}, fmt.Errorf("%w: window needs both start_time and end_time", types.ErrInvalidArgument)
	}
	w := allocation.SpanOf(iv)
	if w.Empty() {
		return engine.Span{}, fmt.Errorf("%w: window end_time must be after start_time", types.ErrInvalidArgument)
	}
	if w.Duration() > allocation.MaxFreeBusyWindow {
		return engine.Span{}, fmt.Errorf("%w: window longer than %v", types.ErrInvalidArgument, allocation.MaxFreeBusyWindow)
	}
	return w, nil
}

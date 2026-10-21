package rfcsvc

import (
	"context"
	"fmt"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc5545/event/v1/eventv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/emptypb"
)

// EventServer serves RFC 5545 events: the bookings, closures and maintenance
// windows on a calendar. Opaque, unconfirmed-or-confirmed events are busy time
// to the engine, so writing one here takes the resource off sale.
type EventServer struct {
	eventv1grpc.UnimplementedEventsServer
	repos Repositories
}

// NewEventServer returns an EventServer over repos.
func NewEventServer(repos Repositories) *EventServer {
	return &EventServer{repos: repos}
}

const eventService = "Events"

func (s *EventServer) GetEvent(ctx context.Context, req *eventv1.GetEventRequest) (*eventv1.Event, error) {
	var out *eventv1.Event
	err := rpc.Traced(ctx, eventService, "GetEvent", func(ctx context.Context) (err error) {
		out, err = s.repos.Events.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *EventServer) ListEvents(ctx context.Context, req *eventv1.ListEventsRequest) (*eventv1.ListEventsResponse, error) {
	out := &eventv1.ListEventsResponse{}
	err := rpc.Traced(ctx, eventService, "ListEvents", func(ctx context.Context) (err error) {
		out.Events, out.NextPageToken, err = s.repos.Events.List(ctx, req.GetParent(),
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// BatchGetEvents returns the named events in request order, all or nothing
// (AIP-231): one missing name fails the call.
func (s *EventServer) BatchGetEvents(ctx context.Context, req *eventv1.BatchGetEventsRequest) (*eventv1.BatchGetEventsResponse, error) {
	out := &eventv1.BatchGetEventsResponse{}
	err := rpc.Traced(ctx, eventService, "BatchGetEvents", func(ctx context.Context) error {
		prefix := req.GetParent() + "/events/"
		for _, name := range req.GetNames() {
			if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
				return rpc.ToStatusErr(fmt.Errorf("%w: %s is not under %s", types.ErrInvalidArgument, name, req.GetParent()))
			}
			e, err := s.repos.Events.Get(ctx, name)
			if err != nil {
				return rpc.ToStatusErr(err)
			}
			out.Events = append(out.Events, e)
		}
		return nil
	})
	return out, err
}

func (s *EventServer) CreateEvent(ctx context.Context, req *eventv1.CreateEventRequest) (*eventv1.Event, error) {
	var out *eventv1.Event
	err := rpc.Traced(ctx, eventService, "CreateEvent", func(ctx context.Context) error {
		if err := checkID("event_id", req.GetEventId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		if err := liveParent(ctx, s.repos, req.GetParent()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetEvent()
		name := ""
		if id := req.GetEventId(); id != "" {
			name = req.GetParent() + "/events/" + id
		}
		PrepareCreate(in, name)
		var err error
		out, err = s.repos.Events.Create(ctx, req.GetParent(), in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *EventServer) UpdateEvent(ctx context.Context, req *eventv1.UpdateEventRequest) (*eventv1.Event, error) {
	var out *eventv1.Event
	err := rpc.Traced(ctx, eventService, "UpdateEvent", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetEvent(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Events.Update(ctx, req.GetEvent(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *EventServer) DeleteEvent(ctx context.Context, req *eventv1.DeleteEventRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, eventService, "DeleteEvent", func(ctx context.Context) error {
		return rpc.ToStatusErr(s.repos.Events.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *EventServer) UndeleteEvent(ctx context.Context, req *eventv1.UndeleteEventRequest) (*eventv1.Event, error) {
	var out *eventv1.Event
	err := rpc.Traced(ctx, eventService, "UndeleteEvent", func(ctx context.Context) (err error) {
		out, err = s.repos.Events.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// expandPageSize is ExpandEvent's default page, which upstream sets apart
// from List's.
const expandPageSize = 250

// ExpandEvent materialises a recurring event's instances in the window, a
// page at a time.
func (s *EventServer) ExpandEvent(ctx context.Context, req *eventv1.ExpandEventRequest) (*eventv1.ExpandEventResponse, error) {
	out := &eventv1.ExpandEventResponse{}
	err := rpc.Traced(ctx, eventService, "ExpandEvent", func(ctx context.Context) error {
		window, err := windowOf(req.GetWindow())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		e, err := s.repos.Events.Get(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		cal, err := s.repos.Calendars.Get(ctx, calendarOf(req.GetName()))
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		overrides, err := s.repos.Overrides(ctx, e)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		instances, err := expand(e, overrides, ZoneOf(cal.GetTimeZone().GetId()), window)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		size := req.GetPageSize()
		if size <= 0 {
			size = expandPageSize
		}
		limit, offset := types.PageBounds(size, req.GetPageToken())
		if offset > len(instances) {
			offset = len(instances)
		}
		end := min(offset+limit, len(instances))
		out.Events = instances[offset:end]
		if end < len(instances) {
			out.NextPageToken = types.EncodeOffset(end)
		}
		return nil
	})
	return out, err
}

// calendarOf returns the calendar an event name sits under.
// "calendars/c/events/e" -> "calendars/c".
func calendarOf(event string) string {
	n := 0
	for i := range len(event) {
		if event[i] == '/' {
			n++
			if n == 2 {
				return event[:i]
			}
		}
	}
	return event
}

package rfcsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc7953/availability/v1/availabilityv1grpc"
	rfcavailabilitypbv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/oh-tarnished/freebusy/internal/runtime/rpc"
	"github.com/oh-tarnished/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/emptypb"
)

// AvailabilityServer serves RFC 7953 VAVAILABILITY components: when a
// calendar's resources are on offer. The engine reads them on every
// availability question, so a change here is a change to what can be booked.
type AvailabilityServer struct {
	availabilityv1grpc.UnimplementedAvailabilitiesServer
	repos Repositories
}

// NewAvailabilityServer returns an AvailabilityServer over repos.
func NewAvailabilityServer(repos Repositories) *AvailabilityServer {
	return &AvailabilityServer{repos: repos}
}

const availabilityService = "Availabilities"

func (s *AvailabilityServer) GetAvailability(ctx context.Context, req *rfcavailabilitypbv1.GetAvailabilityRequest) (*rfcavailabilitypbv1.Availability, error) {
	var out *rfcavailabilitypbv1.Availability
	err := rpc.Traced(ctx, availabilityService, "GetAvailability", func(ctx context.Context) (err error) {
		out, err = s.repos.Availabilities.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *AvailabilityServer) ListAvailabilities(ctx context.Context, req *rfcavailabilitypbv1.ListAvailabilitiesRequest) (*rfcavailabilitypbv1.ListAvailabilitiesResponse, error) {
	out := &rfcavailabilitypbv1.ListAvailabilitiesResponse{}
	err := rpc.Traced(ctx, availabilityService, "ListAvailabilities", func(ctx context.Context) (err error) {
		out.Availabilities, out.NextPageToken, err = s.repos.Availabilities.List(ctx, req.GetParent(),
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *AvailabilityServer) CreateAvailability(ctx context.Context, req *rfcavailabilitypbv1.CreateAvailabilityRequest) (*rfcavailabilitypbv1.Availability, error) {
	var out *rfcavailabilitypbv1.Availability
	err := rpc.Traced(ctx, availabilityService, "CreateAvailability", func(ctx context.Context) error {
		if err := checkID("availability_id", req.GetAvailabilityId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		if err := liveParent(ctx, s.repos, req.GetParent()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetAvailability()
		name := ""
		if id := req.GetAvailabilityId(); id != "" {
			name = req.GetParent() + "/availabilities/" + id
		}
		prepareCreate(in, name)
		var err error
		out, err = s.repos.Availabilities.Create(ctx, req.GetParent(), in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *AvailabilityServer) UpdateAvailability(ctx context.Context, req *rfcavailabilitypbv1.UpdateAvailabilityRequest) (*rfcavailabilitypbv1.Availability, error) {
	var out *rfcavailabilitypbv1.Availability
	err := rpc.Traced(ctx, availabilityService, "UpdateAvailability", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetAvailability(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Availabilities.Update(ctx, req.GetAvailability(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *AvailabilityServer) DeleteAvailability(ctx context.Context, req *rfcavailabilitypbv1.DeleteAvailabilityRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, availabilityService, "DeleteAvailability", func(ctx context.Context) error {
		return rpc.ToStatusErr(s.repos.Availabilities.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *AvailabilityServer) UndeleteAvailability(ctx context.Context, req *rfcavailabilitypbv1.UndeleteAvailabilityRequest) (*rfcavailabilitypbv1.Availability, error) {
	var out *rfcavailabilitypbv1.Availability
	err := rpc.Traced(ctx, availabilityService, "UndeleteAvailability", func(ctx context.Context) (err error) {
		out, err = s.repos.Availabilities.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// liveParent checks that calendar exists and is not soft-deleted: a child
// written under a deleted calendar would be invisible to List and to the
// engine both, and only reappear by surprise on undelete.
func liveParent(ctx context.Context, repos Repositories, calendar string) error {
	cal, err := repos.Calendars.Get(ctx, calendar)
	if err != nil {
		return err
	}
	if cal.GetDeleteTime() != nil {
		return fmt.Errorf("%w: %s is deleted", types.ErrInvalidState, calendar)
	}
	return nil
}

func isNotFound(err error) bool { return errors.Is(err, types.ErrNotFound) }

// sortBy sorts s stably by less.
func sortBy[T any](s []T, less func(a, b T) bool) {
	slices.SortStableFunc(s, func(a, b T) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return 0
	})
}

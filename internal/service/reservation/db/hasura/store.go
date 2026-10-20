// Package hasura is ReservationService's Store on Hasura DDN.
package hasura

import (
	"context"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql"
	ressql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/reservationql/resourceql"
	resrepo "github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/service/dbutil"
	svc "github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"github.com/the-protobuf-project/runtime-go/network/graphql"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store implements reservation.Store on Hasura: reads through the generated
// repository, writes through the freebusyql client, transitions as DDN
// pre-checked updates.
type Store struct {
	svc  *freebusyql.Service
	repo resrepo.ReservationRepository
}

var _ svc.Store = (*Store)(nil)

// New returns a Store over the freebusyql client.
func New(s *freebusyql.Service) *Store {
	return &Store{
		svc:  s,
		repo: resrepo.NewGraphQL(s, resrepo.WithReservationHooks(resrepo.ReservationHooks{AfterRead: svc.FillHold})).Reservations,
	}
}

// Get reads the row and converts it here. The generated GraphQL converter maps
// only the fields a client may write (protoc-gen-store 1.5.4), so a read
// through it would come back without the window, resources or any of the
// lifecycle timestamps.
func (s *Store) Get(ctx context.Context, name string) (*reservationpbv1.Reservation, error) {
	rows, err := s.svc.Query.Reservation.Resource.List(ctx, ressql.List().Where(ressql.Name.Eq(name)).Limit(1))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	if len(rows) == 0 {
		return nil, types.ErrNotFound
	}
	return toProto(rows[0]), nil
}

// List pages through the generated repository, which owns AIP-160 filtering
// and page tokens, then re-reads that page's rows in one query for the full
// conversion Get explains.
func (s *Store) List(ctx context.Context, parent string, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
	page, next, err := s.repo.List(ctx, parent, in)
	if err != nil || len(page) == 0 {
		return page, next, err
	}
	names := make([]string, len(page))
	for i, r := range page {
		names[i] = r.GetName()
	}
	rows, err := s.svc.Query.Reservation.Resource.List(ctx, ressql.List().Where(ressql.Name.In(names...)))
	if err != nil {
		return nil, "", dbutil.MapHasuraErr(err)
	}
	full := make(map[string]*reservationpbv1.Reservation, len(rows))
	for _, r := range rows {
		full[r.Name] = toProto(r)
	}
	for i, r := range page {
		if f, ok := full[r.GetName()]; ok {
			page[i] = f
		}
	}
	return page, next, nil
}

// Insert writes the row exactly as given.
func (s *Store) Insert(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	now := ts(time.Now())
	in := ressql.CreateInput{
		Id:                   repox.LastSegment(r.GetName()),
		Name:                 r.GetName(),
		OrganizationalUnitId: svc.SiteID(r.GetName()),
		Hold:                 repox.LastSegment(r.GetHold()),
		Customer:             r.GetCustomer(),
		State:                stateName(r.GetState()),
		Etag:                 repox.NewULID(),
		CreateTime:           now,
		UpdateTime:           now,
	}
	if t := r.GetStartTime(); t != nil {
		in.StartTime = ts(t.AsTime())
	}
	if t := r.GetEndTime(); t != nil {
		in.EndTime = ts(t.AsTime())
	}
	for i := range r.GetResources() {
		in.Resources = append(in.Resources, &r.Resources[i])
	}
	if a := r.GetAttributes(); a != nil {
		raw, err := protojson.Marshal(a)
		if err != nil {
			return nil, err
		}
		in.Attributes = raw
	}
	if _, err := s.svc.Mutation.Reservation.Resource.Create(ctx, in); err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	return s.Get(ctx, r.GetName())
}

// Transition is a single pre-checked update: DDN applies it only to a row
// still in one of the from states (and with the given etag), and reports zero
// affected rows otherwise.
func (s *Store) Transition(ctx context.Context, name, etag string, from []reservationpbv1.ReservationState, c svc.Change) (*reservationpbv1.Reservation, error) {
	cur, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(from))
	for i, f := range from {
		names[i] = stateName(f)
	}
	match := ressql.State.In(names...)
	if etag != "" {
		match = ressql.And(match, ressql.Etag.Eq(etag))
	}
	patch := ressql.UpdateInput{
		State:      graphql.Value(stateName(c.State)),
		Etag:       graphql.Value(repox.NewULID()),
		UpdateTime: graphql.Value(ts(time.Now())),
	}
	if c.ConfirmTime != nil {
		patch.ConfirmTime = graphql.Value(ts(*c.ConfirmTime))
	}
	if c.CancelTime != nil {
		patch.CancelTime = graphql.Value(ts(*c.CancelTime))
	}
	if c.CancelReason != "" {
		patch.CancelReason = graphql.Value(c.CancelReason)
	}
	if _, err := s.svc.Mutation.Reservation.Resource.UpdateIfMatch(ctx, repox.LastSegment(cur.GetName()), patch, match); err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	return s.Get(ctx, name)
}

// Stuck reads mid-transition reservations across every site.
func (s *Store) Stuck(ctx context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error) {
	rows, err := s.svc.Query.Reservation.Resource.List(ctx, ressql.List().Where(ressql.And(
		ressql.State.In("PENDING", "CANCELLING"),
		ressql.UpdateTime.Lt(ts(cutoff)),
	)).OrderBy(ressql.UpdateTime.Asc()).Limit(limit))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	out := make([]*reservationpbv1.Reservation, 0, len(rows))
	for _, r := range rows {
		out = append(out, toProto(r))
	}
	return out, nil
}

// toProto converts a raw row, every field.
func toProto(r ressql.ReservationResource) *reservationpbv1.Reservation {
	pb := &reservationpbv1.Reservation{
		Name:         r.Name,
		Hold:         svc.HoldOf(r.Name),
		State:        stateValue(repox.Deref(r.State)),
		Customer:     repox.Deref(r.Customer),
		CancelReason: repox.Deref(r.CancelReason),
		Etag:         repox.Deref(r.Etag),
		CreateTime:   parse(&r.CreateTime),
		UpdateTime:   parse(&r.UpdateTime),
	}
	for _, v := range r.Resources {
		if v != nil {
			pb.Resources = append(pb.Resources, *v)
		}
	}
	pb.StartTime = parse(r.StartTime)
	pb.EndTime = parse(r.EndTime)
	pb.ConfirmTime = parse(r.ConfirmTime)
	pb.CancelTime = parse(r.CancelTime)
	if r.Attributes != nil {
		var st structpb.Struct
		if protojson.Unmarshal(*r.Attributes, &st) == nil {
			pb.Attributes = &st
		}
	}
	return pb
}

func stateName(s reservationpbv1.ReservationState) string {
	return reservationpbv1.ReservationState_name[int32(s)][len("RESERVATION_STATE_"):]
}

func stateValue(s string) reservationpbv1.ReservationState {
	return reservationpbv1.ReservationState(reservationpbv1.ReservationState_value["RESERVATION_STATE_"+s])
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parse(s *string) *timestamppb.Timestamp {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

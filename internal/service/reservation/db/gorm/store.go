// Package gorm is ReservationService's Store on GORM/PostgreSQL.
package gorm

import (
	"context"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/reservation"
	resrepo "github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	svc "github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"gorm.io/gorm"
)

// Store implements reservation.Store on GORM. Reads go through the generated
// repository, which owns filtering and paging; writes are here because the
// generated Update never writes OUTPUT_ONLY fields, and a reservation's state
// is nothing but.
type Store struct {
	db   *gorm.DB
	repo resrepo.ReservationRepository
}

var _ svc.Store = (*Store)(nil)

// New returns a Store over db.
func New(db *gorm.DB) *Store {
	return &Store{
		db:   db,
		repo: resrepo.NewGorm(db, resrepo.WithReservationHooks(resrepo.ReservationHooks{AfterRead: svc.FillHold})).Reservations,
	}
}

func (s *Store) Get(ctx context.Context, name string) (*reservationpbv1.Reservation, error) {
	return s.repo.Get(ctx, name)
}

func (s *Store) List(ctx context.Context, parent string, in repox.ListInput) ([]*reservationpbv1.Reservation, string, error) {
	return s.repo.List(ctx, parent, in)
}

// Insert writes the row exactly as given; a taken name surfaces as
// types.ErrAlreadyExists through the unique index on name.
func (s *Store) Insert(ctx context.Context, r *reservationpbv1.Reservation) (*reservationpbv1.Reservation, error) {
	m := reservation.ReservationFromProto(r)
	m.ID = repox.LastSegment(r.GetName())
	m.OrganizationalUnitID = svc.SiteID(r.GetName())
	m.HoldID = repox.LastSegment(r.GetHold())
	m.Etag = repox.Ptr(repox.NewULID())
	if err := s.db.WithContext(ctx).Create(m).Error; err != nil {
		return nil, repox.MapGormErr(err)
	}
	return s.Get(ctx, r.GetName())
}

// Transition is one conditional UPDATE: the state and etag checks are in its
// WHERE clause, so the compare and the set are a single statement.
func (s *Store) Transition(ctx context.Context, name, etag string, from []reservationpbv1.ReservationState, c svc.Change) (*reservationpbv1.Reservation, error) {
	states := make([]reservation.ReservationState, len(from))
	for i, f := range from {
		states[i] = reservation.ReservationStateFromProto(f)
	}
	set := map[string]any{
		"state":       reservation.ReservationStateFromProto(c.State),
		"etag":        repox.NewULID(),
		"update_time": time.Now(),
	}
	if c.ConfirmTime != nil {
		set["confirm_time"] = *c.ConfirmTime
	}
	if c.CancelTime != nil {
		set["cancel_time"] = *c.CancelTime
	}
	if c.CancelReason != "" {
		set["cancel_reason"] = c.CancelReason
	}
	q := s.db.WithContext(ctx).Model(&reservation.Reservation{}).Where("name = ? AND state IN ?", name, states)
	if etag != "" {
		q = q.Where("etag = ?", etag)
	}
	res := q.Updates(set)
	if res.Error != nil {
		return nil, repox.MapGormErr(res.Error)
	}
	if res.RowsAffected == 0 {
		if _, err := s.Get(ctx, name); err != nil {
			return nil, err
		}
		return nil, types.ErrConflict
	}
	return s.Get(ctx, name)
}

// Stuck reads mid-transition reservations across every site.
func (s *Store) Stuck(ctx context.Context, cutoff time.Time, limit int) ([]*reservationpbv1.Reservation, error) {
	var rows []reservation.Reservation
	err := s.db.WithContext(ctx).
		Where("state IN ? AND update_time < ?", []reservation.ReservationState{reservation.ReservationStatePending, reservation.ReservationStateCancelling}, cutoff).
		Order("update_time").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	out := make([]*reservationpbv1.Reservation, 0, len(rows))
	for i := range rows {
		r := reservation.ReservationToProto(&rows[i])
		r.Hold = svc.HoldOf(r.GetName())
		out = append(out, r)
	}
	return out, nil
}

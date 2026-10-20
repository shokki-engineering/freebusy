// Package gorm is the availability engine's Store on GORM/PostgreSQL. It reads
// the rfc schema (resources, calendars, VAVAILABILITY, events) and owns the
// allocation schema (holds and the allocation rows they take).
package gorm

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/freebusy/allocation"
	rfcavail "github.com/oh-tarnished/freebusy/internal/database/gorm/rfc/availability"
	rfccal "github.com/oh-tarnished/freebusy/internal/database/gorm/rfc/calendar"
	rfcevent "github.com/oh-tarnished/freebusy/internal/database/gorm/rfc/event"
	"github.com/oh-tarnished/freebusy/internal/database/gorm/rfc/orgunit"
	rfcres "github.com/oh-tarnished/freebusy/internal/database/gorm/rfc/resource"
	allocrepo "github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	svc "github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/protobuf/rfc9073/resource/v1/resourcepbv1"
	"gorm.io/gorm"
)

// Store implements allocation.Store on GORM.
type Store struct {
	db    *gorm.DB
	holds allocrepo.HoldRepository
}

var _ svc.Store = (*Store)(nil)

// New returns a Store over db.
func New(db *gorm.DB) *Store {
	return &Store{db: db, holds: allocrepo.NewGorm(db).Holds}
}

// Resources reads the selected resources with their structured data and their
// calendars' zones, in three queries.
func (s *Store) Resources(ctx context.Context, q svc.ResourceQuery) ([]svc.ResourceRecord, error) {
	tx := s.db.WithContext(ctx).Preload("StructuredDatas")
	if len(q.OrganizationalUnits) > 0 {
		tx = tx.Where("organizational_unit IN ?", lastSegments(q.OrganizationalUnits))
	}
	if q.Organization != "" {
		tx = tx.Where("organizational_unit IN (?)", s.db.Model(&orgunit.OrganizationalUnit{}).
			Select("id").Where("organization = ?", repox.LastSegment(q.Organization)))
	}
	if len(q.Names) > 0 {
		tx = tx.Where("name IN ?", q.Names)
	}
	if len(q.Types) > 0 {
		tx = tx.Where("resource_types && ?", pq.StringArray(q.Types))
	}
	var rows []rfcres.Resource
	if err := tx.Order("name").Find(&rows).Error; err != nil {
		return nil, repox.MapGormErr(err)
	}

	var calIDs []string
	for _, r := range rows {
		if r.CalendarID != nil && !slices.Contains(calIDs, *r.CalendarID) {
			calIDs = append(calIDs, *r.CalendarID)
		}
	}
	zones := map[string]string{}
	if len(calIDs) > 0 {
		var cals []rfccal.Calendar
		if err := s.db.WithContext(ctx).Select("id", "time_zone_id").Where("id IN ?", calIDs).Find(&cals).Error; err != nil {
			return nil, repox.MapGormErr(err)
		}
		for _, c := range cals {
			zones[c.ID] = repox.Deref(c.TimeZoneID)
		}
	}

	out := make([]svc.ResourceRecord, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		pb := rfcres.ResourceToProto(r)
		if r.CalendarID != nil {
			pb.Calendar = "calendars/" + *r.CalendarID
		}
		if r.OrganizationalUnitID != nil {
			pb.OrganizationalUnit = "organizationalUnits/" + *r.OrganizationalUnitID
		}
		for j := range r.StructuredDatas {
			pb.StructuredData = append(pb.StructuredData, structuredData(&r.StructuredDatas[j]))
		}
		out = append(out, svc.ResourceRecord{Resource: pb, TimeZone: zones[repox.Deref(r.CalendarID)]})
	}
	return out, nil
}

// structuredData converts one row, setting the oneof by hand: the generated
// converter leaves oneof members to the caller.
func structuredData(m *rfcres.StructuredData) *resourcepbv1.StructuredData {
	pb := rfcres.StructuredDataToProto(m)
	if m.Text != nil {
		pb.Value = &resourcepbv1.StructuredData_Text{Text: *m.Text}
	}
	return pb
}

// Availability reads every live VAVAILABILITY on the calendars, with periods.
func (s *Store) Availability(ctx context.Context, calendars []string) (map[string][]svc.AvailabilityRecord, error) {
	out := map[string][]svc.AvailabilityRecord{}
	if len(calendars) == 0 {
		return out, nil
	}
	var rows []rfcavail.Availability
	err := s.db.WithContext(ctx).
		Preload("Start").Preload("End").
		Preload("AvailablePeriods.Start").Preload("AvailablePeriods.End").
		Where("calendar_id IN ? AND delete_time IS NULL", lastSegments(calendars)).
		Find(&rows).Error
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	for _, a := range rows {
		start, startDay := availTime(a.Start)
		end, _ := availTime(a.End)
		if repox.Deref(a.EndFormCase) == rfcavail.AvailabilityEndFormCaseDuration && !start.IsZero() {
			end = start.Add(svc.ICalDuration(repox.Deref(a.Duration)))
		}
		rec := svc.AvailabilityRecord{
			Range:       engine.Span{Start: start, End: end},
			RangeAllDay: startDay,
			Priority:    int(repox.Deref(a.Priority)),
		}
		for _, p := range a.AvailablePeriods {
			ps, allDay := availTime(p.Start)
			pe, _ := availTime(p.End)
			if repox.Deref(p.EndFormCase) == rfcavail.AvailablePeriodEndFormCaseDuration || pe.IsZero() {
				pe = ps.Add(svc.ICalDuration(repox.Deref(p.Duration)))
			}
			rec.Periods = append(rec.Periods, svc.PeriodRecord{Start: ps, End: pe, RRule: repox.Deref(p.RecurrenceRule), AllDay: allDay})
		}
		key := "calendars/" + a.CalendarID
		out[key] = append(out[key], rec)
	}
	return out, nil
}

func availTime(t *rfcavail.CalendarTime) (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	if t.DateTime != nil {
		return *t.DateTime, false
	}
	if t.Date != nil {
		return *t.Date, true
	}
	return time.Time{}, false
}

func eventTime(t *rfcevent.CalendarTime) (time.Time, bool) {
	if t == nil {
		return time.Time{}, false
	}
	if t.DateTime != nil {
		return *t.DateTime, false
	}
	if t.Date != nil {
		return *t.Date, true
	}
	return time.Time{}, false
}

// Events reads the busy events on the calendars that may overlap window:
// opaque, not cancelled, starting before the window ends. Recurring events and
// DURATION-form events are returned regardless of their end, which SQL cannot
// see; the engine clips them.
func (s *Store) Events(ctx context.Context, calendars []string, window engine.Span) (map[string][]svc.PeriodRecord, error) {
	out := map[string][]svc.PeriodRecord{}
	if len(calendars) == 0 {
		return out, nil
	}
	var rows []rfcevent.Event
	err := s.db.WithContext(ctx).
		Preload("Start").Preload("End").
		Preload("Recurrence.WeekdayNums").Preload("Recurrence.Until").
		Joins(`JOIN "event"."calendar_times" st ON st.id = "event"."resource".start_id`).
		Joins(`LEFT JOIN "event"."calendar_times" en ON en.id = "event"."resource".end_id`).
		Where(`"event"."resource".calendar_id IN ? AND "event"."resource".delete_time IS NULL`, lastSegments(calendars)).
		Where(`COALESCE("event"."resource".transparency, 'OPAQUE') = 'OPAQUE'`).
		Where(`COALESCE("event"."resource".confirmation, 'CONFIRMED') <> 'CANCELLED'`).
		Where(`COALESCE(st.date_time, st.date::timestamptz) < ?`, window.End.Add(24*time.Hour)).
		Where(`"event"."resource".recurrence_id IS NOT NULL OR en.id IS NULL OR COALESCE(en.date_time, en.date::timestamptz) > ?`, window.Start.Add(-24*time.Hour)).
		Find(&rows).Error
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	for _, e := range rows {
		start, allDay := eventTime(e.Start)
		end, _ := eventTime(e.End)
		if repox.Deref(e.EndsCase) == rfcevent.EventEndsCaseDuration || end.IsZero() {
			d := svc.ICalDuration(repox.Deref(e.Duration))
			if d == 0 && allDay {
				d = 24 * time.Hour // RFC 5545 3.6.1: a DATE event with no end lasts the day
			}
			end = start.Add(d)
		}
		rec := svc.PeriodRecord{Start: start, End: end, AllDay: allDay}
		if r := e.Recurrence; r != nil {
			rec.RRule = recurrence(r).RRule()
		}
		key := "calendars/" + e.CalendarID
		out[key] = append(out[key], rec)
	}
	return out, nil
}

func recurrence(r *rfcevent.Recurrence) svc.RecurrenceParts {
	p := svc.RecurrenceParts{
		Frequency: string(r.Frequency),
		Interval:  r.Interval,
		Count:     r.Count,
		MonthDays: r.MonthDays,
		Unsupported: len(r.SecondNumbers) > 0 || len(r.MinuteNumbers) > 0 || len(r.HourNumbers) > 0 ||
			len(r.YearDays) > 0 || len(r.WeekNumbers) > 0 || len(r.SetPositions) > 0 ||
			repox.Deref(r.Months) != "" || repox.Deref(r.LeapMonths) != "" || repox.Deref(r.Rscale) != "",
	}
	if u, _ := eventTime(r.Until); !u.IsZero() {
		p.Until = &u
	}
	for _, w := range r.WeekdayNums {
		p.ByDay = append(p.ByDay, svc.WeekdayPart{Day: string(w.Day), Ordinal: w.Ordinal})
	}
	return p
}

// liveClaim is the predicate for allocation rows that still count at instant
// now: converted (no expiry) or not yet lapsed.
const liveClaim = "(expire_time IS NULL OR expire_time > ?)"

// Claims reads live allocation rows overlapping window, in one range scan on
// the (resource, start_time, end_time) index.
func (s *Store) Claims(ctx context.Context, resources []string, window engine.Span, now time.Time) (svc.Claims, error) {
	out := svc.Claims{Held: map[string]engine.Spans{}, Converted: map[string]engine.Spans{}}
	if len(resources) == 0 {
		return out, nil
	}
	var rows []allocation.Allocation
	err := s.db.WithContext(ctx).
		Where("resource IN ? AND start_time < ? AND end_time > ? AND "+liveClaim, resources, window.End, window.Start, now).
		Find(&rows).Error
	if err != nil {
		return out, repox.MapGormErr(err)
	}
	for _, r := range rows {
		sp := engine.Spans{{Start: r.StartTime, End: r.EndTime}}
		if r.ExpireTime == nil {
			out.Converted[r.Resource] = out.Converted[r.Resource].Union(sp)
		} else {
			out.Held[r.Resource] = out.Held[r.Resource].Union(sp)
		}
	}
	return out, nil
}

// InsertHold writes the hold and its allocations in one transaction, under a
// transaction-scoped advisory lock per resource.
//
// The locks serialise every writer that could take the same resource, so the
// overlap check below and the insert after it see the same world: two callers
// racing for the last court queue on its lock, and the second finds the
// first's row. Locks are taken in name order so two holds over overlapping
// resource sets cannot deadlock, and they release at commit with no cleanup.
func (s *Store) InsertHold(ctx context.Context, parent string, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	window := svc.SpanOf(h.GetWindow())
	ouID := repox.LastSegment(parent)
	holdID := repox.LastSegment(h.GetName())
	resources := slices.Sorted(slices.Values(h.GetResources()))

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, r := range resources {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "allocation:"+r).Error; err != nil {
				return err
			}
		}
		var taken int64
		if err := tx.Model(&allocation.Allocation{}).
			Where("resource IN ? AND start_time < ? AND end_time > ? AND "+liveClaim, resources, window.End, window.Start, time.Now()).
			Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return types.ErrConflict
		}
		m := allocation.HoldFromProto(h)
		m.ID = holdID
		m.OrganizationalUnitID = ouID
		m.Window = svc.EncodeWindow(window)
		m.Etag = repox.Ptr(repox.NewULID())
		if err := tx.Create(m).Error; err != nil {
			return err
		}
		for _, r := range h.GetResources() {
			id := repox.NewULID()
			row := &allocation.Allocation{
				ID:                   id,
				Name:                 parent + "/allocations/" + id,
				Resource:             r,
				StartTime:            window.Start,
				EndTime:              window.End,
				ExpireTime:           m.ExpireTime,
				OrganizationalUnitID: ouID,
				HoldID:               &holdID,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, types.ErrConflict) {
		return nil, err
	}
	if uniqueViolation(err) {
		return nil, types.ErrAlreadyExists
	}
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return s.GetHold(ctx, h.GetName())
}

// uniqueViolation reports a Postgres unique-constraint failure (SQLSTATE
// 23505). repox.MapGormErr only recognises gorm.ErrDuplicatedKey, which GORM
// produces solely when the connection opts into TranslateError -- and
// freebusy's does not -- so a hold name taken twice would otherwise surface as
// Internal instead of "already exists".
func uniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// GetHold reads one hold.
func (s *Store) GetHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	var m allocation.Hold
	if err := s.db.WithContext(ctx).Where("name = ?", name).First(&m).Error; err != nil {
		return nil, repox.MapGormErr(err)
	}
	return holdToProto(&m), nil
}

// FindHold looks a hold up by idempotency key within its site.
func (s *Store) FindHold(ctx context.Context, parent, key string) (*allocationpbv1.Hold, error) {
	var m allocation.Hold
	err := s.db.WithContext(ctx).
		Where("organizational_unit_id = ? AND idempotency_key = ?", repox.LastSegment(parent), key).
		Order("create_time").First(&m).Error
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return holdToProto(&m), nil
}

// ListHolds pages a site's holds through the generated repository, which owns
// AIP-160 filtering and page tokens, then fills in the windows it cannot map.
func (s *Store) ListHolds(ctx context.Context, parent string, in repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	items, next, err := s.holds.List(ctx, parent, in)
	if err != nil || len(items) == 0 {
		return items, next, err
	}
	names := make([]string, len(items))
	for i, h := range items {
		names[i] = h.GetName()
	}
	var rows []allocation.Hold
	if err := s.db.WithContext(ctx).Select("name", "window").Where("name IN ?", names).Find(&rows).Error; err != nil {
		return nil, "", repox.MapGormErr(err)
	}
	windows := map[string]string{}
	for _, r := range rows {
		windows[r.Name] = r.Window
	}
	for _, h := range items {
		if w, ok := svc.DecodeWindow(windows[h.GetName()]); ok {
			h.Window = svc.IntervalOf(w)
		}
	}
	return items, next, nil
}

// UpdateHold sets state and expiry on the hold and its allocations together,
// guarded by etag with the row locked for the duration.
func (s *Store) UpdateHold(ctx context.Context, name, etag string, state allocationpbv1.HoldState, expire *time.Time) (*allocationpbv1.Hold, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m allocation.Hold
		if err := tx.Set("gorm:query_option", "FOR UPDATE").Where("name = ?", name).First(&m).Error; err != nil {
			return err
		}
		if etag != "" && etag != repox.Deref(m.Etag) {
			return types.ErrConflict
		}
		if err := tx.Model(&allocation.Hold{}).Where("id = ?", m.ID).Updates(map[string]any{
			"state":       allocation.HoldStateFromProto(state),
			"expire_time": expire,
			"etag":        repox.NewULID(),
			"update_time": time.Now(),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&allocation.Allocation{}).Where("hold = ?", m.ID).
			Update("expire_time", expire).Error
	})
	if errors.Is(err, types.ErrConflict) {
		return nil, err
	}
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return s.GetHold(ctx, name)
}

// DeleteHold removes a hold and its allocations, guarded by etag.
func (s *Store) DeleteHold(ctx context.Context, name, etag string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var m allocation.Hold
		if err := tx.Set("gorm:query_option", "FOR UPDATE").Where("name = ?", name).First(&m).Error; err != nil {
			return err
		}
		if etag != "" && etag != repox.Deref(m.Etag) {
			return types.ErrConflict
		}
		if err := tx.Where("hold = ?", m.ID).Delete(&allocation.Allocation{}).Error; err != nil {
			return err
		}
		return tx.Delete(&allocation.Hold{}, "id = ?", m.ID).Error
	})
	if errors.Is(err, types.ErrConflict) {
		return err
	}
	return repox.MapGormErr(err)
}

// ExpireHolds marks lapsed ACTIVE holds EXPIRED in one statement.
func (s *Store) ExpireHolds(ctx context.Context, now time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Model(&allocation.Hold{}).
		Where("state = ? AND expire_time <= ?", allocation.HoldStateActive, now).
		Updates(map[string]any{"state": allocation.HoldStateExpired, "update_time": now, "etag": gorm.Expr("md5(random()::text || id)")})
	return res.RowsAffected, repox.MapGormErr(res.Error)
}

func holdToProto(m *allocation.Hold) *allocationpbv1.Hold {
	pb := allocation.HoldToProto(m)
	if w, ok := svc.DecodeWindow(m.Window); ok {
		pb.Window = svc.IntervalOf(w)
	}
	return pb
}

func lastSegments(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = repox.LastSegment(n)
	}
	return out
}

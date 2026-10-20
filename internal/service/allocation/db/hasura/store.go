// Package hasura is the availability engine's Store on Hasura DDN, through the
// generated freebusyql client. Same contract as the GORM store; where DDN cannot
// lock, the race for a resource is settled by insert-then-verify instead.
package hasura

import (
	"context"
	"slices"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/allocationql/holdsql"
	allocsql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/allocationql/resourceql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/availabilityql/availabilitiesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/availabilityql/availableperiodsql"
	availtimesql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/availabilityql/calendartimesql"
	calsql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/calendarql/resourceql"
	eventtimesql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/eventql/calendartimesql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/eventql/recurrencesql"
	eventsql "github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/eventql/resourceql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/eventql/weekdaynumsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/orgunitql/organizationalunitsql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/resourceql/entityql"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql/resourceql/structureddatasql"
	allocrepo "github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/allocation"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	svc "github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/service/dbutil"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/protobuf/rfc9073/resource/v1/resourcepbv1"
	"github.com/the-protobuf-project/runtime-go/network/graphql"
	"github.com/the-protobuf-project/runtime-go/network/runtime"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Store implements allocation.Store on Hasura.
type Store struct {
	svc   *freebusyql.Service
	holds allocrepo.HoldRepository
}

var _ svc.Store = (*Store)(nil)

// New returns a Store over the freebusyql client.
func New(s *freebusyql.Service) *Store {
	return &Store{svc: s, holds: allocrepo.NewGraphQL(s).Holds}
}

// Resources reads the selected resources, their structured data and their
// calendars' zones. RESOURCE-TYPE is an array column the client cannot filter
// on, so types are matched here after the read.
func (s *Store) Resources(ctx context.Context, q svc.ResourceQuery) ([]svc.ResourceRecord, error) {
	var preds []graphql.Predicate
	if len(q.OrganizationalUnits) > 0 {
		preds = append(preds, entityql.OrganizationalUnit.In(lastSegments(q.OrganizationalUnits)...))
	}
	if q.Organization != "" {
		ous, err := s.svc.Query.Orgunit.OrganizationalUnits.List(ctx, organizationalunitsql.List().
			Where(organizationalunitsql.Organization.Eq(repox.LastSegment(q.Organization))))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		ids := make([]string, len(ous))
		for i, ou := range ous {
			ids[i] = ou.Id
		}
		if len(ids) == 0 {
			return nil, nil
		}
		preds = append(preds, entityql.OrganizationalUnit.In(ids...))
	}
	if len(q.Names) > 0 {
		preds = append(preds, entityql.Name.In(q.Names...))
	}
	req := entityql.List().OrderBy(entityql.Name.Asc())
	if len(preds) > 0 {
		req = req.Where(entityql.And(preds...))
	}
	rows, err := s.svc.Query.Resource.Entity.List(ctx, req)
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	rows = slices.DeleteFunc(rows, func(r entityql.ResourceEntity) bool {
		return len(q.Types) > 0 && !slices.ContainsFunc(r.ResourceTypes, func(t *string) bool {
			return t != nil && slices.Contains(q.Types, *t)
		})
	})
	if len(rows) == 0 {
		return nil, nil
	}

	ids := make([]string, len(rows))
	var calIDs []string
	for i, r := range rows {
		ids[i] = r.Id
		if r.Calendar != nil && !slices.Contains(calIDs, *r.Calendar) {
			calIDs = append(calIDs, *r.Calendar)
		}
	}
	sds, err := s.svc.Query.Resource.StructuredDatas.List(ctx, structureddatasql.List().
		Where(structureddatasql.ResourceId.In(ids...)))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	byResource := map[string][]*resourcepbv1.StructuredData{}
	for _, sd := range sds {
		pb := &resourcepbv1.StructuredData{Schema: repox.Deref(sd.Schema), FormatType: repox.Deref(sd.FormatType)}
		if sd.Text != nil {
			pb.Value = &resourcepbv1.StructuredData_Text{Text: *sd.Text}
		}
		byResource[sd.ResourceId] = append(byResource[sd.ResourceId], pb)
	}
	zones := map[string]string{}
	if len(calIDs) > 0 {
		cals, err := s.svc.Query.Calendar.Resource.List(ctx, calsql.List().Where(calsql.Id.In(calIDs...)))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		for _, c := range cals {
			zones[c.Id] = repox.Deref(c.TimeZoneId)
		}
	}

	out := make([]svc.ResourceRecord, 0, len(rows))
	for _, r := range rows {
		pb := &resourcepbv1.Resource{
			Name:           r.Name,
			DisplayName:    repox.Deref(r.DisplayName),
			Description:    repox.Deref(r.Description),
			StructuredData: byResource[r.Id],
			Etag:           repox.Deref(r.Etag),
		}
		for _, t := range r.ResourceTypes {
			if t != nil {
				pb.ResourceTypes = append(pb.ResourceTypes, *t)
			}
		}
		if r.Calendar != nil {
			pb.Calendar = "calendars/" + *r.Calendar
		}
		if r.OrganizationalUnit != nil {
			pb.OrganizationalUnit = "organizationalUnits/" + *r.OrganizationalUnit
		}
		if t, ok := parseTime(repox.Deref(r.DeleteTime)); ok {
			pb.DeleteTime = timestamppb.New(t)
		}
		out = append(out, svc.ResourceRecord{Resource: pb, TimeZone: zones[repox.Deref(r.Calendar)]})
	}
	return out, nil
}

// Availability reads the live VAVAILABILITY on the calendars: components,
// their periods, then every CalendarTime either references, in three queries.
func (s *Store) Availability(ctx context.Context, calendars []string) (map[string][]svc.AvailabilityRecord, error) {
	out := map[string][]svc.AvailabilityRecord{}
	if len(calendars) == 0 {
		return out, nil
	}
	avs, err := s.svc.Query.Availability.Availabilities.List(ctx, availabilitiesql.List().Where(availabilitiesql.And(
		availabilitiesql.CalendarId.In(lastSegments(calendars)...),
		availabilitiesql.DeleteTime.IsNull(true),
	)))
	if err != nil || len(avs) == 0 {
		return out, dbutil.MapHasuraErr(err)
	}
	avIDs := make([]string, len(avs))
	var timeIDs []string
	for i, a := range avs {
		avIDs[i] = a.Id
		timeIDs = appendIDs(timeIDs, a.StartId, a.EndId)
	}
	periods, err := s.svc.Query.Availability.AvailablePeriods.List(ctx, availableperiodsql.List().
		Where(availableperiodsql.AvailabilityId.In(avIDs...)))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	for _, p := range periods {
		timeIDs = appendIDs(timeIDs, &p.StartId, p.EndId)
	}
	times := map[string]calTime{}
	if len(timeIDs) > 0 {
		rows, err := s.svc.Query.Availability.CalendarTimes.List(ctx, availtimesql.List().Where(availtimesql.Id.In(timeIDs...)))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		for _, t := range rows {
			times[t.Id] = toCalTime(t.DateTime, t.Date)
		}
	}
	byAvail := map[string][]availableperiodsql.AvailabilityAvailablePeriods{}
	for _, p := range periods {
		byAvail[p.AvailabilityId] = append(byAvail[p.AvailabilityId], p)
	}
	for _, a := range avs {
		start := times[repox.Deref(a.StartId)]
		end := times[repox.Deref(a.EndId)]
		if repox.Deref(a.EndFormCase) == "DURATION" && !start.t.IsZero() {
			end = calTime{t: start.t.Add(svc.ICalDuration(repox.Deref(a.Duration))), day: start.day}
		}
		rec := svc.AvailabilityRecord{
			Range:       engine.Span{Start: start.t, End: end.t},
			RangeAllDay: start.day,
			Priority:    int(repox.Deref(a.Priority)),
		}
		for _, p := range byAvail[a.Id] {
			ps := times[p.StartId]
			pe := times[repox.Deref(p.EndId)]
			if repox.Deref(p.EndFormCase) == "DURATION" || pe.t.IsZero() {
				pe = calTime{t: ps.t.Add(svc.ICalDuration(repox.Deref(p.Duration)))}
			}
			rec.Periods = append(rec.Periods, svc.PeriodRecord{Start: ps.t, End: pe.t, RRule: repox.Deref(p.RecurrenceRule), AllDay: ps.day})
		}
		key := "calendars/" + a.CalendarId
		out[key] = append(out[key], rec)
	}
	return out, nil
}

// Events reads the busy events on the calendars. The client cannot join to
// event start times, so the window is applied after the read: events whose
// DTSTART is after the window are dropped, everything else goes to the engine,
// which clips.
func (s *Store) Events(ctx context.Context, calendars []string, window engine.Span) (map[string][]svc.PeriodRecord, error) {
	out := map[string][]svc.PeriodRecord{}
	if len(calendars) == 0 {
		return out, nil
	}
	evs, err := s.svc.Query.Event.Resource.List(ctx, eventsql.List().Where(eventsql.And(
		eventsql.CalendarId.In(lastSegments(calendars)...),
		eventsql.DeleteTime.IsNull(true),
		eventsql.Or(eventsql.Transparency.IsNull(true), eventsql.Transparency.Eq("OPAQUE")),
		eventsql.Or(eventsql.Confirmation.IsNull(true), eventsql.Confirmation.Neq("CANCELLED")),
	)))
	if err != nil || len(evs) == 0 {
		return out, dbutil.MapHasuraErr(err)
	}
	var timeIDs, recIDs []string
	for _, e := range evs {
		timeIDs = appendIDs(timeIDs, &e.StartId, e.EndId)
		recIDs = appendIDs(recIDs, e.RecurrenceId)
	}
	recs := map[string]svc.RecurrenceParts{}
	if len(recIDs) > 0 {
		rows, err := s.svc.Query.Event.Recurrences.List(ctx, recurrencesql.List().Where(recurrencesql.Id.In(recIDs...)))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		days, err := s.svc.Query.Event.WeekdayNums.List(ctx, weekdaynumsql.List().Where(weekdaynumsql.RecurrenceId.In(recIDs...)))
		if err != nil {
			return nil, dbutil.MapHasuraErr(err)
		}
		for _, r := range rows {
			timeIDs = appendIDs(timeIDs, r.UntilId)
		}
		times, err := s.eventTimes(ctx, timeIDs)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			recs[r.Id] = recurrence(r, days, times)
		}
	}
	times, err := s.eventTimes(ctx, timeIDs)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		start := times[e.StartId]
		if start.t.IsZero() || !start.t.Before(window.End.Add(24*time.Hour)) {
			continue
		}
		end := times[repox.Deref(e.EndId)]
		if repox.Deref(e.EndsCase) == "DURATION" || end.t.IsZero() {
			d := svc.ICalDuration(repox.Deref(e.Duration))
			if d == 0 && start.day {
				d = 24 * time.Hour // RFC 5545 3.6.1: a DATE event with no end lasts the day
			}
			end = calTime{t: start.t.Add(d)}
		}
		rec := svc.PeriodRecord{Start: start.t, End: end.t, AllDay: start.day}
		if e.RecurrenceId != nil {
			rec.RRule = recs[*e.RecurrenceId].RRule()
		} else if !end.t.After(window.Start.Add(-24 * time.Hour)) {
			continue
		}
		key := "calendars/" + e.CalendarId
		out[key] = append(out[key], rec)
	}
	return out, nil
}

func (s *Store) eventTimes(ctx context.Context, ids []string) (map[string]calTime, error) {
	out := map[string]calTime{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.svc.Query.Event.CalendarTimes.List(ctx, eventtimesql.List().Where(eventtimesql.Id.In(ids...)))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	for _, t := range rows {
		out[t.Id] = toCalTime(t.DateTime, t.Date)
	}
	return out, nil
}

func recurrence(r recurrencesql.EventRecurrences, days []weekdaynumsql.EventWeekdayNums, times map[string]calTime) svc.RecurrenceParts {
	p := svc.RecurrenceParts{
		Frequency: r.Frequency,
		Interval:  r.Interval,
		Count:     r.Count,
		Unsupported: len(r.SecondNumbers) > 0 || len(r.MinuteNumbers) > 0 || len(r.HourNumbers) > 0 ||
			len(r.YearDays) > 0 || len(r.WeekNumbers) > 0 || len(r.SetPositions) > 0 ||
			repox.Deref(r.Months) != "" || repox.Deref(r.LeapMonths) != "" || repox.Deref(r.Rscale) != "",
	}
	for _, d := range r.MonthDays {
		if d != nil {
			p.MonthDays = append(p.MonthDays, *d)
		}
	}
	if u := times[repox.Deref(r.UntilId)]; !u.t.IsZero() {
		p.Until = &u.t
	}
	for _, d := range days {
		if d.RecurrenceId == r.Id {
			p.ByDay = append(p.ByDay, svc.WeekdayPart{Day: d.Day, Ordinal: d.Ordinal})
		}
	}
	return p
}

// Claims reads live allocation rows overlapping window.
func (s *Store) Claims(ctx context.Context, resources []string, window engine.Span, now time.Time) (svc.Claims, error) {
	out := svc.Claims{Held: map[string]engine.Spans{}, Converted: map[string]engine.Spans{}}
	if len(resources) == 0 {
		return out, nil
	}
	rows, err := s.liveAllocations(ctx, resources, window, now)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		start, _ := parseTime(r.StartTime)
		end, _ := parseTime(r.EndTime)
		sp := engine.Spans{{Start: start, End: end}}
		if r.ExpireTime == nil {
			out.Converted[r.Resource] = out.Converted[r.Resource].Union(sp)
		} else {
			out.Held[r.Resource] = out.Held[r.Resource].Union(sp)
		}
	}
	return out, nil
}

func (s *Store) liveAllocations(ctx context.Context, resources []string, window engine.Span, now time.Time) ([]allocsql.AllocationResource, error) {
	rows, err := s.svc.Query.Allocation.Resource.List(ctx, allocsql.List().Where(allocsql.And(
		allocsql.Resource.In(resources...),
		allocsql.StartTime.Lt(ts(window.End)),
		allocsql.EndTime.Gt(ts(window.Start)),
		allocsql.Or(allocsql.ExpireTime.IsNull(true), allocsql.ExpireTime.Gt(ts(now))),
	)))
	return rows, dbutil.MapHasuraErr(err)
}

// InsertHold writes the hold and its allocations in one DDN transaction, then
// verifies.
//
// DDN offers no lock to take first, so the order is reversed: claim, then look.
// After the write lands, any other live allocation overlapping ours on the same
// resource means two writers raced, and this one withdraws. Of two racers at
// least the later reader sees the other's committed row, so at most one keeps
// its claim; under heavy contention both may withdraw, which the service's
// retry absorbs. Never selling a court twice is the property; never refusing
// one is not promised.
func (s *Store) InsertHold(ctx context.Context, parent string, h *allocationpbv1.Hold) (*allocationpbv1.Hold, error) {
	window := svc.SpanOf(h.GetWindow())
	ouID := repox.LastSegment(parent)
	holdID := repox.LastSegment(h.GetName())
	now := time.Now().UTC()
	expire := nullableTime(h.GetExpireTime())

	tx := s.svc.Mutation.Tx()
	var hRes holdsql.InsertAllocationHoldsResponse
	tx.Add(s.svc.Mutation.Allocation.Holds.CreateOp(holdsql.CreateInput{
		Id:                   holdID,
		Name:                 h.GetName(),
		OrganizationalUnitId: ouID,
		Window:               svc.EncodeWindow(window),
		Quantity:             h.GetQuantity(),
		ExpireTime:           ts(h.GetExpireTime().AsTime()),
		State:                "ACTIVE",
		IdempotencyKey:       h.GetIdempotencyKey(),
		Resources:            ptrAll(h.GetResources()),
		CandidateResources:   ptrAll(h.GetCandidateResources()),
		Etag:                 repox.NewULID(),
		CreateTime:           ts(now),
		UpdateTime:           ts(now),
	}, &hRes))
	for _, r := range h.GetResources() {
		id := repox.NewULID()
		var aRes allocsql.InsertAllocationResourceResponse
		in := allocsql.CreateInput{
			Id:                   id,
			Name:                 h.GetName() + "/allocations/" + id,
			Resource:             r,
			StartTime:            ts(window.Start),
			EndTime:              ts(window.End),
			OrganizationalUnitId: ouID,
			HoldId:               holdID,
			CreateTime:           ts(now),
		}
		if v, ok := expire.Get(); ok {
			in.ExpireTime = v
		}
		tx.Add(s.svc.Mutation.Allocation.Resource.CreateOp(in, &aRes))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}

	live, err := s.liveAllocations(ctx, h.GetResources(), window, time.Now())
	if err != nil {
		return nil, err
	}
	for _, a := range live {
		if a.HoldId != holdID {
			if derr := s.deleteHold(ctx, holdID); derr != nil {
				return nil, derr
			}
			return nil, types.ErrConflict
		}
	}
	return s.GetHold(ctx, h.GetName())
}

// GetHold reads one hold.
func (s *Store) GetHold(ctx context.Context, name string) (*allocationpbv1.Hold, error) {
	rows, err := s.svc.Query.Allocation.Holds.List(ctx, holdsql.List().Where(holdsql.Name.Eq(name)).Limit(1))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	if len(rows) == 0 {
		return nil, types.ErrNotFound
	}
	return holdToProto(rows[0]), nil
}

// FindHold looks a hold up by idempotency key within its site.
func (s *Store) FindHold(ctx context.Context, parent, key string) (*allocationpbv1.Hold, error) {
	rows, err := s.svc.Query.Allocation.Holds.List(ctx, holdsql.List().Where(holdsql.And(
		holdsql.OrganizationalUnitId.Eq(repox.LastSegment(parent)),
		holdsql.IdempotencyKey.Eq(key),
	)).OrderBy(holdsql.CreateTime.Asc()).Limit(1))
	if err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	if len(rows) == 0 {
		return nil, types.ErrNotFound
	}
	return holdToProto(rows[0]), nil
}

// ListHolds pages through the generated repository, then fills in windows.
func (s *Store) ListHolds(ctx context.Context, parent string, in repox.ListInput) ([]*allocationpbv1.Hold, string, error) {
	items, next, err := s.holds.List(ctx, parent, in)
	if err != nil || len(items) == 0 {
		return items, next, err
	}
	names := make([]string, len(items))
	for i, h := range items {
		names[i] = h.GetName()
	}
	rows, err := s.svc.Query.Allocation.Holds.List(ctx, holdsql.List().Where(holdsql.Name.In(names...)))
	if err != nil {
		return nil, "", dbutil.MapHasuraErr(err)
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

// UpdateHold sets state and expiry on the hold and its allocations in one
// transaction, with the etag as a pre-check on the hold.
//
// A failed pre-check zeroes the hold's update without aborting the batch, so
// the allocations may have been written with an expiry the hold did not take.
// When that happens they are re-synced to the hold as it now stands -- the
// invariant is that every allocation's expiry equals its hold's -- and the
// caller gets the conflict.
func (s *Store) UpdateHold(ctx context.Context, name, etag string, state allocationpbv1.HoldState, expire *time.Time) (*allocationpbv1.Hold, error) {
	cur, err := s.row(ctx, name)
	if err != nil {
		return nil, err
	}
	if etag == "" {
		etag = repox.Deref(cur.Etag)
	}
	exp := graphql.Null[string]()
	if expire != nil {
		exp = graphql.Value(ts(*expire))
	}
	tx := s.svc.Mutation.Tx()
	var hRes holdsql.UpdateAllocationHoldsByIdResponse
	tx.Add(s.svc.Mutation.Allocation.Holds.UpdateOp(cur.Id, holdsql.UpdateInput{
		State:      graphql.Value(stateName(state)),
		ExpireTime: exp,
		Etag:       graphql.Value(repox.NewULID()),
		UpdateTime: graphql.Value(ts(time.Now())),
	}, &hRes, holdsql.Update().PreCheck(holdsql.Etag.Eq(etag))))
	if err := s.queueAllocationExpiry(ctx, tx, cur.Id, exp); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dbutil.MapHasuraErr(err)
	}
	if hRes.AffectedRows == 0 {
		if err := s.resync(ctx, name); err != nil {
			return nil, err
		}
		return nil, types.ErrConflict
	}
	return s.GetHold(ctx, name)
}

// resync copies a hold's current expiry onto its allocations.
func (s *Store) resync(ctx context.Context, name string) error {
	cur, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	exp := graphql.Null[string]()
	if cur.ExpireTime != nil {
		exp = graphql.Value(*cur.ExpireTime)
	}
	tx := s.svc.Mutation.Tx()
	if err := s.queueAllocationExpiry(ctx, tx, cur.Id, exp); err != nil {
		return err
	}
	return dbutil.MapHasuraErr(tx.Commit(ctx))
}

// queueAllocationExpiry adds an update setting exp on every allocation of a
// hold. DDN updates by key only, so the keys are read first.
func (s *Store) queueAllocationExpiry(ctx context.Context, tx *runtime.Tx, holdID string, exp graphql.Nullable[string]) error {
	rows, err := s.svc.Query.Allocation.Resource.List(ctx, allocsql.List().Where(allocsql.HoldId.Eq(holdID)))
	if err != nil {
		return dbutil.MapHasuraErr(err)
	}
	for _, a := range rows {
		var res allocsql.UpdateAllocationResourceByIdResponse
		tx.Add(s.svc.Mutation.Allocation.Resource.UpdateOp(a.Id, allocsql.UpdateInput{ExpireTime: exp}, &res))
	}
	return nil
}

// DeleteHold removes a hold and its allocations, guarded by etag.
func (s *Store) DeleteHold(ctx context.Context, name, etag string) error {
	cur, err := s.row(ctx, name)
	if err != nil {
		return err
	}
	if etag != "" && etag != repox.Deref(cur.Etag) {
		return types.ErrConflict
	}
	return s.deleteHold(ctx, cur.Id)
}

// deleteHold removes a hold and its allocations in one transaction,
// allocations first so the foreign key is never left dangling.
func (s *Store) deleteHold(ctx context.Context, holdID string) error {
	rows, err := s.svc.Query.Allocation.Resource.List(ctx, allocsql.List().Where(allocsql.HoldId.Eq(holdID)))
	if err != nil {
		return dbutil.MapHasuraErr(err)
	}
	tx := s.svc.Mutation.Tx()
	for _, a := range rows {
		var res allocsql.DeleteAllocationResourceByIdResponse
		tx.Add(s.svc.Mutation.Allocation.Resource.DeleteOp(a.Id, &res))
	}
	var res holdsql.DeleteAllocationHoldsByIdResponse
	tx.Add(s.svc.Mutation.Allocation.Holds.DeleteOp(holdID, &res))
	return dbutil.MapHasuraErr(tx.Commit(ctx))
}

// expireBatch bounds one sweep. A backlog larger than this drains over the
// next ticks; reads never depended on it.
const expireBatch = 500

// ExpireHolds marks lapsed ACTIVE holds EXPIRED, guarded per hold on its state
// so a hold converted mid-sweep is left alone.
func (s *Store) ExpireHolds(ctx context.Context, now time.Time) (int64, error) {
	rows, err := s.svc.Query.Allocation.Holds.List(ctx, holdsql.List().Where(holdsql.And(
		holdsql.State.Eq("ACTIVE"),
		holdsql.ExpireTime.Lte(ts(now)),
	)).Limit(expireBatch))
	if err != nil || len(rows) == 0 {
		return 0, dbutil.MapHasuraErr(err)
	}
	tx := s.svc.Mutation.Tx()
	results := make([]holdsql.UpdateAllocationHoldsByIdResponse, len(rows))
	for i, h := range rows {
		tx.Add(s.svc.Mutation.Allocation.Holds.UpdateOp(h.Id, holdsql.UpdateInput{
			State:      graphql.Value("EXPIRED"),
			Etag:       graphql.Value(repox.NewULID()),
			UpdateTime: graphql.Value(ts(now)),
		}, &results[i], holdsql.Update().PreCheck(holdsql.State.Eq("ACTIVE"))))
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, dbutil.MapHasuraErr(err)
	}
	var n int64
	for _, r := range results {
		n += int64(r.AffectedRows)
	}
	return n, nil
}

// row reads one hold row by name.
func (s *Store) row(ctx context.Context, name string) (holdsql.AllocationHolds, error) {
	rows, err := s.svc.Query.Allocation.Holds.List(ctx, holdsql.List().Where(holdsql.Name.Eq(name)).Limit(1))
	if err != nil {
		return holdsql.AllocationHolds{}, dbutil.MapHasuraErr(err)
	}
	if len(rows) == 0 {
		return holdsql.AllocationHolds{}, types.ErrNotFound
	}
	return rows[0], nil
}

func holdToProto(r holdsql.AllocationHolds) *allocationpbv1.Hold {
	pb := &allocationpbv1.Hold{
		Name:               r.Name,
		Quantity:           repox.Deref(r.Quantity),
		State:              stateValue(repox.Deref(r.State)),
		IdempotencyKey:     repox.Deref(r.IdempotencyKey),
		Etag:               repox.Deref(r.Etag),
		Resources:          derefAll(r.Resources),
		CandidateResources: derefAll(r.CandidateResources),
	}
	if w, ok := svc.DecodeWindow(r.Window); ok {
		pb.Window = svc.IntervalOf(w)
	}
	if t, ok := parseTime(repox.Deref(r.ExpireTime)); ok {
		pb.ExpireTime = timestamppb.New(t)
	}
	if t, ok := parseTime(r.CreateTime); ok {
		pb.CreateTime = timestamppb.New(t)
	}
	if t, ok := parseTime(r.UpdateTime); ok {
		pb.UpdateTime = timestamppb.New(t)
	}
	return pb
}

func stateName(s allocationpbv1.HoldState) string {
	switch s {
	case allocationpbv1.HoldState_HOLD_STATE_ACTIVE:
		return "ACTIVE"
	case allocationpbv1.HoldState_HOLD_STATE_RELEASED:
		return "RELEASED"
	case allocationpbv1.HoldState_HOLD_STATE_EXPIRED:
		return "EXPIRED"
	case allocationpbv1.HoldState_HOLD_STATE_CONVERTED:
		return "CONVERTED"
	}
	return ""
}

func stateValue(s string) allocationpbv1.HoldState {
	return allocationpbv1.HoldState(allocationpbv1.HoldState_value["HOLD_STATE_"+s])
}

// calTime is an RFC 5545 DATE or DATE-TIME as read: the instant, and whether
// it was a DATE.
type calTime struct {
	t   time.Time
	day bool
}

func toCalTime(dateTime, date *string) calTime {
	if t, ok := parseTime(repox.Deref(dateTime)); ok {
		return calTime{t: t}
	}
	if d, err := time.Parse("2006-01-02", repox.Deref(date)); err == nil {
		return calTime{t: d, day: true}
	}
	return calTime{}
}

// ts renders an instant for a timestamptz column, keeping sub-second
// precision so a window round-trips exactly.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableTime(t *timestamppb.Timestamp) graphql.Nullable[string] {
	if t == nil {
		return graphql.Null[string]()
	}
	return graphql.Value(ts(t.AsTime()))
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

func appendIDs(ids []string, more ...*string) []string {
	for _, id := range more {
		if id != nil && *id != "" && !slices.Contains(ids, *id) {
			ids = append(ids, *id)
		}
	}
	return ids
}

func derefAll(in []*string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, *v)
		}
	}
	return out
}

func lastSegments(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = repox.LastSegment(n)
	}
	return out
}

func ptrAll(in []string) []*string {
	out := make([]*string, len(in))
	for i := range in {
		out[i] = &in[i]
	}
	return out
}

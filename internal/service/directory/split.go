// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package directory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	eventv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/genproto/googleapis/type/dayofweek"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// A RECURRENCE-ID;RANGE=THISANDFUTURE override (RFC 5545 section 3.2.13)
// changes the instance it names and every later one. freebusy stores it the
// way CalDAV servers do: the series is split at that instance. The original
// ends just before it (COUNT or UNTIL), and a new series -- the edit, with
// the original's rule -- carries on from it under a UID of its own. Section
// 3.8.4.4: the edit's start minus the instance it names is applied to every
// later instance too.

// Split is what SplitSeries queued.
type Split struct {
	// Series is the original series as written: cut before the split
	// instance, or -- when the split is at its first instance -- the whole
	// series edited in place.
	Series *eventv1.Event
	// Tail is the new series from the split instance on; nil when the split
	// cancels the rest of the series or edits it in place.
	Tail *eventv1.Event
	// Kept are the overrides that stay with Series unchanged: those before
	// the split instance.
	Kept []*eventv1.Event
}

// SplitSeries queues on wr the split of series at the instance edit's
// original_start names: series rewritten (an update guarded by its etag, or a
// create when it carries none), the tail created -- or updated, when an
// earlier attempt of the same split already created it -- and the overrides
// re-homed or deleted. overrides are series' overrides; those with a name are
// stored and are updated or deleted, those without are created. Nothing is
// written until the caller commits wr, so the split lands with whatever else
// the caller queued, in one transaction on GORM.
func SplitSeries(ctx context.Context, repos Repositories, wr Writes, parent string, series *eventv1.Event, overrides []*eventv1.Event, edit *eventv1.Event, loc *time.Location) (Split, error) {
	truncated, tail, rehomed, dropped, err := planSplit(series, overrides, edit, loc)
	if err != nil {
		return Split{}, err
	}
	out := Split{Series: truncated, Tail: tail}
	sAt, _, _ := readTime(edit.GetOriginalStart(), loc)
	for _, o := range overrides {
		if oAt, _, err := readTime(o.GetOriginalStart(), loc); err == nil && oAt.Before(sAt) {
			out.Kept = append(out.Kept, o)
		}
	}

	if truncated.GetEtag() != "" {
		wr.Events.UpdateEvent(truncated, nil)
	} else if _, err := wr.Events.CreateEvent(parent, truncated); err != nil {
		return Split{}, err
	}
	if tail != nil {
		// The tail's name is derived from the series' name and the split
		// instance, so a retry of the same split finds the tail the first
		// attempt made and updates it instead of making a second.
		existing, err := repos.Events.Get(ctx, tail.GetName())
		switch {
		case err == nil && existing.GetDeleteTime() == nil:
			tail.Etag = existing.GetEtag()
			wr.Events.UpdateEvent(tail, nil)
			// The overrides the first attempt moved there are the tail's
			// now: update them rather than create them twice.
			moved, err := repos.Overrides(ctx, existing)
			if err != nil {
				return Split{}, err
			}
			claim(moved, rehomed, loc)
		case err == nil || errors.Is(err, repox.ErrNotFound):
			if _, err := wr.Events.CreateEvent(parent, tail); err != nil {
				return Split{}, err
			}
		default:
			return Split{}, err
		}
	}
	for _, o := range rehomed {
		if o.GetName() != "" {
			wr.Events.UpdateEvent(o, nil)
			continue
		}
		if err := PrepareCreate(o, ""); err != nil {
			return Split{}, err
		}
		if _, err := wr.Events.CreateEvent(parent, o); err != nil {
			return Split{}, err
		}
	}
	for _, o := range dropped {
		if o.GetName() != "" {
			wr.Events.DeleteEvent(o.GetName())
		}
	}
	return out, nil
}

// claim gives each unsaved override in incoming the name and etag of the
// stored override of the same instance, so writing it updates that record.
func claim(stored, incoming []*eventv1.Event, loc *time.Location) {
	byInstant := map[int64]*eventv1.Event{}
	for _, e := range stored {
		if t, _, err := readTime(e.GetOriginalStart(), loc); err == nil {
			byInstant[t.UnixNano()] = e
		}
	}
	for _, e := range incoming {
		if t, _, err := readTime(e.GetOriginalStart(), loc); err == nil && e.GetName() == "" {
			if old, ok := byInstant[t.UnixNano()]; ok {
				e.Name, e.Etag = old.GetName(), old.GetEtag()
			}
		}
	}
}

// FindSeries returns the live recurring series in parent whose UID is uid,
// which a THISANDFUTURE override splits.
func FindSeries(ctx context.Context, repos Repositories, parent, uid string) (*eventv1.Event, error) {
	found, _, err := repos.Events.List(ctx, parent, repox.ListInput{
		PageSize: 1,
		Where: []filterx.Condition{
			{Field: "ical_uid", Op: filterx.OpEq, Value: uid},
			filterx.IsNull("original_start", true),
			filterx.IsNull("recurrence", false),
		},
	})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, servicex.Blocked("SERIES_NOT_FOUND", hints.TypeOf(parent), parent,
			fmt.Sprintf("no recurring event with ical_uid %q is in %s, so there is no series for original_start_range to split", uid, parent),
			"Send the override with the ical_uid of a recurring event in the same calendar, or leave original_start_range unset")
	}
	return found[0], nil
}

// TailName is the resource name of the series split off name at instance at:
// "calendars/c/events/weekly" -> "calendars/c/events/weekly-r20270125t090000z",
// and a CalDAV object "Weekly.ics" -> "Weekly_R20270125T090000Z.ics".
func TailName(name string, at time.Time) string {
	stamp := at.UTC().Format(utcBasic)
	dir, id := "", name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		dir, id = name[:i+1], name[i+1:]
	}
	if i := strings.LastIndex(id, "."); i > 0 {
		return dir + id[:i] + "_R" + stamp + id[i:]
	}
	return dir + id + "-r" + strings.ToLower(stamp)
}

// tailUID is the UID of the series split off uid at instance at, the
// instance's UTC time inserted before any @domain:
// "maint@example.com" -> "maint_R20270125T090000Z@example.com". It is the
// same on every attempt, so a retried split names the same tail.
func tailUID(uid string, at time.Time) string {
	suffix := "_R" + at.UTC().Format(utcBasic)
	if i := strings.LastIndex(uid, "@"); i >= 0 {
		return uid[:i] + suffix + uid[i:]
	}
	return uid + suffix
}

const utcBasic = "20060102T150405Z"

// planSplit works out a THISANDFUTURE split of series at the instance edit's
// original_start names, S, without writing anything:
//
//   - truncated is series ending before S -- COUNT cut to the rule instances
//     before S, else UNTIL set to the last of them, in the form section
//     3.3.10 asks -- with its EXDATEs and RDATEs from S on dropped and its
//     SEQUENCE raised. When S is the first instance, there is nothing before
//     it: truncated is then the whole series edited in place, and tail nil.
//   - tail is edit as a series of its own from S on: the original's rule
//     (COUNT reduced by the instances before S, UNTIL moved by the shift),
//     the EXDATEs and RDATEs from
//     S on moved by edit's shift, a UID derived from the original's, and the
//     original UID in its REFIDs.
//   - rehomed are the overrides after S moved to tail (their original_start
//     shifted, and their start and end when they had not moved).
//   - dropped are the overrides the split ends: the one at S itself, which
//     edit replaces, and, when edit is CANCELLED, every one after S -- a
//     cancelled THISANDFUTURE ends the series there and has no tail.
//
// S must be an instance of series; a rule part the shift would change the
// meaning of (BYDAY with an ordinal, BYSETPOS, a BYMONTHDAY pushed off the
// month) is refused rather than guessed at.
func planSplit(series *eventv1.Event, overrides []*eventv1.Event, edit *eventv1.Event, loc *time.Location) (truncated, tail *eventv1.Event, rehomed, dropped []*eventv1.Event, err error) {
	if series.GetRecurrence() == nil {
		return nil, nil, nil, nil, servicex.Blocked("NOT_RECURRING", hints.TypeOf(series.GetName()), series.GetName(),
			fmt.Sprintf("event %s has no recurrence rule, so there is no series to split", series.GetName()),
			"Edit the event itself, without original_start_range")
	}
	if edit.GetOriginalStart() == nil {
		return nil, nil, nil, nil, servicex.BadField("REQUIRED_FIELD", "event.original_start",
			"original_start_range needs original_start, the instance the change starts from",
			"Set original_start to the instance's start, as the series' rule produces it")
	}
	sAt, _, err := readTime(edit.GetOriginalStart(), loc)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	first, err := allocation.EventSpan(series, loc)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	_, startForm, err := readTime(series.GetStart(), loc)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	// S must be an instance: produced by the rule or an RDATE, not excluded.
	at, _, err := allocation.Occurrences(series, nil, loc, engine.Span{Start: sAt, End: sAt.Add(time.Nanosecond)})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !slices.ContainsFunc(at, sAt.Equal) {
		return nil, nil, nil, nil, servicex.BadField("NOT_AN_INSTANCE", "event.original_start",
			fmt.Sprintf("%s is not an instance of %s: its rule and added dates do not produce it, or it is excluded", sAt.UTC().Format(time.RFC3339), series.GetName()),
			"Set original_start to an instance's start; "+hints.Call("ExpandEvent", map[string]string{"name": series.GetName()})+" lists them")
	}
	rule, err := ruleOf(series, first.Start, loc)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	before := rule.starts(first.Start, sAt)
	isRule := len(rule.starts(sAt, sAt.Add(time.Nanosecond))) > 0
	refuse := func(what string) error {
		return servicex.Blocked("RANGE_NOT_SUPPORTED", hints.TypeOf(series.GetName()), series.GetName(),
			fmt.Sprintf("event %s cannot be split at %s: %s", series.GetName(), sAt.UTC().Format(time.RFC3339), what),
			"Split the series yourself: end it with UNTIL or COUNT before original_start, and create the rest as a new event")
	}
	if !isRule && len(rule.starts(sAt, sAt.AddDate(100, 0, 0))) > 0 {
		return nil, nil, nil, nil, refuse("that instance is an added date (RDATE) with rule instances after it")
	}
	if len(before) == 0 && slices.ContainsFunc(series.GetAdditionalTimes(), func(ct *eventv1.CalendarTime) bool {
		t, _, err := readTime(ct, loc)
		return err == nil && t.Before(sAt)
	}) {
		return nil, nil, nil, nil, refuse("only added dates (RDATE) come before it")
	}

	// The edit's shift, section 3.8.4.4: it applies to every later instance.
	eAt, eForm := sAt, startForm
	if edit.GetStart() != nil {
		if eAt, eForm, err = readTime(edit.GetStart(), loc); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	delta := eAt.Sub(sAt)
	shiftedRule, err := shiftRule(series.GetRecurrence(), sAt.In(first.Start.Location()), eAt)
	if err != nil {
		return nil, nil, nil, nil, refuse(err.Error())
	}
	cancel := edit.GetConfirmation() == eventv1.Confirmation_CONFIRMATION_CANCELLED

	// Overrides before S stay; the one at S is replaced by edit; later ones
	// follow the instances they override, or end with a cancelled series.
	var later []*eventv1.Event
	for _, o := range overrides {
		oAt, _, err := readTime(o.GetOriginalStart(), loc)
		switch {
		case err != nil || oAt.Before(sAt):
		case oAt.Equal(sAt) || cancel:
			dropped = append(dropped, o)
		default:
			later = append(later, o)
		}
	}
	shift := func(ct *eventv1.CalendarTime) *eventv1.CalendarTime {
		t, f, err := readTime(ct, loc)
		if err != nil {
			return ct
		}
		return f.render(t.Add(delta))
	}
	rehome := func(uid string) []*eventv1.Event {
		var out []*eventv1.Event
		for _, o := range later {
			c := proto.Clone(o).(*eventv1.Event)
			c.IcalUid = uid
			if start, _, err := readTime(o.GetStart(), loc); err == nil {
				if orig, _, _ := readTime(o.GetOriginalStart(), loc); start.Equal(orig) {
					c.Start = shift(o.GetStart())
					if o.GetEnd() != nil {
						c.Ends = &eventv1.Event_End{End: shift(o.GetEnd())}
					}
				}
			}
			c.OriginalStart = shift(o.GetOriginalStart())
			out = append(out, c)
		}
		return out
	}
	// fromEdit is edit as a series of its own, starting at its start.
	fromEdit := func() *eventv1.Event {
		c := proto.Clone(edit).(*eventv1.Event)
		c.OriginalStart, c.OriginalStartRange = nil, eventv1.RecurrenceRange_RECURRENCE_RANGE_UNSPECIFIED
		c.Start = eForm.render(eAt)
		if c.GetEnd() == nil && c.GetDuration() == nil {
			switch {
			case series.GetDuration() != nil:
				c.Ends = &eventv1.Event_Duration{Duration: series.GetDuration()}
			case series.GetEnd() != nil:
				_, endForm, err := readTime(series.GetEnd(), loc)
				if err == nil {
					c.Ends = &eventv1.Event_End{End: endForm.render(eAt.Add(first.End.Sub(first.Start)))}
				}
			}
		}
		return c
	}

	if len(before) == 0 {
		// S is the first instance: the whole series changes, in place.
		if cancel {
			whole := proto.Clone(series).(*eventv1.Event)
			whole.Confirmation = eventv1.Confirmation_CONFIRMATION_CANCELLED
			whole.Sequence = series.GetSequence() + 1
			return whole, nil, nil, dropped, nil
		}
		whole := fromEdit()
		whole.Name, whole.Etag, whole.Uid, whole.IcalUid = series.GetName(), series.GetEtag(), series.GetUid(), series.GetIcalUid()
		whole.CreateTime, whole.UpdateTime = series.GetCreateTime(), series.GetUpdateTime()
		whole.Recurrence = shiftedRule
		if u := series.GetRecurrence().GetUntil(); u != nil {
			whole.Recurrence.Bound = &eventv1.Recurrence_Until{Until: shift(u)}
		}
		whole.ExcludedTimes, whole.AdditionalTimes = nil, nil
		for _, x := range series.GetExcludedTimes() {
			whole.ExcludedTimes = append(whole.ExcludedTimes, shift(x))
		}
		for _, x := range series.GetAdditionalTimes() {
			whole.AdditionalTimes = append(whole.AdditionalTimes, shift(x))
		}
		whole.Sequence = max(series.GetSequence(), edit.GetSequence()) + 1
		return whole, nil, rehome(series.GetIcalUid()), dropped, nil
	}

	// The original, ending before S.
	truncated = proto.Clone(series).(*eventv1.Event)
	n := series.GetRecurrence().GetCount()
	if isRule {
		r := proto.Clone(series.GetRecurrence()).(*eventv1.Recurrence)
		if n > 0 {
			r.Bound = &eventv1.Recurrence_Count{Count: int32(len(before))}
		} else {
			r.Bound = &eventv1.Recurrence_Until{Until: untilOf(before[len(before)-1], startForm, loc)}
		}
		truncated.Recurrence = r
	}
	truncated.ExcludedTimes, truncated.AdditionalTimes = nil, nil
	var movedEx, movedAdd []*eventv1.CalendarTime
	for _, x := range series.GetExcludedTimes() {
		if t, _, err := readTime(x, loc); err == nil && !t.Before(sAt) {
			movedEx = append(movedEx, shift(x))
			continue
		}
		truncated.ExcludedTimes = append(truncated.ExcludedTimes, x)
	}
	for _, x := range series.GetAdditionalTimes() {
		t, _, err := readTime(x, loc)
		if err == nil && !t.Before(sAt) {
			if !t.Equal(sAt) {
				movedAdd = append(movedAdd, shift(x))
			}
			continue
		}
		truncated.AdditionalTimes = append(truncated.AdditionalTimes, x)
	}
	truncated.Sequence = series.GetSequence() + 1
	if cancel {
		return truncated, nil, nil, dropped, nil
	}

	// The rest, as a series of its own.
	base := series.GetIcalUid()
	if base == "" {
		base = series.GetUid()
	}
	tail = fromEdit()
	tail.Name, tail.Etag, tail.Uid = TailName(series.GetName(), sAt), "", ""
	tail.CreateTime, tail.UpdateTime, tail.DeleteTime, tail.ExpireTime = nil, nil, nil, nil
	tail.IcalUid = tailUID(base, sAt)
	tail.Recurrence = nil
	if isRule {
		tail.Recurrence = shiftedRule
		switch b := series.GetRecurrence().GetBound().(type) {
		case *eventv1.Recurrence_Count:
			tail.Recurrence.Bound = &eventv1.Recurrence_Count{Count: n - int32(len(before))}
		case *eventv1.Recurrence_Until:
			// UNTIL moves with the instances, so the last one is still the
			// last: kept as it was, a later shift would lose it and an
			// earlier one add one.
			tail.Recurrence.Bound = &eventv1.Recurrence_Until{Until: shift(b.Until)}
		}
	} else if len(movedAdd) > 0 {
		return nil, nil, nil, nil, refuse("that instance is an added date (RDATE) with more added dates after it")
	}
	tail.ExcludedTimes, tail.AdditionalTimes = movedEx, movedAdd
	if base != "" && !slices.Contains(tail.GetRefids(), base) {
		tail.Refids = append(tail.Refids, base)
	}
	return truncated, tail, rehome(tail.GetIcalUid()), dropped, nil
}

// untilOf is UNTIL for a rule whose last instance is last, in the form
// section 3.3.10 requires: a DATE when DTSTART is a DATE, a floating
// DATE-TIME when DTSTART floats, and otherwise UTC.
func untilOf(last time.Time, start timeForm, loc *time.Location) *eventv1.CalendarTime {
	switch {
	case start.date:
		l := last.In(loc)
		return &eventv1.CalendarTime{Value: &eventv1.CalendarTime_Date{Date: &date.Date{
			Year: int32(l.Year()), Month: int32(l.Month()), Day: int32(l.Day()),
		}}}
	case start.zone == nil && start.offset == nil:
		return timeForm{loc: loc}.render(last)
	}
	return timeForm{loc: time.UTC, offset: &durationpb.Duration{}}.render(last)
}

// recurrence is a series' RRULE as the engine expands it, without its
// EXDATEs or RDATEs: COUNT counts these instances.
type recurrence struct {
	rule  engine.Recurrence
	start time.Time
}

func ruleOf(series *eventv1.Event, start time.Time, loc *time.Location) (recurrence, error) {
	parts, _ := allocation.RecurrenceFromProto(series.GetRecurrence(), loc)
	rule, err := engine.ParseRRule(parts.RRule(), start.Location())
	if err != nil {
		return recurrence{}, err
	}
	return recurrence{rule: rule, start: start}, nil
}

// starts returns the rule's instance starts in [from, to).
func (r recurrence) starts(from, to time.Time) []time.Time {
	var out []time.Time
	for _, occ := range r.rule.Occurrences(r.start, time.Nanosecond, engine.Span{Start: from, End: to}) {
		if !occ.Start.Before(from) && occ.Start.Before(to) {
			out = append(out, occ.Start)
		}
	}
	return out
}

// shiftRule returns r with the parts that pin a day or a time of day moved
// from the instance at s to e: the shift an edit applies to every later
// instance. A rule that takes its days and times from DTSTART needs nothing;
// one whose parts would change meaning under the shift is an error.
func shiftRule(r *eventv1.Recurrence, s, e time.Time) (*eventv1.Recurrence, error) {
	out := proto.Clone(r).(*eventv1.Recurrence)
	days := civilDays(e) - civilDays(s)
	dh, dm := e.Hour()-s.Hour(), e.Minute()-s.Minute()
	if days == 0 && dh == 0 && dm == 0 {
		return out, nil
	}
	if len(r.GetSetPositions()) > 0 {
		return nil, errors.New("its rule has BYSETPOS, which a shift of the day or time would change the meaning of")
	}
	if len(r.GetMonths()) > 0 && e.Month() != s.Month() {
		return nil, errors.New("the shift moves it into another month and its rule has BYMONTH")
	}
	if days != 0 {
		for i, d := range r.GetWeekdays() {
			if d.GetOrdinal() != 0 {
				return nil, fmt.Errorf("its rule has BYDAY=%d%s, which a shift by %d days would change the meaning of", d.GetOrdinal(), d.GetDay(), days)
			}
			out.Weekdays[i].Day = dayofweek.DayOfWeek((int(d.GetDay())-1+days%7+7)%7 + 1)
		}
		for i, md := range r.GetMonthDays() {
			v := md + int32(days)
			if md < 0 || v < 1 || v > 31 {
				return nil, fmt.Errorf("its rule has BYMONTHDAY=%d, which a shift by %d days moves off the month", md, days)
			}
			out.MonthDays[i] = v
		}
	}
	for i, h := range r.GetHourNumbers() {
		v := h + int32(dh)
		if v < 0 || v > 23 {
			return nil, fmt.Errorf("its rule has BYHOUR=%d, which the shift moves into another day", h)
		}
		out.HourNumbers[i] = v
	}
	for i, m := range r.GetMinuteNumbers() {
		v := m + int32(dm)
		if v < 0 || v > 59 {
			return nil, fmt.Errorf("its rule has BYMINUTE=%d, which the shift moves into another hour", m)
		}
		out.MinuteNumbers[i] = v
	}
	return out, nil
}

// civilDays numbers t's calendar date in its own zone.
func civilDays(t time.Time) int {
	return int(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

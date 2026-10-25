// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"fmt"
	"strings"
	"time"

	"github.com/shokki-engineering/freebusy/internal/service/allocation/engine"
	"google.golang.org/genproto/googleapis/type/interval"
)

// RecurrenceParts is an RFC 5545 event recurrence as the rfc schema stores it:
// structured, one column per rule part. Both storage adapters fill one from
// their own row types and render it with RRule, so the engine sees one form.
type RecurrenceParts struct {
	// Frequency is the stored FREQ, e.g. "WEEKLY".
	Frequency string
	Interval  *int32
	Count     *int32
	Until     *time.Time
	// ByDay holds stored day names ("MONDAY") with an optional ordinal.
	ByDay     []WeekdayPart
	MonthDays []int32
	// Months are BYMONTH, 1 to 12.
	Months       []int32
	Hours        []int32
	Minutes      []int32
	SetPositions []int32
	// WeekStart is WKST as a day code ("SU"); empty for the default, Monday.
	WeekStart string
	// Unsupported is set when any rule part the engine does not expand is
	// present: BYSECOND, BYYEARDAY, BYWEEKNO, RSCALE. Rendering such a rule
	// faithfully would only have it rejected later, so RRule emits a marker the
	// engine refuses outright.
	Unsupported bool
}

// WeekdayPart is one BYDAY entry.
type WeekdayPart struct {
	Day     string
	Ordinal *int32
}

var dayCodes = map[string]string{
	"MONDAY": "MO", "TUESDAY": "TU", "WEDNESDAY": "WE", "THURSDAY": "TH",
	"FRIDAY": "FR", "SATURDAY": "SA", "SUNDAY": "SU",
}

// RRule renders the parts as an RRULE value. A rule the engine cannot expand
// renders as one it will reject, which the event path turns into "busy from
// here on": a booking nobody can read is a booking, not free time.
func (p RecurrenceParts) RRule() string {
	if p.Unsupported {
		return "FREQ=" + p.Frequency + ";X-UNSUPPORTED=1"
	}
	parts := []string{"FREQ=" + p.Frequency}
	if p.Interval != nil && *p.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", *p.Interval))
	}
	if p.Count != nil && *p.Count > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", *p.Count))
	}
	if p.Until != nil {
		parts = append(parts, "UNTIL="+p.Until.UTC().Format("20060102T150405Z"))
	}
	if len(p.ByDay) > 0 {
		codes := make([]string, 0, len(p.ByDay))
		for _, d := range p.ByDay {
			c := dayCodes[d.Day]
			if d.Ordinal != nil && *d.Ordinal != 0 {
				c = fmt.Sprintf("%d%s", *d.Ordinal, c)
			}
			codes = append(codes, c)
		}
		parts = append(parts, "BYDAY="+strings.Join(codes, ","))
	}
	for _, list := range []struct {
		key  string
		vals []int32
	}{
		{"BYMONTHDAY", p.MonthDays}, {"BYMONTH", p.Months}, {"BYHOUR", p.Hours},
		{"BYMINUTE", p.Minutes}, {"BYSETPOS", p.SetPositions},
	} {
		if len(list.vals) > 0 {
			vals := make([]string, 0, len(list.vals))
			for _, v := range list.vals {
				vals = append(vals, fmt.Sprint(v))
			}
			parts = append(parts, list.key+"="+strings.Join(vals, ","))
		}
	}
	if p.WeekStart != "" {
		parts = append(parts, "WKST="+p.WeekStart)
	}
	return strings.Join(parts, ";")
}

// StoredDuration reads a DURATION column of the rfc schema, for the storage
// adapters resolving an event or period stored with DURATION instead of DTEND. Those columns
// hold a google.protobuf.Duration, which the generated converters write in
// Go's form ("10h0m0s"); rows written before them, or by hand, may hold RFC
// 5545 text ("PT10H"). Either reads; anything else, or a negative value, is
// zero.
func StoredDuration(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return max(d, 0)
	}
	return icalDuration(s)
}

// SpanOf converts a hold's interval, for the adapters.
func SpanOf(iv *interval.Interval) engine.Span {
	return engine.Span{Start: iv.GetStartTime().AsTime(), End: iv.GetEndTime().AsTime()}
}

// IntervalOf converts a span to a google.type.Interval, for the adapters.
func IntervalOf(s engine.Span) *interval.Interval { return intervalOf(s) }

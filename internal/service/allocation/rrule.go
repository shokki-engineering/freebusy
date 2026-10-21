package allocation

import (
	"fmt"
	"strings"
	"time"

	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
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
	// Unsupported is set when any rule part the engine does not expand is
	// present: BYSECOND, BYMINUTE, BYHOUR, BYYEARDAY, BYWEEKNO, BYSETPOS,
	// BYMONTH, RSCALE. Rendering such a rule faithfully would only have it
	// rejected later, so RRule emits a marker the engine refuses outright.
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
	if len(p.MonthDays) > 0 {
		days := make([]string, 0, len(p.MonthDays))
		for _, d := range p.MonthDays {
			days = append(days, fmt.Sprint(d))
		}
		parts = append(parts, "BYMONTHDAY="+strings.Join(days, ","))
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

// EncodeWindow renders a hold's window for its text column, as an ISO 8601
// interval of two RFC 3339 instants. The column predates a typed mapping for
// google.type.Interval, so both adapters write and read it through these two
// functions and agree on the form.
func EncodeWindow(w engine.Span) string {
	return w.Start.UTC().Format(time.RFC3339Nano) + "/" + w.End.UTC().Format(time.RFC3339Nano)
}

// DecodeWindow parses EncodeWindow's output; ok is false for anything else.
func DecodeWindow(s string) (engine.Span, bool) {
	a, b, found := strings.Cut(s, "/")
	if !found {
		return engine.Span{}, false
	}
	start, err1 := time.Parse(time.RFC3339Nano, a)
	end, err2 := time.Parse(time.RFC3339Nano, b)
	if err1 != nil || err2 != nil {
		return engine.Span{}, false
	}
	return engine.Span{Start: start, End: end}, true
}

// SpanOf converts a hold's interval, for the adapters.
func SpanOf(iv *interval.Interval) engine.Span {
	return engine.Span{Start: iv.GetStartTime().AsTime(), End: iv.GetEndTime().AsTime()}
}

// IntervalOf converts a span to a google.type.Interval, for the adapters.
func IntervalOf(s engine.Span) *interval.Interval { return intervalOf(s) }

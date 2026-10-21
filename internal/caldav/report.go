package caldav

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/engine"
	"github.com/oh-tarnished/freebusy/internal/service/rfcsvc"
)

// report answers REPORT, RFC 4791 section 7: the three reports this server
// advertises. Anything else is refused with DAV:supported-report, which is
// how a client learns to fall back (sync-collection to a PROPFIND of etags).
func (h *Handler) report(w http.ResponseWriter, r *http.Request, t target) error {
	body, err := readXML(r)
	if err != nil {
		return err
	}
	switch {
	case body.is(nsCalDAV, "calendar-query"):
		return h.calendarQuery(w, r.Context(), t, body)
	case body.is(nsCalDAV, "calendar-multiget"):
		return h.calendarMultiget(w, r.Context(), body)
	case body.is(nsCalDAV, "free-busy-query"):
		return h.freeBusyQuery(w, r.Context(), t, body)
	}
	return failf(http.StatusForbidden, "D:supported-report", "unsupported REPORT")
}

// dataRequest is what a report asks to see of each match.
type dataRequest struct {
	props  []xml.Name
	expand *engine.Span // C:expand inside C:calendar-data
}

func readDataRequest(body *node) (dataRequest, error) {
	var d dataRequest
	p := body.child(nsDAV, "prop")
	if p == nil {
		return d, nil
	}
	d.props = p.propNames()
	if cd := p.child(nsCalDAV, "calendar-data"); cd != nil {
		if ex := cd.child(nsCalDAV, "expand"); ex != nil {
			sp, err := timeRange(ex, true)
			if err != nil {
				return d, err
			}
			d.expand = &sp
		}
	}
	return d, nil
}

// filter is the part of a calendar-query filter this server evaluates:
// which component, which time, and which UID.
type filter struct {
	component string       // "VEVENT", "VAVAILABILITY", or "" for any
	none      bool         // the component is asked to be absent: nothing matches
	window    *engine.Span // time-range, nil for any
	uid       string       // prop-filter UID text-match, "" for any
	uidNegate bool
}

// readFilter parses C:filter, RFC 4791 section 9.7. What it cannot evaluate
// it refuses with CALDAV:supported-filter rather than ignores: a filter
// silently widened returns objects the client said it did not want.
func readFilter(body *node) (filter, error) {
	var f filter
	fl := body.child(nsCalDAV, "filter")
	if fl == nil {
		return f, nil
	}
	cal := fl.child(nsCalDAV, "comp-filter")
	if cal == nil || cal.attr("name") != "VCALENDAR" {
		return f, failf(http.StatusForbidden, "C:valid-filter", "a filter starts with comp-filter VCALENDAR")
	}
	comp := cal.child(nsCalDAV, "comp-filter")
	if comp == nil {
		return f, nil
	}
	f.component = strings.ToUpper(comp.attr("name"))
	for _, c := range comp.Nodes {
		switch {
		case c.is(nsCalDAV, "is-not-defined"):
			f.none = true
		case c.is(nsCalDAV, "time-range"):
			sp, err := timeRange(&c, false)
			if err != nil {
				return f, err
			}
			f.window = &sp
		case c.is(nsCalDAV, "prop-filter") && strings.EqualFold(c.attr("name"), "UID"):
			tm := c.child(nsCalDAV, "text-match")
			if tm == nil {
				return f, failf(http.StatusForbidden, "C:supported-filter", "a UID prop-filter needs a text-match")
			}
			f.uid, f.uidNegate = tm.Text, tm.attr("negate-condition") == "yes"
		default:
			return f, failf(http.StatusForbidden, "C:supported-filter",
				"filter element %s is not supported; time-range and UID text-match are", c.XMLName.Local)
		}
	}
	return f, nil
}

// timeRange reads the start and end attributes of a time-range or expand
// element, UTC DATE-TIMEs, RFC 4791 section 9.9. An open end is bounded at
// allocation.MaxFreeBusyWindow from the other, since an unbounded rule
// cannot be expanded to infinity; expand requires both ends.
func timeRange(n *node, both bool) (engine.Span, error) {
	parse := func(attr string) (time.Time, bool, error) {
		v := n.attr(attr)
		if v == "" {
			return time.Time{}, false, nil
		}
		t, err := time.Parse(utcStamp, v)
		if err != nil {
			return time.Time{}, false, failf(http.StatusBadRequest, "C:valid-filter", "%s %q is not a UTC DATE-TIME", attr, v)
		}
		return t, true, nil
	}
	start, hasStart, err := parse("start")
	if err != nil {
		return engine.Span{}, err
	}
	end, hasEnd, err := parse("end")
	if err != nil {
		return engine.Span{}, err
	}
	switch {
	case both && (!hasStart || !hasEnd):
		return engine.Span{}, failf(http.StatusBadRequest, "C:valid-filter", "%s needs both start and end", n.XMLName.Local)
	case !hasStart && !hasEnd:
		return engine.Span{}, failf(http.StatusBadRequest, "C:valid-filter", "time-range needs start or end")
	case !hasStart:
		start = end.Add(-allocation.MaxFreeBusyWindow)
	case !hasEnd:
		end = start.Add(allocation.MaxFreeBusyWindow)
	}
	if !end.After(start) {
		return engine.Span{}, failf(http.StatusBadRequest, "C:valid-filter", "time-range end is not after start")
	}
	return engine.Span{Start: start, End: end}, nil
}

// calendarQuery answers calendar-query, RFC 4791 section 7.8.
func (h *Handler) calendarQuery(w http.ResponseWriter, ctx context.Context, t target, body *node) error {
	if t.kind != kindCalendar && t.kind != kindObject {
		return failf(http.StatusForbidden, "", "calendar-query runs on a calendar or a calendar object")
	}
	data, err := readDataRequest(body)
	if err != nil {
		return err
	}
	f, err := readFilter(body)
	if err != nil {
		return err
	}
	c, err := h.calendar(ctx, t.calendar)
	if err != nil {
		return err
	}
	loc := rfcsvc.ZoneOf(c.GetTimeZone().GetId())
	var objs []object
	if t.kind == kindObject {
		o, err := h.object(ctx, t.calendar, t.object)
		if err != nil {
			return err
		}
		objs = []object{o}
	} else if objs, err = h.objects(ctx, t.calendar); err != nil {
		return err
	}
	var out []response
	for _, o := range objs {
		ok, err := f.matches(o, loc)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		resp, err := h.objectResponse(t.calendar, o, data, loc)
		if err != nil {
			return err
		}
		out = append(out, resp)
	}
	writeMultistatus(w, out)
	return nil
}

func (f filter) matches(o object, loc *time.Location) (bool, error) {
	if f.none {
		return false, nil
	}
	switch f.component {
	case "VEVENT":
		if o.event == nil {
			return false, nil
		}
	case "VAVAILABILITY":
		if o.avail == nil {
			return false, nil
		}
	case "":
	default:
		return false, nil // VTODO, VJOURNAL: this server holds none
	}
	if f.uid != "" {
		uid := uidOf(o.event.GetIcalUid(), o.event.GetUid(), o.event.GetName())
		if o.avail != nil {
			uid = uidOf(o.avail.GetIcalUid(), o.avail.GetUid(), o.avail.GetName())
		}
		// RFC 4791 section 9.7.5: text-match is a case-insensitive substring
		// match under the default i;ascii-casemap collation.
		if strings.Contains(strings.ToLower(uid), strings.ToLower(f.uid)) == f.uidNegate {
			return false, nil
		}
	}
	// An availability's range is not narrowed by time here: one with no end
	// covers every window, and RFC 7953 clients fetch the whole set anyway.
	if f.window == nil || o.event == nil {
		return true, nil
	}
	instances, err := rfcsvc.Instances(o.event, o.overrides, loc, *f.window)
	if err != nil {
		return false, err
	}
	return len(instances) > 0, nil
}

// objectResponse renders one matched object with the properties asked for:
// calendar-data in full, or expanded to its instances in the asked window.
func (h *Handler) objectResponse(calendar string, o object, data dataRequest, loc *time.Location) (response, error) {
	res := h.objectResource(calendar, o)
	resp := response{href: res.href}
	names := data.props
	if len(names) == 0 {
		names = []xml.Name{dav("getetag")}
	}
	for _, n := range names {
		if n != caldav("calendar-data") {
			if v, ok := res.props(n); ok {
				resp.found = append(resp.found, prop{name: n, inner: v})
			} else {
				resp.missing = append(resp.missing, n)
			}
			continue
		}
		text, err := objectData(o, data.expand, loc)
		if err != nil {
			return resp, err
		}
		resp.found = append(resp.found, prop{name: n, inner: escapeText(text)})
	}
	return resp, nil
}

func objectData(o object, expand *engine.Span, loc *time.Location) (string, error) {
	if expand == nil || o.event == nil || o.event.GetRecurrence() == nil {
		block, err := objectComponent(o)
		return calendarObject(block), err
	}
	instances, err := rfcsvc.Instances(o.event, o.overrides, loc, *expand)
	if err != nil {
		return "", err
	}
	blocks, err := instanceComponents(o.event, instances, loc)
	if err != nil {
		return "", err
	}
	return calendarObject(blocks...), nil
}

// calendarMultiget answers calendar-multiget, RFC 4791 section 7.9 -- the
// BatchGetEvents of CalDAV. A missing href is a 404 response, not a failure
// of the report.
func (h *Handler) calendarMultiget(w http.ResponseWriter, ctx context.Context, body *node) error {
	data, err := readDataRequest(body)
	if err != nil {
		return err
	}
	var out []response
	for _, n := range body.Nodes {
		if !n.is(nsDAV, "href") {
			continue
		}
		href := strings.TrimSpace(n.Text)
		if u, err := url.Parse(href); err == nil {
			href = u.EscapedPath()
		}
		t, ok := parsePath(href)
		if !ok || t.kind != kindObject {
			out = append(out, response{href: href, status: http.StatusNotFound})
			continue
		}
		c, err := h.calendar(ctx, t.calendar)
		if err != nil {
			out = append(out, response{href: href, status: http.StatusNotFound})
			continue
		}
		o, err := h.object(ctx, t.calendar, t.object)
		if err != nil {
			out = append(out, response{href: href, status: http.StatusNotFound})
			continue
		}
		resp, err := h.objectResponse(t.calendar, o, data, rfcsvc.ZoneOf(c.GetTimeZone().GetId()))
		if err != nil {
			return err
		}
		out = append(out, resp)
	}
	writeMultistatus(w, out)
	return nil
}

// freeBusyQuery answers free-busy-query, RFC 4791 section 7.10, from the
// engine: the same busy time QueryFreeBusy reports and a hold would meet.
func (h *Handler) freeBusyQuery(w http.ResponseWriter, ctx context.Context, t target, body *node) error {
	if t.kind != kindCalendar {
		return failf(http.StatusForbidden, "", "free-busy-query runs on a calendar collection")
	}
	tr := body.child(nsCalDAV, "time-range")
	if tr == nil {
		return failf(http.StatusBadRequest, "C:valid-filter", "free-busy-query needs a time-range")
	}
	window, err := timeRange(tr, true)
	if err != nil {
		return err
	}
	if window.Duration() > allocation.MaxFreeBusyWindow {
		return failf(http.StatusForbidden, "C:max-date-time", "window longer than %v", allocation.MaxFreeBusyWindow)
	}
	c, err := h.calendar(ctx, t.calendar)
	if err != nil {
		return err
	}
	fb, err := h.fb.FreeBusy(ctx, c.GetName(), c.GetTimeZone().GetId(), window)
	if err != nil {
		return err
	}
	text := freeBusyObject(fb, window, h.now())
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(http.StatusOK)
	_, err = io.WriteString(w, text)
	return err
}

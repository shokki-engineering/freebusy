// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/types"
)

// object is one calendar object resource: an event or an availability.
// Exactly one is set. An event that is a recurring series carries the
// overrides of its instances with it, RFC 4791 section 4.1: they share its
// UID, so they are one object, stored as events of their own.
type object struct {
	id        string
	event     *eventv1.Event
	overrides []*eventv1.Event
	avail     *availabilityv1.Availability
}

// etag is the object's entity tag. With overrides it covers each of them as
// well as the series, so editing one instance changes the object's tag.
func (o object) etag() string {
	if o.avail != nil {
		return o.avail.GetEtag()
	}
	if len(o.overrides) == 0 {
		return o.event.GetEtag()
	}
	parts := []string{o.event.GetEtag()}
	for _, e := range o.overrides {
		parts = append(parts, e.GetName()+"\x00"+e.GetEtag())
	}
	slices.Sort(parts[1:])
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// calendar returns the live calendar id, or a 404. A soft-deleted calendar
// is gone as far as CalDAV is concerned.
func (h *Handler) calendar(ctx context.Context, id string) (*calendarv1.Calendar, error) {
	c, err := h.repos.Calendars.Get(ctx, calendarName(id))
	if err != nil {
		return nil, err
	}
	if c.GetDeleteTime() != nil {
		return nil, types.ErrNotFound
	}
	return c, nil
}

// object returns the live object id under calendar: an event, with the
// overrides of its instances, else an availability of that id. An override
// is part of its series' object, not one of its own, so its id is not found.
func (h *Handler) object(ctx context.Context, calendar, id string) (object, error) {
	e, err := h.repos.Events.Get(ctx, calendarName(calendar)+"/events/"+id)
	switch {
	case err == nil && e.GetDeleteTime() == nil:
		if e.GetOriginalStart() != nil {
			if attached, err := h.hasSeries(ctx, e); err != nil || attached {
				return object{}, cmp.Or(err, types.ErrNotFound)
			}
			return object{id: id, event: e}, nil
		}
		overrides, err := h.repos.Overrides(ctx, e)
		if err != nil {
			return object{}, err
		}
		return object{id: id, event: e, overrides: overrides}, nil
	case err != nil && !errors.Is(err, types.ErrNotFound):
		return object{}, err
	}
	a, err := h.repos.Availabilities.Get(ctx, calendarName(calendar)+"/availabilities/"+id)
	switch {
	case err == nil && a.GetDeleteTime() == nil:
		return object{id: id, avail: a}, nil
	case err != nil && !errors.Is(err, types.ErrNotFound):
		return object{}, err
	}
	return object{}, types.ErrNotFound
}

// hasSeries reports whether override belongs to a live recurring series in
// its calendar, which makes it part of that series' object.
func (h *Handler) hasSeries(ctx context.Context, override *eventv1.Event) (bool, error) {
	series, _, err := h.repos.Events.List(ctx, parentOf(override.GetName()), repox.ListInput{
		PageSize: 1,
		Where: []filterx.Condition{
			{Field: "ical_uid", Op: filterx.OpEq, Value: override.GetIcalUid()},
			filterx.IsNull("original_start", true),
			filterx.IsNull("recurrence", false),
		},
	})
	return len(series) > 0, err
}

// objects returns every live object in a calendar, events first. Overrides
// are gathered into their series' object; one whose series is not in the
// calendar stands as an object of its own.
func (h *Handler) objects(ctx context.Context, calendar string) ([]object, error) {
	var events []*eventv1.Event
	parent := calendarName(calendar)
	for token := ""; ; {
		page, next, err := h.repos.Events.List(ctx, parent, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		if token = next; token == "" {
			break
		}
	}
	var out []object
	series := map[string]int{}
	for _, e := range events {
		if e.GetOriginalStart() != nil {
			continue
		}
		if e.GetRecurrence() != nil && e.GetIcalUid() != "" {
			series[e.GetIcalUid()] = len(out)
		}
		out = append(out, object{id: lastSegment(e.GetName()), event: e})
	}
	for _, e := range events {
		if e.GetOriginalStart() == nil {
			continue
		}
		if i, ok := series[e.GetIcalUid()]; ok {
			out[i].overrides = append(out[i].overrides, e)
		} else {
			out = append(out, object{id: lastSegment(e.GetName()), event: e})
		}
	}
	for token := ""; ; {
		page, next, err := h.repos.Availabilities.List(ctx, parent, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, a := range page {
			out = append(out, object{id: lastSegment(a.GetName()), avail: a})
		}
		if token = next; token == "" {
			break
		}
	}
	return out, nil
}

// calendars returns every live calendar.
func (h *Handler) calendars(ctx context.Context) ([]*calendarv1.Calendar, error) {
	var out []*calendarv1.Calendar
	for token := ""; ; {
		page, next, err := h.repos.Calendars.List(ctx, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if token = next; token == "" {
			return out, nil
		}
	}
}

// ctag summarises a calendar's state for CalendarServer's getctag: it changes
// whenever any member, or the calendar itself, does. Derived from etags the
// repositories already rotate on every write, so it needs no storage.
func ctag(c *calendarv1.Calendar, objs []object) string {
	h := sha256.New()
	h.Write([]byte(c.GetEtag()))
	for _, o := range objs {
		h.Write([]byte{0})
		h.Write([]byte(o.id))
		h.Write([]byte{0})
		h.Write([]byte(o.etag()))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// parentOf is the collection an event sits in.
// "calendars/c/events/e" -> "calendars/c".
func parentOf(name string) string {
	if i := strings.LastIndex(name, "/events/"); i >= 0 {
		return name[:i]
	}
	return name
}

func lastSegment(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:]
		}
	}
	return name
}

// quoteETag renders an entity tag for an ETag header or getetag property.
func quoteETag(etag string) string { return `"` + etag + `"` }

// matchesIf reports whether stored satisfies an If-Match or If-None-Match
// header value: "*" matches anything that exists, otherwise any listed
// entity tag. Weak tags compare by value, which is all a calendar client
// sends.
func matchesIf(header, stored string, exists bool) bool {
	if header == "*" {
		return exists
	}
	if !exists {
		return false
	}
	for _, t := range splitTags(header) {
		if t == stored {
			return true
		}
	}
	return false
}

func splitTags(header string) []string {
	var out []string
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		out = append(out, strings.Trim(t, `"`))
	}
	return out
}

// checkPreconditions applies If-Match and If-None-Match to a resource that
// exists with etag stored (or does not exist), RFC 9110 section 13.
func checkPreconditions(r *http.Request, stored string, exists bool) error {
	if v := r.Header.Get("If-Match"); v != "" && !matchesIf(v, stored, exists) {
		return failf(http.StatusPreconditionFailed, "", "If-Match %s does not match", v)
	}
	if v := r.Header.Get("If-None-Match"); v != "" && matchesIf(v, stored, exists) {
		return failf(http.StatusPreconditionFailed, "", "If-None-Match %s matches", v)
	}
	return nil
}

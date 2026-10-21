package caldav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/calendar/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc5545/event/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc7953/availability/v1"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/types"
)

// object is one calendar object resource: an event or an availability.
// Exactly one is set.
type object struct {
	id    string
	event *eventv1.Event
	avail *availabilityv1.Availability
}

func (o object) etag() string {
	if o.event != nil {
		return o.event.GetEtag()
	}
	return o.avail.GetEtag()
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

// object returns the live object id under calendar: an event, else an
// availability of that id.
func (h *Handler) object(ctx context.Context, calendar, id string) (object, error) {
	e, err := h.repos.Events.Get(ctx, calendarName(calendar)+"/events/"+id)
	switch {
	case err == nil && e.GetDeleteTime() == nil:
		return object{id: id, event: e}, nil
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

// objects returns every live object in a calendar, events first.
func (h *Handler) objects(ctx context.Context, calendar string) ([]object, error) {
	var out []object
	parent := calendarName(calendar)
	for token := ""; ; {
		page, next, err := h.repos.Events.List(ctx, parent, repox.ListInput{PageSize: 1000, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			out = append(out, object{id: lastSegment(e.GetName()), event: e})
		}
		if token = next; token == "" {
			break
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

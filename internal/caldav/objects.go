package caldav

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/oh-tarnished/freebusy/internal/service/rfcsvc"
	"github.com/oh-tarnished/freebusy/internal/types"
)

// get answers GET and HEAD. An object is served as its own VCALENDAR; a
// calendar collection as one VCALENDAR holding every member, which is the
// whole calendar as a .ics file -- the export a subscribing client reads.
func (h *Handler) get(w http.ResponseWriter, r *http.Request, t target) error {
	var body, etag string
	switch t.kind {
	case kindObject:
		if _, err := h.calendar(r.Context(), t.calendar); err != nil {
			return err
		}
		o, err := h.object(r.Context(), t.calendar, t.object)
		if err != nil {
			return err
		}
		block, err := objectComponent(o)
		if err != nil {
			return err
		}
		body, etag = calendarObject(block), o.etag()
	case kindCalendar:
		c, err := h.calendar(r.Context(), t.calendar)
		if err != nil {
			return err
		}
		objs, err := h.objects(r.Context(), t.calendar)
		if err != nil {
			return err
		}
		blocks := make([]string, 0, len(objs))
		for _, o := range objs {
			block, err := objectComponent(o)
			if err != nil {
				return err
			}
			blocks = append(blocks, block)
		}
		body, etag = calendarObject(blocks...), ctag(c, objs)
		w.Header().Set("Content-Disposition", `attachment; filename="`+t.calendar+`.ics"`)
	default:
		return failf(http.StatusMethodNotAllowed, "", "GET is served on calendars and calendar objects")
	}
	w.Header().Set("Content-Type", MediaType)
	w.Header().Set("ETag", quoteETag(etag))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if v := r.Header.Get("If-None-Match"); v != "" && matchesIf(v, etag, true) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, body)
	}
	return nil
}

// put answers PUT of a calendar object resource, RFC 4791 section 5.3.2: a
// create when the name is new, a full replace when it exists. If-Match and
// If-None-Match are honoured, so a client's conditional write cannot clobber
// someone else's.
func (h *Handler) put(w http.ResponseWriter, r *http.Request, t target) error {
	if t.kind != kindObject {
		return failf(http.StatusMethodNotAllowed, "", "PUT writes calendar objects; MKCALENDAR creates calendars")
	}
	ctx := r.Context()
	// RFC 4791 section 5.3.2: a PUT into a collection that is not there is a
	// conflict, not a 404 on the object.
	if _, err := h.calendar(ctx, t.calendar); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			return failf(http.StatusConflict, "", "calendar %s does not exist", t.calendar)
		}
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return failf(http.StatusForbidden, "C:max-resource-size", "calendar object over %d bytes", maxBody)
	}
	d, err := decode(string(raw))
	if err != nil {
		return err
	}

	existing, err := h.object(ctx, t.calendar, t.object)
	exists := err == nil
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return err
	}
	if err := checkPreconditions(r, existing.etag(), exists); err != nil {
		return err
	}
	if exists && (existing.event != nil) != (d.event != nil) {
		return failf(http.StatusForbidden, "C:valid-calendar-object-resource",
			"%s holds a different component type; delete it first", t.object)
	}

	parent := calendarName(t.calendar)
	var etag string
	switch {
	case d.event != nil && exists:
		d.event.Name, d.event.Etag = existing.event.GetName(), existing.etag()
		out, err := h.repos.Events.Update(ctx, d.event, nil)
		if err != nil {
			return err
		}
		etag = out.GetEtag()
	case d.event != nil:
		rfcsvc.PrepareCreate(d.event, parent+"/events/"+t.object)
		out, err := h.repos.Events.Create(ctx, parent, d.event)
		if err != nil {
			return err
		}
		etag = out.GetEtag()
	case exists:
		d.avail.Name, d.avail.Etag = existing.avail.GetName(), existing.etag()
		out, err := h.repos.Availabilities.Update(ctx, d.avail, nil)
		if err != nil {
			return err
		}
		etag = out.GetEtag()
	default:
		rfcsvc.PrepareCreate(d.avail, parent+"/availabilities/"+t.object)
		out, err := h.repos.Availabilities.Create(ctx, parent, d.avail)
		if err != nil {
			return err
		}
		etag = out.GetEtag()
	}
	w.Header().Set("ETag", quoteETag(etag))
	if exists {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
	return nil
}

// delete answers DELETE. Deletes are soft, AIP-164, so the gRPC API can
// still undelete what a calendar client removed. A calendar takes its
// members with it, as a WebDAV collection must.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, t target) error {
	ctx := r.Context()
	switch t.kind {
	case kindObject:
		if _, err := h.calendar(ctx, t.calendar); err != nil {
			return err
		}
		o, err := h.object(ctx, t.calendar, t.object)
		if err != nil {
			return err
		}
		if err := checkPreconditions(r, o.etag(), true); err != nil {
			return err
		}
		if o.event != nil {
			err = h.repos.Events.Delete(ctx, o.event.GetName())
		} else {
			err = h.repos.Availabilities.Delete(ctx, o.avail.GetName())
		}
		if err != nil {
			return err
		}
	case kindCalendar:
		c, err := h.calendar(ctx, t.calendar)
		if err != nil {
			return err
		}
		objs, err := h.objects(ctx, t.calendar)
		if err != nil {
			return err
		}
		if err := checkPreconditions(r, ctag(c, objs), true); err != nil {
			return err
		}
		for _, o := range objs {
			if o.event != nil {
				err = h.repos.Events.Delete(ctx, o.event.GetName())
			} else {
				err = h.repos.Availabilities.Delete(ctx, o.avail.GetName())
			}
			if err != nil && !errors.Is(err, types.ErrNotFound) {
				return err
			}
		}
		if err := h.repos.Calendars.Delete(ctx, c.GetName()); err != nil {
			return err
		}
	default:
		return failf(http.StatusForbidden, "", "%s cannot be deleted", t.href())
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

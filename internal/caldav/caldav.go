// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package caldav serves freebusy's calendars over CalDAV, RFC 4791, so an
// operator can block out a resource from any calendar app and the engine sees
// it at once.
//
// It is an adapter, not a second store. protobuf-rfc maps CalDAV onto the
// services freebusy already runs -- MKCALENDAR is CreateCalendar, a
// calendar-query time-range is a filter over events, calendar-multiget is
// BatchGetEvents, the free-busy-query REPORT is QueryFreeBusy -- so every
// request here reads and writes the same generated repositories the gRPC API
// does, and iCalendar crosses the boundary through runtime-go/rfc's codecs.
//
// Layout, under Prefix:
//
//	/.well-known/caldav             -> 301 to /dav/ (RFC 6764)
//	/dav/                            root
//	/dav/principals/default/         the one principal
//	/dav/calendars/                  calendar home: every calendar
//	/dav/calendars/{calendar}/       a calendar collection
//	/dav/calendars/{calendar}/{obj}  an event, or an RFC 7953 availability
//
// An object's name is its resource id verbatim, so a client's "{uuid}.ics"
// round-trips, and an event made over gRPC appears under its id.
//
// There is no authentication: every request acts as the single principal.
// That is why the server is off unless [caldav] enabled is set, and the
// release configuration leaves it off.
package caldav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/shokki-engineering/freebusy/internal/service/rfcsvc"
	"github.com/shokki-engineering/freebusy/internal/types"
)

// Prefix is where the CalDAV tree is mounted.
const Prefix = "/dav"

// Handler answers CalDAV requests over the RFC repositories.
type Handler struct {
	repos rfcsvc.Repositories
	fb    rfcsvc.FreeBusyQuerier
	now   func() time.Time
}

// New returns a Handler over repos, answering free-busy-query from fb.
func New(repos rfcsvc.Repositories, fb rfcsvc.FreeBusyQuerier) *Handler {
	return &Handler{repos: repos, fb: fb, now: time.Now}
}

// methods are the HTTP methods CalDAV uses: RFC 4918's WebDAV methods, RFC
// 4791's MKCALENDAR, and the plain HTTP ones on calendar object resources.
var methods = []string{
	http.MethodOptions, "PROPFIND", "PROPPATCH", "REPORT", "MKCALENDAR",
	http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete,
}

// Mount registers the handler on the REST gateway's mux, so CalDAV shares
// the HTTP port and the gateway keeps every other path.
func (h *Handler) Mount(mux *runtime.ServeMux) error {
	serve := func(w http.ResponseWriter, r *http.Request, _ map[string]string) { h.ServeHTTP(w, r) }
	for _, m := range methods {
		for _, p := range []string{Prefix, Prefix + "/{path=**}"} {
			if err := mux.HandlePath(m, p, serve); err != nil {
				return fmt.Errorf("caldav: mount %s %s: %w", m, p, err)
			}
		}
	}
	// RFC 6764 section 5: clients probe the well-known path first.
	wellKnown := func(w http.ResponseWriter, r *http.Request, _ map[string]string) {
		http.Redirect(w, r, Prefix+"/", http.StatusMovedPermanently)
	}
	for _, m := range []string{http.MethodGet, http.MethodHead, "PROPFIND", http.MethodOptions} {
		if err := mux.HandlePath(m, "/.well-known/caldav", wellKnown); err != nil {
			return fmt.Errorf("caldav: mount well-known: %w", err)
		}
	}
	return nil
}

// ServeHTTP dispatches one CalDAV request.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	t, ok := parsePath(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	var err error
	switch r.Method {
	case http.MethodOptions:
		h.options(w)
	case "PROPFIND":
		err = h.propfind(w, r, t)
	case "PROPPATCH":
		err = h.proppatch(w, r, t)
	case "REPORT":
		err = h.report(w, r, t)
	case "MKCALENDAR":
		err = h.mkcalendar(w, r, t)
	case http.MethodGet, http.MethodHead:
		err = h.get(w, r, t)
	case http.MethodPut:
		err = h.put(w, r, t)
	case http.MethodDelete:
		err = h.delete(w, r, t)
	default:
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
	if err != nil {
		writeError(w, err)
	}
}

func (h *Handler) options(w http.ResponseWriter) {
	// RFC 4791 section 5.1: calendar-access in the DAV header is how a
	// client learns this is a CalDAV server. Class 3 is RFC 4918's.
	w.Header().Set("DAV", "1, 3, calendar-access")
	w.Header().Set("Allow", strings.Join(methods, ", "))
	w.WriteHeader(http.StatusOK)
}

// kind is what a request path names.
type kind int

const (
	kindRoot kind = iota
	kindPrincipal
	kindHome
	kindCalendar
	kindObject
)

// target is a parsed request path.
type target struct {
	kind     kind
	calendar string // calendar id, for kindCalendar and kindObject
	object   string // object id, for kindObject
}

const principalPath = Prefix + "/principals/default/"

// parsePath reads an escaped request path. Segments are unescaped one by one,
// so an id holding an encoded slash stays one segment.
func parsePath(escaped string) (target, bool) {
	rest, ok := strings.CutPrefix(escaped, Prefix)
	if !ok || (rest != "" && rest[0] != '/') {
		return target{}, false
	}
	var segs []string
	for _, s := range strings.Split(rest, "/") {
		if s == "" {
			continue
		}
		u, err := url.PathUnescape(s)
		if err != nil || u == "" {
			return target{}, false
		}
		segs = append(segs, u)
	}
	switch {
	case len(segs) == 0:
		return target{kind: kindRoot}, true
	case len(segs) == 2 && segs[0] == "principals" && segs[1] == "default":
		return target{kind: kindPrincipal}, true
	case len(segs) >= 1 && segs[0] == "calendars":
		switch len(segs) {
		case 1:
			return target{kind: kindHome}, true
		case 2:
			return target{kind: kindCalendar, calendar: segs[1]}, true
		case 3:
			return target{kind: kindObject, calendar: segs[1], object: segs[2]}, true
		}
	}
	return target{}, false
}

// href is the canonical path of t: collections end in a slash.
func (t target) href() string {
	switch t.kind {
	case kindPrincipal:
		return principalPath
	case kindHome:
		return Prefix + "/calendars/"
	case kindCalendar:
		return calendarHref(t.calendar)
	case kindObject:
		return calendarHref(t.calendar) + url.PathEscape(t.object)
	}
	return Prefix + "/"
}

func calendarHref(id string) string { return Prefix + "/calendars/" + url.PathEscape(id) + "/" }

// calendarName is the resource name of calendar id.
func calendarName(id string) string { return "calendars/" + id }

// statusError is an HTTP failure with an optional RFC 4918 section 16
// precondition element naming the condition that failed.
type statusError struct {
	status       int
	precondition string // e.g. "C:valid-calendar-data"; empty for none
	msg          string
}

func (e *statusError) Error() string { return e.msg }

func failf(status int, precondition, format string, args ...any) error {
	return &statusError{status: status, precondition: precondition, msg: fmt.Sprintf(format, args...)}
}

// writeError renders err: a statusError as itself, a repository sentinel as
// the status it means, anything else as 500.
func writeError(w http.ResponseWriter, err error) {
	var se *statusError
	if !errors.As(err, &se) {
		se = &statusError{status: http.StatusInternalServerError, msg: err.Error()}
		switch {
		case errors.Is(err, types.ErrNotFound):
			se.status = http.StatusNotFound
		case errors.Is(err, types.ErrConflict):
			se.status = http.StatusPreconditionFailed
		case errors.Is(err, types.ErrAlreadyExists):
			se.status = http.StatusPreconditionFailed
		case errors.Is(err, types.ErrInvalidArgument):
			se.status = http.StatusBadRequest
		case errors.Is(err, types.ErrInvalidState):
			se.status = http.StatusConflict
		case errors.Is(err, context.Canceled):
			se.status = 499
		}
	}
	if se.precondition == "" {
		http.Error(w, se.msg, se.status)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(se.status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+
		`<D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><%s/><D:responsedescription>%s</D:responsedescription></D:error>`,
		se.precondition, escapeText(se.msg))
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

// Tests of the read-only feeds over runtime-go's in-memory CalDAV store: the
// right token gets the whole calendar, anything else a 404, and nothing but
// GET and HEAD gets through.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/the-protobuf-project/runtime-go/rfc/caldav"
	"github.com/the-protobuf-project/runtime-go/rfc/caldav/memstore"
)

const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\n" +
	"UID:wedding-1\r\nDTSTAMP:20261026T000000Z\r\nDTSTART:20261205T090000Z\r\n" +
	"DTEND:20261205T170000Z\r\nSUMMARY:Sharma wedding · 300 guests\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// server is a CalDAV tree holding calendar hall-1 with one event, and the
// feeds in front of it.
func server(t *testing.T) (*Feed, http.Handler) {
	t.Helper()
	dav := caldav.New(memstore.New())
	for _, req := range []*http.Request{
		httptest.NewRequest("MKCALENDAR", "/dav/calendars/hall-1/", nil),
		httptest.NewRequest(http.MethodPut, "/dav/calendars/hall-1/wedding-1.ics", strings.NewReader(event)),
	} {
		rec := httptest.NewRecorder()
		dav.ServeHTTP(rec, req)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", req.Method, req.URL, rec.Code, rec.Body)
		}
	}
	feed, err := NewFeed(dav, []byte(strings.Repeat("s", MinFeedSecret)))
	if err != nil {
		t.Fatal(err)
	}
	mux := runtime.NewServeMux()
	if err := feed.Mount(mux); err != nil {
		t.Fatal(err)
	}
	return feed, mux
}

func TestFeed(t *testing.T) {
	feed, mux := server(t)
	link := feed.URL("https://freebusy.example", "calendars/hall-1")
	path := strings.TrimPrefix(link, "https://freebusy.example")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
		t.Errorf("Content-Type = %q; want text/calendar", ct)
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("the feed is served as a download")
	}
	if body := rec.Body.String(); !strings.Contains(body, "BEGIN:VCALENDAR") || !strings.Contains(body, "Sharma wedding") {
		t.Errorf("body lacks the calendar's event:\n%s", body)
	}

	for name, p := range map[string]string{
		"no token":         "/feeds/calendars/hall-1.ics",
		"wrong token":      "/feeds/calendars/hall-1.ics?token=" + feed.Token("calendars/hall-2"),
		"no .ics":          strings.Replace(path, ".ics", "", 1),
		"unknown calendar": "/feeds/calendars/hall-9.ics?token=" + feed.Token("calendars/hall-9"),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: GET %s = %d; want 404", name, p, rec.Code)
		}
	}

	for _, m := range []string{http.MethodPut, http.MethodDelete, "PROPFIND"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(m, path, strings.NewReader(event)))
		if rec.Code < 400 {
			t.Errorf("%s through the feed = %d; want refused", m, rec.Code)
		}
	}
}

func TestFeedSecretTooShort(t *testing.T) {
	if _, err := NewFeed(http.NotFoundHandler(), []byte("short")); err == nil {
		t.Error("a short secret was accepted")
	}
}

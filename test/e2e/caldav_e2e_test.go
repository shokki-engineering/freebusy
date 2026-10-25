// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// The CalDAV suite: a calendar client's view of freebusy, driven over real
// HTTP against the handler mounted exactly as the server mounts it, on both
// backends.
//
//	FREEBUSY_TEST_POSTGRES_DSN=... FREEBUSY_TEST_GRAPHQL_URL=... go test ./internal/e2e/ -run CalDAV -v
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/shokki-engineering/freebusy/internal/caldav"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
)

func TestCalDAV_Gorm(t *testing.T) {
	gdb := openTestGorm(t)
	caldavFlow(t, &database.Connection{PgSQLConn: gdb, Provider: database.ProviderGorm})
}

func TestCalDAV_Hasura(t *testing.T) {
	svc := connectTestGraphQL(t)
	caldavFlow(t, &database.Connection{Hasura: svc, Provider: database.ProviderHasura})
}

// davClient sends WebDAV requests to one test server.
type davClient struct {
	t    *testing.T
	base string
}

func (c davClient) do(method, path, body string, headers ...string) (*http.Response, string) {
	c.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func (c davClient) want(method, path, body string, status int, headers ...string) (*http.Response, string) {
	c.t.Helper()
	resp, out := c.do(method, path, body, headers...)
	if resp.StatusCode != status {
		c.t.Fatalf("%s %s: status %d, want %d\n%s", method, path, resp.StatusCode, status, out)
	}
	return resp, out
}

func mustContain(t *testing.T, what, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Fatalf("%s: missing %q in\n%s", what, p, body)
		}
	}
}

// madrid is the VTIMEZONE block Apple Calendar sends with every zoned event.
const madrid = "BEGIN:VTIMEZONE\r\nTZID:Europe/Madrid\r\nBEGIN:DAYLIGHT\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nDTSTART:19810329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nTZNAME:CEST\r\nEND:DAYLIGHT\r\nBEGIN:STANDARD\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nDTSTART:19961027T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nTZNAME:CET\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"

func caldavFlow(t *testing.T, conn *database.Connection) {
	repos := directory.NewRepositories(conn.Repox(), 0)
	h := caldav.New(repos, allocation.NewServer(db.New(conn)))
	mux := runtime.NewServeMux()
	if err := h.Mount(mux); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := davClient{t: t, base: srv.URL}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	cal := "/dav/calendars/court-" + suffix + "/"
	calName := "calendars/court-" + suffix
	// A Monday 40-odd days out, so nothing is in the past.
	day := time.Now().UTC().AddDate(0, 0, 40).Truncate(24 * time.Hour)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, 1)
	}
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	local := func(t time.Time) string { return t.Format("20060102T150405") }
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }

	// RFC 6764 discovery, then RFC 5397 / RFC 4791 section 6.2.1.
	resp, _ := c.want("PROPFIND", "/.well-known/caldav", "", http.StatusMovedPermanently)
	if loc := resp.Header.Get("Location"); loc != "/dav/" {
		t.Fatalf("well-known redirects to %q", loc)
	}
	_, out := c.want("PROPFIND", "/dav/", `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "0")
	mustContain(t, "root", out, "<D:current-user-principal><D:href>/dav/principals/default/</D:href>")
	_, out = c.want("PROPFIND", "/dav/principals/default/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-home-set/><D:displayname/><C:calendar-user-address-set/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "0")
	mustContain(t, "principal", out, "<C:calendar-home-set><D:href>/dav/calendars/</D:href>", "<C:calendar-user-address-set/>", "404 Not Found")
	resp, _ = c.want("OPTIONS", "/dav/", "", http.StatusOK)
	if !strings.Contains(resp.Header.Get("DAV"), "calendar-access") {
		t.Fatalf("DAV header %q", resp.Header.Get("DAV"))
	}

	// MKCALENDAR, RFC 4791 section 5.3.1.
	c.want("MKCALENDAR", cal, `<?xml version="1.0"?><C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:displayname>Court `+suffix+`</D:displayname><C:calendar-description>Padel court</C:calendar-description></D:prop></D:set></C:mkcalendar>`, http.StatusCreated)
	defer func() { _ = repos.Calendars.Purge(context.Background(), calName) }()
	c.want("MKCALENDAR", cal, "", http.StatusMethodNotAllowed)
	_, out = c.want("PROPFIND", "/dav/calendars/", `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><D:resourcetype/><D:displayname/><CS:getctag/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "1")
	mustContain(t, "home", out, "<D:href>"+cal+"</D:href>", "<C:calendar/>", "<D:displayname>Court "+suffix+"</D:displayname>")
	// This calendar's own response carries a ctag, though it has no members
	// yet: an empty calendar is still one a client syncs.
	own := out[strings.Index(out, "<D:href>"+cal+"</D:href>"):]
	own = own[:strings.Index(own, "</D:response>")]
	mustContain(t, "empty calendar", own, "<CS:getctag>")

	// PUT an Apple-style object: a zoned weekly series with its VTIMEZONE,
	// under a client-chosen upper-case name. If-None-Match: * is a create.
	obj := cal + "Weekly-Maint.ics"
	event := func(summary string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//macOS 15//EN\r\n" + madrid +
			"BEGIN:VEVENT\r\nUID:maint-" + suffix + "@example.com\r\nDTSTAMP:" + stamp(time.Now()) + "\r\n" +
			"DTSTART;TZID=Europe/Madrid:" + local(at(10)) + "\r\nDTEND;TZID=Europe/Madrid:" + local(at(11)) + "\r\n" +
			"RRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:" + summary + "\r\nTRANSP:OPAQUE\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	resp, _ = c.want("PUT", obj, event("Maintenance"), http.StatusCreated, "Content-Type", "text/calendar", "If-None-Match", "*")
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("PUT returned no ETag")
	}
	c.want("PUT", obj, event("Maintenance"), http.StatusPreconditionFailed, "If-None-Match", "*")

	resp, out = c.want("GET", obj, "", http.StatusOK)
	mustContain(t, "GET", out, "UID:maint-"+suffix+"@example.com", "DTSTAMP:", "RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Maintenance")
	if resp.Header.Get("ETag") != etag || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/calendar") {
		t.Fatalf("GET headers %v", resp.Header)
	}

	// Conditional replace, RFC 4791 section 5.3.4.
	c.want("PUT", obj, event("Resurfacing"), http.StatusPreconditionFailed, "If-Match", `"stale"`)
	resp, _ = c.want("PUT", obj, event("Resurfacing"), http.StatusNoContent, "If-Match", etag)
	if next := resp.Header.Get("ETag"); next == "" || next == etag {
		t.Fatalf("etag after replace: %q (was %q)", next, etag)
	}

	// beacon-dav's own query: VEVENTs in a window, expanded to instances.
	window := func(a, b time.Time) string {
		return `<?xml version="1.0" encoding="utf-8"?><C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
			`<D:prop><D:getetag/><C:calendar-data><C:expand start="` + stamp(a) + `" end="` + stamp(b) + `"/></C:calendar-data></D:prop>` +
			`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="` + stamp(a) + `" end="` + stamp(b) + `"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
	}
	_, out = c.want("REPORT", cal, window(day, day.AddDate(0, 0, 21)), http.StatusMultiStatus, "Depth", "1")
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 3 {
		t.Fatalf("expanded %d instances, want 3:\n%s", n, out)
	}
	mustContain(t, "expand", out, "RECURRENCE-ID:", "SUMMARY:Resurfacing", "<D:href>"+obj+"</D:href>")
	if strings.Contains(out, "RRULE") {
		t.Fatal("expanded instances still carry RRULE")
	}
	_, out = c.want("REPORT", cal, window(day.AddDate(0, 2, 0), day.AddDate(0, 2, 7)), http.StatusMultiStatus, "Depth", "1")
	if strings.Contains(out, "<D:response>") {
		t.Fatalf("a window after the series matched:\n%s", out)
	}
	// Apple looks events up by UID with a text-match prop-filter.
	_, out = c.want("REPORT", cal, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="UID"><C:text-match collation="i;octet">maint-`+suffix+`@example.com</C:text-match></C:prop-filter></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`, http.StatusMultiStatus, "Depth", "1")
	mustContain(t, "UID query", out, "<D:href>"+obj+"</D:href>")
	c.want("REPORT", cal, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="SUMMARY"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`, http.StatusForbidden, "Depth", "1")

	// calendar-multiget, RFC 4791 section 7.9.
	_, out = c.want("REPORT", cal, `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop><D:href>`+obj+`</D:href><D:href>`+cal+`missing.ics</D:href></C:calendar-multiget>`, http.StatusMultiStatus, "Depth", "1")
	mustContain(t, "multiget", out, "BEGIN:VCALENDAR", "<D:href>"+cal+"missing.ics</D:href><D:status>HTTP/1.1 404 Not Found</D:status>")

	// Opening hours as an RFC 7953 object, then free-busy-query, which the
	// engine answers: the series is BUSY, outside 08:00-20:00 Madrid is
	// BUSY-UNAVAILABLE.
	hours := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + madrid +
		"BEGIN:VAVAILABILITY\r\nUID:hours-" + suffix + "\r\nDTSTAMP:" + stamp(time.Now()) + "\r\nDTSTART;TZID=Europe/Madrid:" + local(day) + "\r\n" +
		"BEGIN:AVAILABLE\r\nUID:hours-" + suffix + "-a\r\nDTSTAMP:" + stamp(time.Now()) + "\r\nDTSTART;TZID=Europe/Madrid:" + local(at(8)) + "\r\nDTEND;TZID=Europe/Madrid:" + local(at(20)) + "\r\nRRULE:FREQ=DAILY\r\nEND:AVAILABLE\r\n" +
		"END:VAVAILABILITY\r\nEND:VCALENDAR\r\n"
	c.want("PUT", cal+"hours.ics", hours, http.StatusCreated, "Content-Type", "text/calendar")
	_, out = c.want("GET", cal+"hours.ics", "", http.StatusOK)
	mustContain(t, "availability GET", out, "BEGIN:VAVAILABILITY", "BEGIN:AVAILABLE", "UID:hours-"+suffix)

	madridLoc, _ := time.LoadLocation("Europe/Madrid")
	m := func(h int) string { return stamp(time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, madridLoc)) }
	resp, out = c.want("REPORT", cal, `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="`+m(6)+`" end="`+m(22)+`"/></C:free-busy-query>`, http.StatusOK, "Depth", "1")
	mustContain(t, "free-busy", out, "BEGIN:VFREEBUSY",
		"FREEBUSY;FBTYPE=BUSY:"+m(10)+"/"+m(11),
		"FREEBUSY;FBTYPE=BUSY-UNAVAILABLE:"+m(6)+"/"+m(8),
		"FREEBUSY;FBTYPE=BUSY-UNAVAILABLE:"+m(20)+"/"+m(22))
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/calendar") {
		t.Fatalf("free-busy content type %q", resp.Header.Get("Content-Type"))
	}

	// The whole calendar as one .ics: the export.
	_, out = c.want("GET", cal, "", http.StatusOK)
	mustContain(t, "export", out, "BEGIN:VEVENT", "BEGIN:VAVAILABILITY")
	if strings.Count(out, "BEGIN:VCALENDAR") != 1 {
		t.Fatalf("export is not one VCALENDAR:\n%s", out)
	}

	// PROPPATCH: the name and description are writable, nothing else.
	_, out = c.want("PROPPATCH", cal, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Court One</D:displayname></D:prop></D:set></D:propertyupdate>`, http.StatusMultiStatus)
	mustContain(t, "proppatch", out, "<D:displayname/>", "200 OK")
	_, out = c.want("PROPPATCH", cal, `<D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop><D:displayname>X</D:displayname><A:calendar-color>#FF0000</A:calendar-color></D:prop></D:set></D:propertyupdate>`, http.StatusMultiStatus)
	mustContain(t, "proppatch refused", out, "403 Forbidden", "424 Failed Dependency")
	if got, _ := repos.Calendars.Get(context.Background(), calName); got.GetDisplayName() != "Court One" {
		t.Fatalf("display name %q after a refused PROPPATCH", got.GetDisplayName())
	}

	// Single occurrences edited, RFC 5545 section 3.8.4.4: the first week
	// moved to 12:00, the second cancelled. One object, three VEVENTs.
	uid := "maint-" + suffix + "@example.com"
	override := func(week int, extra string) string {
		orig := at(10).AddDate(0, 0, 7*week)
		return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:" + stamp(time.Now()) + "\r\n" +
			"RECURRENCE-ID;TZID=Europe/Madrid:" + local(orig) + "\r\n" + extra + "END:VEVENT\r\n"
	}
	edited := strings.Replace(event("Resurfacing"), "END:VCALENDAR\r\n",
		override(0, "DTSTART;TZID=Europe/Madrid:"+local(at(12))+"\r\nDTEND;TZID=Europe/Madrid:"+local(at(13))+"\r\nSUMMARY:Moved\r\n")+
			override(1, "DTSTART;TZID=Europe/Madrid:"+local(at(10).AddDate(0, 0, 7))+"\r\nSTATUS:CANCELLED\r\n")+
			"END:VCALENDAR\r\n", 1)
	resp, _ = c.want("PUT", obj, edited, http.StatusNoContent)
	editedTag := resp.Header.Get("ETag")
	resp, out = c.want("GET", obj, "", http.StatusOK)
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 3 || resp.Header.Get("ETag") != editedTag {
		t.Fatalf("GET of an edited series: %d VEVENTs, ETag %q (PUT gave %q)\n%s", n, resp.Header.Get("ETag"), editedTag, out)
	}
	mustContain(t, "edited GET", out, "RRULE:FREQ=WEEKLY;COUNT=3",
		"RECURRENCE-ID;TZID=Europe/Madrid:"+local(at(10)), "SUMMARY:Moved",
		"RECURRENCE-ID;TZID=Europe/Madrid:"+local(at(10).AddDate(0, 0, 7)), "STATUS:CANCELLED")
	// A series and its overrides are one write. Here the series' new summary
	// and the first override's edit are queued ahead of a new override the
	// database refuses (a NUL byte, which PostgreSQL text cannot hold): the
	// PUT fails, and the object is exactly as it was.
	//
	// Hasura DDN is skipped: its engine runs each root field of a mutation
	// request in a transaction of its own, so the Unit's single request is
	// not atomic there, and a failure part way leaves the writes before it.
	if conn.Provider == database.ProviderHasura {
		t.Log("skipping the atomic series write check: DDN does not run one mutation request in one transaction")
	} else {
		broken := strings.Replace(edited, "SUMMARY:Resurfacing", "SUMMARY:Half-written", 1)
		broken = strings.Replace(broken, "SUMMARY:Moved", "SUMMARY:Moved again", 1)
		broken = strings.Replace(broken, "END:VCALENDAR\r\n",
			override(2, "DTSTART;TZID=Europe/Madrid:"+local(at(14).AddDate(0, 0, 14))+"\r\nSUMMARY:Bad\x00byte\r\n")+"END:VCALENDAR\r\n", 1)
		if resp, out := c.do("PUT", obj, broken, "If-Match", editedTag); resp.StatusCode < 400 {
			t.Fatalf("PUT of a series with a refused override: status %d\n%s", resp.StatusCode, out)
		}
		resp, out = c.want("GET", obj, "", http.StatusOK)
		if n := strings.Count(out, "BEGIN:VEVENT"); n != 3 || resp.Header.Get("ETag") != editedTag {
			t.Fatalf("a failed series PUT changed the object: %d VEVENTs, ETag %q (was %q)\n%s", n, resp.Header.Get("ETag"), editedTag, out)
		}
		if strings.Contains(out, "Half-written") || strings.Contains(out, "Moved again") {
			t.Fatalf("a failed series PUT left part of its writes:\n%s", out)
		}
		// The same for a create: nothing of the new object is stored.
		fresh := strings.ReplaceAll(broken, "UID:"+uid, "UID:fresh-"+uid)
		if resp, out := c.do("PUT", cal+"fresh.ics", fresh, "If-None-Match", "*"); resp.StatusCode < 400 {
			t.Fatalf("PUT creating a series with a refused override: status %d\n%s", resp.StatusCode, out)
		}
		c.want("GET", cal+"fresh.ics", "", http.StatusNotFound)
		stray, _, err := repos.Events.List(context.Background(), calName, repox.ListInput{PageSize: 100, Filter: `ical_uid = "fresh-` + uid + `"`, ShowDeleted: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(stray) != 0 {
			t.Fatalf("a failed create left %d events behind", len(stray))
		}
	}
	_, out = c.want("PROPFIND", cal, `<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "1")
	if n := strings.Count(out, "<D:response>"); n != 3 { // the calendar, the series, the hours
		t.Fatalf("overrides listed as objects of their own:\n%s", out)
	}

	// Expanded, the moved instance is at 12:00 and the cancelled one is gone.
	_, out = c.want("REPORT", cal, window(day, day.AddDate(0, 0, 21)), http.StatusMultiStatus, "Depth", "1")
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 2 {
		t.Fatalf("expanded %d instances of the edited series, want 2:\n%s", n, out)
	}
	mustContain(t, "edited expand", out, "SUMMARY:Moved", "DTSTART;TZID=Europe/Madrid:"+local(at(12)),
		"RECURRENCE-ID:"+m(10))
	// And the engine agrees: 10:00 is free, 12:00 busy, the second week free.
	fbWeek := func(week int) string {
		d := day.AddDate(0, 0, 7*week)
		mm := func(h int) string { return stamp(time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, madridLoc)) }
		_, out := c.want("REPORT", cal, `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="`+mm(8)+`" end="`+mm(20)+`"/></C:free-busy-query>`, http.StatusOK, "Depth", "1")
		return out
	}
	out = fbWeek(0)
	mustContain(t, "edited free-busy", out, "FREEBUSY;FBTYPE=BUSY:"+m(12)+"/"+m(13))
	if strings.Contains(out, "FBTYPE=BUSY:"+m(10)) {
		t.Fatalf("the moved instance still blocks 10:00:\n%s", out)
	}
	if out = fbWeek(1); strings.Contains(out, "FBTYPE=BUSY:") {
		t.Fatalf("the cancelled instance still blocks:\n%s", out)
	}

	// Re-PUT the series without overrides: both instances come back.
	c.want("PUT", obj, event("Resurfacing"), http.StatusNoContent, "If-Match", editedTag)
	_, out = c.want("GET", obj, "", http.StatusOK)
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 1 {
		t.Fatalf("overrides survived a PUT without them:\n%s", out)
	}
	_, out = c.want("REPORT", cal, window(day, day.AddDate(0, 0, 21)), http.StatusMultiStatus, "Depth", "1")
	if n := strings.Count(out, "BEGIN:VEVENT"); n != 3 {
		t.Fatalf("expanded %d instances after the overrides were dropped, want 3:\n%s", n, out)
	}
	// Overrides without their series, or carrying their own RRULE, are not
	// one object.
	_, out = c.want("PUT", cal+"orphans.ics", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"+madrid+
		override(0, "DTSTART;TZID=Europe/Madrid:"+local(at(12))+"\r\n")+override(1, "DTSTART;TZID=Europe/Madrid:"+local(at(12))+"\r\n")+
		"END:VCALENDAR\r\n", http.StatusForbidden)
	mustContain(t, "orphans", out, "valid-calendar-object-resource")

	// DELETE takes the overrides with the series.
	resp, _ = c.want("PUT", obj, edited, http.StatusNoContent)
	c.want("DELETE", obj, "", http.StatusNoContent, "If-Match", resp.Header.Get("ETag"))
	left, _, err := repos.Events.List(context.Background(), calName, repox.ListInput{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("%d events live after the series was deleted", len(left))
	}
	if _, err := repos.Events.Undelete(context.Background(), calName+"/events/Weekly-Maint.ics"); err != nil {
		t.Fatal(err)
	}

	// DELETE is soft: gone from CalDAV and the engine, recoverable over gRPC.
	_, cur := c.want("PROPFIND", obj, `<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "0")
	tag := cur[strings.Index(cur, "<D:getetag>")+len("<D:getetag>") : strings.Index(cur, "</D:getetag>")]
	tag = strings.ReplaceAll(tag, "&#34;", `"`)
	c.want("DELETE", obj, "", http.StatusPreconditionFailed, "If-Match", `"stale"`)
	c.want("DELETE", obj, "", http.StatusNoContent, "If-Match", tag)
	c.want("GET", obj, "", http.StatusNotFound)
	if _, err := repos.Events.Undelete(context.Background(), calName+"/events/Weekly-Maint.ics"); err != nil {
		t.Fatalf("undelete over the API: %v", err)
	}
	c.want("GET", obj, "", http.StatusOK)

	caldavSplit(t, c, conn, repos, cal, calName, suffix, day)

	c.want("DELETE", cal, "", http.StatusNoContent)
	c.want("PROPFIND", cal, "", http.StatusNotFound, "Depth", "0")
	c.want("PUT", cal+"late.ics", event("Late"), http.StatusConflict)
	all, _, _ := repos.Events.List(context.Background(), calName, repox.ListInput{PageSize: 100, ShowDeleted: true})
	for _, e := range all {
		_ = repos.Events.Purge(context.Background(), e.GetName())
	}
	_ = repos.Availabilities.Purge(context.Background(), calName+"/availabilities/hours.ics")
}

// caldavSplit is RECURRENCE-ID;RANGE=THISANDFUTURE, RFC 5545 section 3.2.13:
// a weekly 09:00 series moves to 15:00 from its third week on. The server
// splits it -- the original ends after two weeks, a new object carries the
// rest -- and answers without an ETag, RFC 4791 section 5.3.4, since what it
// stored is not what was sent.
func caldavSplit(t *testing.T, c davClient, conn *database.Connection, repos directory.Repositories, cal, calName, suffix string, day time.Time) {
	stamp := func(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
	local := func(t time.Time) string { return t.Format("20060102T150405") }
	madridLoc, _ := time.LoadLocation("Europe/Madrid")
	wall := func(week, h int) time.Time {
		d := day.AddDate(0, 0, 7*week)
		return time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, madridLoc)
	}
	// local() of a UTC-midnight day plus hours is that Madrid wall clock.
	at := func(week, h int) time.Time { return day.AddDate(0, 0, 7*week).Add(time.Duration(h) * time.Hour) }

	obj := cal + "Split.ics"
	uid := "split-" + suffix + "@example.com"
	body := func(components ...string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + madrid +
			"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:" + stamp(time.Now()) + "\r\n" +
			"DTSTART;TZID=Europe/Madrid:" + local(at(0, 8)) + "\r\nDTEND;TZID=Europe/Madrid:" + local(at(0, 9)) + "\r\n" +
			"RRULE:FREQ=WEEKLY;COUNT=5\r\nSUMMARY:Lessons\r\nEND:VEVENT\r\n" + strings.Join(components, "") + "END:VCALENDAR\r\n"
	}
	override := func(week int, rng string, start int, summary string) string {
		return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:" + stamp(time.Now()) + "\r\n" +
			"RECURRENCE-ID" + rng + ";TZID=Europe/Madrid:" + local(at(week, 8)) + "\r\n" +
			"DTSTART;TZID=Europe/Madrid:" + local(at(week, start)) + "\r\nDTEND;TZID=Europe/Madrid:" + local(at(week, start+1)) + "\r\n" +
			"SUMMARY:" + summary + "\r\nEND:VEVENT\r\n"
	}
	// Week 1 moved to 13:00 stays with the original; week 3, a different
	// summary at the usual time, follows the split to the new series.
	one, three := override(1, "", 13, "Moved one"), override(3, "", 8, "Guest coach")
	c.want("PUT", obj, body(one, three), http.StatusCreated, "If-None-Match", "*")
	split := override(2, ";RANGE=THISANDFUTURE", 15, "Evening lessons")

	// On GORM the split is one transaction: a tail the database refuses (a
	// NUL byte) leaves the object exactly as it was. DDN runs each root field
	// of a mutation in a transaction of its own, so there a failure part way
	// keeps the writes before it; the check is skipped.
	if conn.Provider == database.ProviderHasura {
		t.Log("skipping the atomic split check: DDN does not run one mutation request in one transaction")
	} else {
		resp, _ := c.want("GET", obj, "", http.StatusOK)
		before := resp.Header.Get("ETag")
		if resp, out := c.do("PUT", obj, body(one, three, override(2, ";RANGE=THISANDFUTURE", 15, "Bad\x00byte"))); resp.StatusCode < 400 {
			t.Fatalf("PUT of a split with a refused tail: status %d\n%s", resp.StatusCode, out)
		}
		resp, out := c.want("GET", obj, "", http.StatusOK)
		if resp.Header.Get("ETag") != before || !strings.Contains(out, "COUNT=5") {
			t.Fatalf("a failed split changed the object: ETag %q (was %q)\n%s", resp.Header.Get("ETag"), before, out)
		}
	}

	resp, _ := c.want("PUT", obj, body(one, three, split), http.StatusNoContent)
	if tag := resp.Header.Get("ETag"); tag != "" {
		t.Fatalf("a split PUT returned ETag %q; RFC 4791 section 5.3.4 says the client must re-read", tag)
	}
	_, out := c.want("GET", obj, "", http.StatusOK)
	mustContain(t, "split original", out, "RRULE:FREQ=WEEKLY;COUNT=2", "SUMMARY:Lessons",
		"RECURRENCE-ID;TZID=Europe/Madrid:"+local(at(1, 8)), "SUMMARY:Moved one")
	if strings.Contains(out, "RANGE=") || strings.Contains(out, "Guest coach") || strings.Contains(out, "Evening") {
		t.Fatalf("the original kept what the split moved:\n%s", out)
	}

	tailID := "Split_R" + stamp(wall(2, 8)) + ".ics"
	tailUID := "split-" + suffix + "_R" + stamp(wall(2, 8)) + "@example.com"
	_, out = c.want("GET", cal+tailID, "", http.StatusOK)
	mustContain(t, "split tail", out, "UID:"+tailUID, "RRULE:FREQ=WEEKLY;COUNT=3", "SUMMARY:Evening lessons",
		"DTSTART;TZID=Europe/Madrid:"+local(at(2, 15)),
		"RECURRENCE-ID;TZID=Europe/Madrid:"+local(at(3, 15)), "SUMMARY:Guest coach")
	if strings.Contains(out, "RANGE=") {
		t.Fatalf("the tail carries a RANGE:\n%s", out)
	}
	_, out = c.want("PROPFIND", cal, `<D:propfind xmlns:D="DAV:"><D:prop><D:getetag/></D:prop></D:propfind>`, http.StatusMultiStatus, "Depth", "1")
	mustContain(t, "PROPFIND after split", out, "<D:href>"+obj+"</D:href>", "<D:href>"+cal+tailID+"</D:href>")
	_, out = c.want("REPORT", cal, `<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="UID"><C:text-match collation="i;octet">`+tailUID+`</C:text-match></C:prop-filter></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`, http.StatusMultiStatus, "Depth", "1")
	mustContain(t, "UID query for the tail", out, "<D:href>"+cal+tailID+"</D:href>")

	// The engine blocks the shifted times: 15:00 from week 2 on (week 3's
	// override, unmoved, moved with its instance), 08:00 before.
	freeBusy := func(week int) string {
		_, out := c.want("REPORT", cal, `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="`+stamp(wall(week, 8))+`" end="`+stamp(wall(week, 20))+`"/></C:free-busy-query>`, http.StatusOK, "Depth", "1")
		return out
	}
	for week, busy := range map[int]int{0: 8, 1: 13, 2: 15, 3: 15, 4: 15} {
		out := freeBusy(week)
		mustContain(t, fmt.Sprintf("free-busy week %d", week), out, "FREEBUSY;FBTYPE=BUSY:"+stamp(wall(week, busy))+"/"+stamp(wall(week, busy+1)))
		if week > 1 && strings.Contains(out, "FBTYPE=BUSY:"+stamp(wall(week, 8))) {
			t.Fatalf("week %d still busy at 08:00 after the split:\n%s", week, out)
		}
	}
	if out := freeBusy(5); strings.Contains(out, "FBTYPE=BUSY:"+stamp(wall(5, 15))) {
		t.Fatalf("the split added a sixth instance:\n%s", out)
	}

	// A retry of the same PUT finds the tail it made and updates it: still
	// two objects, one copy of each override.
	c.want("PUT", obj, body(one, three, split), http.StatusNoContent)
	all, _, err := repos.Events.List(context.Background(), calName, repox.ListInput{PageSize: 100, Filter: `ical_uid = "` + tailUID + `"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 { // the tail and the re-homed override
		t.Fatalf("after a retried split the tail's UID has %d events, want 2", len(all))
	}
}

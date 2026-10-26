// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

// feed.go publishes calendars as read-only iCalendar feeds a calendar app
// subscribes to by URL -- Google Calendar's "From URL", which speaks no
// CalDAV and sends no credentials, and fetches from Google's servers:
//
//	GET /feeds/calendars/{calendar}.ics?token=...
//
// The body is CalDAV's GET of the collection -- the whole calendar as one
// VCALENDAR -- so a feed shows exactly what CalDAV does. Unlike CalDAV it
// is safe to expose: only GET and HEAD reach it, and only with the
// calendar's token, an HMAC of its name under [feeds] secret. No token is
// stored; a new secret revokes every link at once. It is served whether or
// not the unauthenticated CalDAV tree is mounted.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

// FeedPrefix is where the feeds are mounted.
const FeedPrefix = "/feeds/calendars/"

// MinFeedSecret is the shortest [feeds] secret accepted, in bytes.
const MinFeedSecret = 32

// Feed serves calendars read-only to whoever holds their token.
type Feed struct {
	dav    http.Handler // the CalDAV handler the GET is answered by
	secret []byte
}

// NewFeed returns a Feed answered by dav, a CalDAV handler mounted at
// Prefix, signing with secret.
func NewFeed(dav http.Handler, secret []byte) (*Feed, error) {
	if len(secret) < MinFeedSecret {
		return nil, fmt.Errorf("caldav: feeds secret is %d bytes; it needs %d or more", len(secret), MinFeedSecret)
	}
	return &Feed{dav: dav, secret: secret}, nil
}

// Token is the token of calendar, a resource name calendars/{calendar}:
// 128 bits of HMAC-SHA256, URL-safe base64. Anyone holding the secret can
// make it -- a service that hands out links, say -- with the same recipe.
func (f *Feed) Token(calendar string) string {
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(calendar))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// URL is the link to subscribe to calendar, under base, the public address
// of the HTTP port.
func (f *Feed) URL(base, calendar string) string {
	id := strings.TrimPrefix(calendar, "calendars/")
	return strings.TrimRight(base, "/") + FeedPrefix + url.PathEscape(id) + ".ics?token=" + f.Token(calendar)
}

// Mount registers the feed route on the REST gateway's mux, beside the
// gateway's own paths.
func (f *Feed) Mount(mux *runtime.ServeMux) error {
	serve := func(w http.ResponseWriter, r *http.Request, p map[string]string) { f.serve(w, r, p["file"]) }
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if err := mux.HandlePath(m, FeedPrefix+"{file}", serve); err != nil {
			return fmt.Errorf("caldav: mount feeds %s: %w", m, err)
		}
	}
	return nil
}

// serve answers one feed request for file, "{calendar}.ics". A wrong or
// missing token is a 404, as is a calendar that does not exist, so a
// guessed URL learns nothing.
func (f *Feed) serve(w http.ResponseWriter, r *http.Request, file string) {
	id, ok := strings.CutSuffix(file, ".ics")
	token := r.URL.Query().Get("token")
	if !ok || id == "" || strings.Contains(id, "/") ||
		!hmac.Equal([]byte(f.Token("calendars/"+id)), []byte(token)) {
		http.NotFound(w, r)
		return
	}
	// Answer as CalDAV's GET of the collection, with nothing of the
	// caller's request but its method and conditional headers.
	dav := r.Clone(r.Context())
	dav.URL = &url.URL{
		Path:    Prefix + "/calendars/" + id + "/",
		RawPath: Prefix + "/calendars/" + url.PathEscape(id) + "/",
	}
	dav.Header = http.Header{}
	for _, h := range []string{"If-None-Match", "Accept-Encoding"} {
		if v := r.Header.Get(h); v != "" {
			dav.Header.Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	f.dav.ServeHTTP(&inline{ResponseWriter: w}, dav)
}

// inline serves the calendar for display rather than as a download: CalDAV
// marks a collection's GET as an attachment, which a browser would save.
type inline struct{ http.ResponseWriter }

func (i *inline) WriteHeader(code int) {
	i.Header().Del("Content-Disposition")
	i.ResponseWriter.WriteHeader(code)
}

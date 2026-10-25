// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package caldav serves freebusy's calendars over CalDAV, RFC 4791, so an
// operator can block out a resource from any calendar app and the engine sees
// it at once.
//
// The protocol is runtime-go's rfc/caldav; this package is its store over the
// generated repositories the gRPC API uses, with free/busy and expansion
// answered by the engine, so what a client is shown is what a hold would see.
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

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/the-protobuf-project/runtime-go/rfc/caldav"
)

// Prefix is where the CalDAV tree is mounted.
const Prefix = caldav.DefaultPrefix

// Handler answers CalDAV requests over the RFC repositories.
type Handler struct{ *caldav.Handler }

// New returns a Handler over repos, answering free-busy-query from fb.
func New(repos directory.Repositories, fb directory.FreeBusyQuerier) *Handler {
	return &Handler{caldav.New(&store{repos: repos, fb: fb},
		caldav.WithName("freebusy"),
		caldav.WithProductID("-//shokki-engineering//freebusy CalDAV//EN"),
		caldav.WithErrorStatus(statusOf),
	)}
}

// Mount registers the handler on the REST gateway's mux, so CalDAV shares
// the HTTP port and the gateway keeps every other path.
func (h *Handler) Mount(mux *runtime.ServeMux) error {
	serve := func(w http.ResponseWriter, r *http.Request, _ map[string]string) { h.ServeHTTP(w, r) }
	for _, m := range caldav.Methods() {
		for _, p := range []string{Prefix, Prefix + "/{path=**}"} {
			if err := mux.HandlePath(m, p, serve); err != nil {
				return fmt.Errorf("caldav: mount %s %s: %w", m, p, err)
			}
		}
	}
	// RFC 6764 section 5: clients probe the well-known path first.
	wk := h.WellKnown()
	wellKnown := func(w http.ResponseWriter, r *http.Request, _ map[string]string) { wk.ServeHTTP(w, r) }
	for _, m := range []string{http.MethodGet, http.MethodHead, "PROPFIND", http.MethodOptions} {
		if err := mux.HandlePath(m, "/.well-known/caldav", wellKnown); err != nil {
			return fmt.Errorf("caldav: mount well-known: %w", err)
		}
	}
	return nil
}

// statusOf maps the repositories' sentinels to the statuses they mean.
func statusOf(err error) int {
	switch {
	case errors.Is(err, repox.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, repox.ErrConflict), errors.Is(err, repox.ErrAlreadyExists):
		return http.StatusPreconditionFailed
	case errors.Is(err, repox.ErrInvalidArgument):
		return http.StatusBadRequest
	case errors.Is(err, servicex.ErrFailedPrecondition), errors.Is(err, repox.ErrForeignKey),
		errors.Is(err, repox.ErrReferenceNotFound), errors.Is(err, repox.ErrReferenced):
		return http.StatusConflict
	case errors.Is(err, context.Canceled):
		return 499
	}
	return 0
}

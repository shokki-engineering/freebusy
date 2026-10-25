// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// assembly.go builds freebusy's services on one database connection, and the
// unary interceptor chain every surface runs them behind: the same stores,
// repositories, servers and checks for the server and the e2e suites. It
// reads no configuration; its callers pass what it needs.

import (
	"context"
	"time"

	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/search"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
	"github.com/shokki-engineering/freebusy/internal/service/reservation"
	"github.com/shokki-engineering/freebusy/internal/service/webhook"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/grpc/hints"
	"google.golang.org/grpc"
)

// Settings tune the services. The zero value of each field is its default.
type Settings struct {
	// HoldTTL is how long a new hold lasts; 0 is allocation.DefaultHoldTTL.
	HoldTTL time.Duration
	// Retention keeps soft-deleted inventory restorable this long; 0 is 30
	// days.
	Retention time.Duration
	// Webhooks tune webhook delivery. Its Meter, when nil, is the process
	// pipeline's.
	Webhooks webhook.Options
	// MaxEvaluations bounds the availability evaluations running at once
	// ([engine] max_evaluations); 0 is the number of CPUs.
	MaxEvaluations int
	// AdmissionWait is how long an evaluation waits for a turn before
	// ENGINE_BUSY ([engine] admission_wait_ms); 0 is
	// allocation.DefaultAdmissionWait.
	AdmissionWait time.Duration
	// HorizonDays is how far ahead the availability read model reaches
	// ([search] horizon_days); 0 is allocation.DefaultHorizonDays.
	HorizonDays int
	// OpenSearch, when set, receives every committed write to the read
	// model and serves SearchAvailability, falling back to Postgres
	// ([search] provider = "opensearch").
	OpenSearch *search.OpenSearch
}

// Services are freebusy's services, assembled on one connection.
type Services struct {
	Conn  *database.Connection
	Repos directory.Repositories

	// Allocation serves AllocationService and HoldService, and answers the
	// Calendars service's QueryFreeBusy.
	Allocation     *allocation.Server
	Reservations   *reservation.Server
	Webhooks       *webhook.Server
	Organizations  *directory.OrganizationServer
	Units          *directory.OrganizationalUnitServer
	Resources      *directory.ResourceServer
	Calendars      *directory.CalendarServer
	Availabilities *directory.AvailabilityServer
	Events         *directory.EventServer

	// ReadModel maintains the availability read model (allocation.v1.
	// ResourceDay): its writer runs inside every write to its sources, and
	// it rebuilds and extends itself.
	ReadModel *allocation.DayProjection

	// Sites decides whether a site or an organization a call names is live.
	Sites *directory.Sites
	// Purger ends soft deletes once their retention passes.
	Purger *directory.Purger

	// search is the read model's OpenSearch copy (Settings.OpenSearch).
	search *search.OpenSearch
}

// New assembles every service on conn.
func New(conn *database.Connection, s Settings) *Services {
	if s.Retention <= 0 {
		s.Retention = 30 * 24 * time.Hour
	}
	// The read model's writer must be on the connection before anything
	// builds a repository over it: each takes the projections it carries.
	readModel := allocation.NewDayProjection(readerOn, s.HorizonDays)
	if conn.Projections == nil {
		conn.Projections = repox.NewProjections(readModel.Projector())
		conn.Projections.OnError = func(ctx context.Context, projection string, err error) {
			// A Hasura write landed but its projection did not (it is sent
			// in a request of its own): searches may miss or re-check what
			// it changed until the next write or a rebuild.
			telemetry.Warn("Read model not updated after a write", "projection", projection, "error", err)
		}
	}
	opts := []allocation.Option{allocation.WithMeter(telemetry.Meter()), allocation.WithReadModel(readModel, conn.SearchConn())}
	if s.OpenSearch != nil {
		conn.Projections.Feed = s.OpenSearch.Feed(func(ctx context.Context, err error) {
			// The write landed; the copy missed it until the next write to
			// those rows or a reindex ([search.opensearch] reindex_on_start).
			telemetry.Warn("OpenSearch did not take a read-model change", "error", err)
		})
		opts = append(opts, allocation.WithSearcher(s.OpenSearch.Searcher(allocation.PostgresDays(conn.SearchConn()), telemetry.Meter())))
	}
	if s.HoldTTL > 0 {
		opts = append(opts, allocation.WithHoldTTL(s.HoldTTL))
	}
	opts = append(opts, allocation.WithAdmission(s.MaxEvaluations, s.AdmissionWait))
	if s.Webhooks.Meter == nil {
		s.Webhooks.Meter = telemetry.Meter()
	}
	engine := allocation.NewServer(allocationStore(conn), opts...)
	repos := directory.NewRepositories(conn.Repox(), s.Retention)
	return &Services{
		Conn:           conn,
		Repos:          repos,
		Allocation:     engine,
		ReadModel:      readModel,
		Reservations:   reservation.NewServer(reservationStore(conn), engine),
		Webhooks:       webhook.NewServer(webhook.NewRepositories(conn.Repox()), s.Webhooks),
		Organizations:  directory.NewOrganizationServer(repos),
		Units:          directory.NewOrganizationalUnitServer(repos),
		Resources:      directory.NewResourceServer(repos),
		Calendars:      directory.NewCalendarServer(repos, engine),
		Availabilities: directory.NewAvailabilityServer(repos),
		Events:         directory.NewEventServer(repos),
		Sites:          directory.NewSites(repos),
		Purger:         directory.NewPurger(repos, s.Retention, telemetry.Meter()),
		search:         s.OpenSearch,
	}
}

// Interceptors are the unary interceptors every call to the services passes
// through after request validation (protovalidate, which runtime-go's
// grpc.WithValidation and grpc.NewValidationInterceptor put first): hints
// rewrites the generic errors anything after it returns into ones naming what
// to do; sites refuses a call naming a site or organization that is missing
// or deleted; then `request_id` is honoured on every RPC that declares it --
// so a request that was never going to be served cannot claim an idempotency
// key. The server and the e2e harness run this one list.
func (s *Services) Interceptors() []grpc.UnaryServerInterceptor {
	return []grpc.UnaryServerInterceptor{
		hints.New(hints.Options{Product: "freebusy"}).Interceptor(),
		sitesInterceptor(s.Sites),
		idempotencyInterceptor(s.Conn),
	}
}

// Background selects the background tasks Start runs.
type Background struct {
	// Converge runs the hold sweeper, the reservation reconciler and the
	// read model's daily horizon extension, after priming the read model as
	// RebuildOnStart and ReindexOnStart say.
	Converge bool
	// RebuildOnStart is when the read model is rebuilt before the horizon
	// job first runs: config.OnStartEmpty (also "") when it has no rows,
	// config.OnStartAlways, or config.OnStartNever.
	RebuildOnStart string
	// ReindexOnStart is when the OpenSearch copy, if there is one, is
	// reloaded from the read model next: config.OnStartEmpty (also "") when
	// it has no documents and the table has rows, unless the read model was
	// just rebuilt (the indexer follows that); config.OnStartAlways; or
	// config.OnStartNever.
	ReindexOnStart string
	// Interval is how often the hold sweeper, the reservation reconciler and
	// the purger run; 0 is each one's own default.
	Interval time.Duration
	// Purge runs the purger, which ends soft deletes past their retention.
	Purge bool
	// Webhooks runs webhook delivery every WebhookInterval (0 is 5 seconds).
	Webhooks        bool
	WebhookInterval time.Duration
	// PoolMonitor publishes the connection pool's health to the process's
	// telemetry pipeline.
	PoolMonitor bool
	// Report receives each failure of a task, named by task; nil drops them.
	Report func(task string, err error)
}

// Start launches the background tasks b selects, tied to ctx: the hold
// sweeper, which converges lapsed holds' stored state; the reservation
// reconciler, which finishes reservations left mid-transition; webhook
// delivery, the purger and the pool monitor. The goroutines exit when ctx is
// cancelled.
func (s *Services) Start(ctx context.Context, b Background) {
	report := func(task string) func(error) {
		return func(err error) {
			if b.Report != nil {
				b.Report(task, err)
			}
		}
	}
	if b.Converge {
		s.Reservations.StartReconciler(ctx, b.Interval, report("reservation reconciler"))
		s.Allocation.StartHoldSweeper(ctx, b.Interval, report("allocation hold sweeper"))
		go func() {
			s.primer().run(ctx, b.RebuildOnStart, b.ReindexOnStart, report("read model startup"))
			s.ReadModel.StartHorizon(ctx, s.Conn.SearchConn(), report("read model horizon"))
		}()
	}
	if b.Webhooks {
		interval := b.WebhookInterval
		if interval <= 0 {
			interval = 5 * time.Second
		}
		s.Webhooks.Start(ctx, interval, report("webhook delivery"))
	}
	if b.Purge {
		s.Purger.Start(ctx, b.Interval, report("soft-delete purger"))
	}
	if b.PoolMonitor {
		database.StartPoolMonitor(ctx, s.Conn, 0)
	}
}

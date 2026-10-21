// Package graphqlx is the Hasura-side counterpart to gormx: the Telemetry
// seam a Hasura-backed repository observes through, so
// freebusy_orm_store_ops_total / freebusy_orm_store_duration_ms carry Hasura
// traffic the same way they already carry GORM's (see
// internal/database/gorm/gormx and internal/database/gorm/ormtelemetry).
//
// There is no gorm-style query plugin to hook into on this side — the
// generated GraphQL client (freebusyql) has no callback system — so
// instrumentation happens one layer up, at the hand-written domain
// repositories (see the per-domain "instrumented" wrappers in
// internal/service/*/db/telemetry.go), which call Wrap around each
// repository method.
package graphqlx

import (
	"context"
	"time"

	"github.com/shokki-engineering/freebusy/shared"
	"github.com/the-protobuf-project/opentelementry/opentelementry-go"
)

// Telemetry receives a Hasura-backed repository's spans and per-operation
// metrics. A nil store field is a no-op (see OrNop).
type Telemetry interface {
	// Span wraps one repository operation in a trace span.
	Span(ctx context.Context, name string, fn func(context.Context) error) error
	// RecordOp records one completed operation: an ops counter + duration
	// histogram attributed by table, op, and status, plus error logging.
	RecordOp(ctx context.Context, table, op string, d time.Duration, err error)
}

// NopTelemetry observes nothing.
type NopTelemetry struct{}

// Span runs fn without tracing.
func (NopTelemetry) Span(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

// RecordOp discards the measurement.
func (NopTelemetry) RecordOp(context.Context, string, string, time.Duration, error) {}

// OrNop returns t, or NopTelemetry when t is nil.
func OrNop(t Telemetry) Telemetry {
	if t == nil {
		return NopTelemetry{}
	}
	return t
}

// OpMetric is the per-operation measurement Wrap records. It carries the same
// metric names as gormx.OpMetric on purpose: one freebusy_orm_store_ops_total
// / freebusy_orm_store_duration_ms series serves both providers, since a
// given deployment only runs one of them at a time (config's
// database.provider). The provider attribute added at record time lets a
// dashboard split them out when it matters.
type OpMetric struct {
	Ops        int64   `opentelementry:"metric:counter:orm.store.ops"`
	DurationMS float64 `opentelementry:"metric:histogram:orm.store.duration_ms"`
}

// New adapts o into the Telemetry a Hasura-backed repository observes
// through. A nil o is a no-op.
func New(o *opentelementry.Opentelementry) Telemetry {
	if o == nil {
		return NopTelemetry{}
	}
	return adapter{o: o}
}

type adapter struct {
	o *opentelementry.Opentelementry
}

// Span wraps fn in a trace span, nested under the caller's active span (the
// RPC handler's, ordinarily) via the propagated context.
func (a adapter) Span(ctx context.Context, name string, fn func(context.Context) error) error {
	return a.o.Tracing.Trace(ctx, name, nil, func(ctx context.Context, _ *opentelementry.Span) error {
		return fn(ctx)
	})
}

// RecordOp records the ops counter + duration histogram, attributed by
// table, op, status, and provider="hasura", and logs a trace-correlated
// error on failure.
func (a adapter) RecordOp(ctx context.Context, table, op string, d time.Duration, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	_ = a.o.Metrics.Record(&OpMetric{Ops: 1, DurationMS: float64(d.Microseconds()) / 1000},
		opentelementry.WithAttributes(
			opentelementry.StringAttribute("table", table),
			opentelementry.StringAttribute("op", op),
			opentelementry.StringAttribute("status", status),
			opentelementry.StringAttribute("provider", "hasura"),
		))
	if err != nil {
		_ = a.o.Logger.WithContext(ctx).Error("hasura operation failed", map[string]any{
			"table": table, "op": op, "error": err.Error(),
		})
	}
}

// Wrap runs fn as one Hasura repository operation: a trace span named
// "hasura.<table>.<op>", followed by the ops/duration metric attributed by
// table, op, and outcome. Domain repositories call this once per interface
// method (see internal/service/*/db/telemetry.go) rather than re-deriving
// the timing/span boilerplate at each call site.
func Wrap(ctx context.Context, t Telemetry, table, op string, fn func(context.Context) error) error {
	start := time.Now()
	err := t.Span(ctx, "hasura."+table+"."+op, fn)
	t.RecordOp(ctx, table, op, time.Since(start), err)
	return err
}

// Default returns the process-wide Telemetry built over shared.Telemetry —
// the singleton every generated GORM store already observes through.
func Default() Telemetry {
	return New(shared.Telemetry)
}

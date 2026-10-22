// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"

	"github.com/the-protobuf-project/runtime-go/telemetry"
)

// sweepMetrics is what the hold sweeper reports: each run and how it ended,
// and how many lapsed holds it marked EXPIRED. A sweeper that keeps failing
// leaves lapsed holds ACTIVE in list views; the error outcome is what alerts.
type sweepMetrics struct {
	runs    telemetry.Counter
	expired telemetry.Counter
}

func newSweepMetrics(m telemetry.Meter) sweepMetrics {
	if m == nil {
		m = telemetry.NoopMeter
	}
	return sweepMetrics{
		runs: m.Counter("freebusy.hold.sweeps", telemetry.WithUnit("1"),
			telemetry.WithDescription("Hold sweeper runs, by outcome (ok, error).")),
		expired: m.Counter("freebusy.hold.expired", telemetry.WithUnit("1"),
			telemetry.WithDescription("Lapsed holds the sweeper marked EXPIRED.")),
	}
}

func (m sweepMetrics) record(ctx context.Context, expired int64, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	m.runs.Add(ctx, 1, telemetry.Labels{"outcome": outcome})
	if expired > 0 {
		m.expired.Add(ctx, float64(expired), telemetry.Labels{})
	}
}

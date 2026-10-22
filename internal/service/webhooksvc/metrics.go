// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"context"
	"time"

	"github.com/the-protobuf-project/runtime-go/telemetry"
)

// deliveryMetrics is what delivery reports: every attempt by how it ended,
// and for each delivery that succeeded, how long after it was recorded --
// close to the change itself -- the endpoint accepted it.
type deliveryMetrics struct {
	attempts telemetry.Counter
	lag      telemetry.Histogram
}

func newDeliveryMetrics(m telemetry.Meter) deliveryMetrics {
	if m == nil {
		m = telemetry.NoopMeter
	}
	return deliveryMetrics{
		attempts: m.Counter("freebusy.webhook.attempts", telemetry.WithUnit("1"),
			telemetry.WithDescription("Webhook delivery attempts, by outcome: succeeded, retrying (failed, will retry) or failed (gave up).")),
		lag: m.Histogram("freebusy.webhook.delivery_lag", telemetry.WithUnit("s"),
			telemetry.WithDescription("From a delivery being recorded to its endpoint accepting it."),
			telemetry.WithBuckets(0.5, 1, 2, 5, 10, 30, 60, 300, 1800, 3600, 21600, 86400)),
	}
}

func (m deliveryMetrics) attempt(ctx context.Context, outcome string) {
	m.attempts.Add(ctx, 1, telemetry.Labels{"outcome": outcome})
}

func (m deliveryMetrics) delivered(ctx context.Context, lag time.Duration) {
	m.lag.Record(ctx, lag.Seconds(), telemetry.Labels{})
}

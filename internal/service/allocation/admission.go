// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package allocation

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/the-protobuf-project/runtime-go/grpc/problem"
	"github.com/the-protobuf-project/runtime-go/telemetry"
)

// DefaultAdmissionWait is how long an evaluation waits for a free slot before
// it is turned away.
const DefaultAdmissionWait = 250 * time.Millisecond

// admission bounds the availability evaluations running at once -- search,
// compute, check, and a hold's choice of resources. They are CPU-bound, so
// past the cores there are, more of them in flight only makes every one
// slower: measured without a bound, search latency went from 3 s to 22 s
// past the knee while throughput stayed flat. A caller turned away quickly
// can retry or go elsewhere; one queued behind twenty others cannot.
type admission struct {
	slots    chan struct{}
	wait     time.Duration
	rejected telemetry.Counter
}

func newAdmission(limit int, wait time.Duration, m telemetry.Meter) *admission {
	if limit <= 0 {
		limit = runtime.GOMAXPROCS(0)
	}
	if wait <= 0 {
		wait = DefaultAdmissionWait
	}
	if m == nil {
		m = telemetry.NoopMeter
	}
	return &admission{
		slots: make(chan struct{}, limit),
		wait:  wait,
		rejected: m.Counter("freebusy.engine.rejected", telemetry.WithUnit("1"),
			telemetry.WithDescription("Availability evaluations turned away by admission control (ENGINE_BUSY), by method.")),
	}
}

// acquire takes a slot, waiting up to the admission wait, and returns the
// function that gives it back.
func (a *admission) acquire(ctx context.Context, method string) (func(), error) {
	release := func() { <-a.slots }
	select {
	case a.slots <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(a.wait)
	defer t.Stop()
	select {
	case a.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
	}
	a.rejected.Add(ctx, 1, telemetry.Labels{"method": method})
	return nil, &problem.Problem{
		Kind:   allocationpbv1svc.ErrEngineBusy,
		Reason: allocationpbv1svc.ReasonEngineBusy,
		What: fmt.Sprintf("%s was turned away: %d availability evaluations are already running and none finished within %s",
			method, cap(a.slots), a.wait),
		Fix: "Retry after a short, randomized backoff; narrow the request (fewer sites, a resource type, a smaller page) to make it cheaper",
	}
}

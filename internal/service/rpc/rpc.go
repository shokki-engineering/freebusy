// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rpc is the boundary between freebusy's gRPC handlers and the
// platform runtime: Traced hands each handler body to the shared runtime-go
// Observer (spans, rpc.requests/rpc.errors counters, outcome logs, all emitted
// through freebusy's telemetry identity), and ToStatusErr renders an error as
// a google.rpc.Status (AIP-193) with the translator protoc-gen-service
// generates from the protos' error reasons. Nothing here names a reason.
package rpc

import (
	"context"

	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/allocation/v1/allocationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/reservation/v1/reservationpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/shared/v1/sharedpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/freebusy/webhook/v1/webhookpbv1svc"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"github.com/shokki-engineering/freebusy/internal/telemetry"
	"github.com/the-protobuf-project/runtime-go/grpc"
)

// Traced runs fn inside a span named "<service>/<method>" with request/error
// counters and outcome logging, through the process's telemetry pipeline (see
// grpc.Observer.Traced). Without one (telemetry.Telemetry nil, as in a library
// caller that passed none) it just runs fn.
func Traced(ctx context.Context, service, method string, fn func(context.Context) error) error {
	t := telemetry.Telemetry
	if t == nil {
		return fn(ctx)
	}
	return grpc.NewObserver(t).Traced(ctx, service, method, fn)
}

// Translator is freebusy's one error vocabulary: the reasons of every
// package's ErrorReason enum and their sentinels, the standard methods'
// (servicex), and the store's reference errors, in the freebusy.dev domain.
// The generated servers and the hand-written services share it.
var Translator = sharedpbv1svc.NewTranslator(append(append(allocationpbv1svc.Kinds(),
	reservationpbv1svc.Kinds()...), webhookpbv1svc.Kinds()...)...).
	Declare(allocationpbv1svc.Reasons()...).
	Declare(reservationpbv1svc.Reasons()...).
	Declare(webhookpbv1svc.Reasons()...)

// ToStatusErr renders err as a status a developer can act on (see
// problem.Translator.ToStatus).
func ToStatusErr(err error) error { return Translator.ToStatus(err) }

// ServerOptions are every generated server's options: the shared translator
// and freebusy's tracing.
func ServerOptions() []servicex.Option {
	return []servicex.Option{servicex.WithTranslator(Translator), servicex.WithTracer(Traced)}
}

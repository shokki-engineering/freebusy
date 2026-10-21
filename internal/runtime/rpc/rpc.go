// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rpc is the boundary between freebusy's gRPC handlers and the
// platform runtime: Traced hands each handler body to the shared runtime-go
// Observer (spans, rpc.requests/rpc.errors counters, outcome logs, all emitted
// through freebusy's telemetry identity), and ToStatusErr maps freebusy's
// repository sentinel errors onto gRPC status codes — the one piece that is
// domain-specific and stays here.
package rpc

import (
	"context"
	"errors"

	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/shared"
	"github.com/the-protobuf-project/runtime-go/grpc"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// observer wraps handler bodies with tracing, metrics, and outcome logging;
// built once for the process against freebusy's telemetry client.
var observer = grpc.NewObserver(shared.Telemetry)

// Traced runs fn inside a span named "<service>/<method>" with request/error
// counters and outcome logging. See grpc.Observer.Traced.
func Traced(ctx context.Context, service, method string, fn func(context.Context) error) error {
	return observer.Traced(ctx, service, method, fn)
}

// ErrorDomain scopes the machine-readable reasons below, per AIP-193.
const ErrorDomain = "freebusy.dev"

// Reasons carried in the google.rpc.ErrorInfo detail. AIP puts both "the
// inventory ran out" and "this booking is in the wrong state" on
// FAILED_PRECONDITION, so the status code alone cannot separate them — a client
// that must tell an idempotent re-cancel apart from someone taking the last room
// reads the reason, not the code or the message.
const (
	// ReasonCapacityExhausted: the unit has no room left for the window.
	ReasonCapacityExhausted = "CAPACITY_EXHAUSTED"
	// ReasonInvalidState: the resource's state forbids the transition.
	ReasonInvalidState = "INVALID_STATE"
	// ReasonConcurrentModification: an etag/CAS race; the caller may retry.
	ReasonConcurrentModification = "CONCURRENT_MODIFICATION"
)

// ToStatusErr maps repository sentinel errors onto gRPC status codes. Errors
// that are already gRPC statuses (e.g. InvalidArgument from request validation)
// pass through unchanged; anything else becomes Internal.
//
// The three failure modes that used to share one "conflict" now carry distinct
// reasons: capacity exhaustion and invalid-state both stay FailedPrecondition
// (as AIP requires) but are told apart by ErrorInfo.reason, while a CAS race
// keeps Aborted, the retryable code.
func ToStatusErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, types.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, types.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, types.ErrCapacityExhausted):
		return withReason(codes.FailedPrecondition, err, ReasonCapacityExhausted)
	case errors.Is(err, types.ErrInvalidState):
		return withReason(codes.FailedPrecondition, err, ReasonInvalidState)
	case errors.Is(err, types.ErrConflict):
		return withReason(codes.Aborted, err, ReasonConcurrentModification)
	case errors.Is(err, types.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, types.ErrUnimplemented):
		return status.Error(codes.Unimplemented, err.Error())
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, err.Error())
}

// withReason attaches an ErrorInfo detail so clients can branch on a stable
// reason instead of parsing the message. If the detail cannot be attached the
// bare status still carries code and message, which is strictly better than
// failing the call over its own error reporting.
func withReason(code codes.Code, err error, reason string) error {
	st := status.New(code, err.Error())
	detailed, derr := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain})
	if derr != nil {
		return st.Err()
	}
	return detailed.Err()
}

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
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shokki-engineering/freebusy/internal/types"
	"github.com/shokki-engineering/freebusy/shared"
	"github.com/the-protobuf-project/runtime-go/grpc"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
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
	// ReasonNotFound, ReasonAlreadyExists, ReasonInvalidArgument and
	// ReasonUnimplemented are the defaults for errors no Problem names more
	// precisely.
	ReasonNotFound        = "NOT_FOUND"
	ReasonAlreadyExists   = "ALREADY_EXISTS"
	ReasonInvalidArgument = "INVALID_ARGUMENT"
	ReasonUnimplemented   = "UNIMPLEMENTED"
)

// ToStatusErr maps freebusy's errors onto gRPC statuses a developer can act on.
//
// A types.Problem becomes its message -- what is wrong, then what to do --
// with an ErrorInfo carrying its reason and the google.rpc detail its kind
// calls for: BadRequest for a field, ResourceInfo for a missing resource,
// PreconditionFailure for a blocked call. Any other error wrapping a sentinel
// keeps its message, less the sentinel's own text ("invalid argument: ..."),
// which only repeats the status code. Errors that are already gRPC statuses
// (request validation) pass through; anything else becomes Internal.
//
// Capacity exhaustion and invalid state both stay FailedPrecondition, as AIP
// requires, and are told apart by ErrorInfo.reason; a CAS race keeps Aborted,
// the retryable code.
func ToStatusErr(err error) error {
	if err == nil {
		return nil
	}
	var p *types.Problem
	if errors.As(err, &p) {
		return problemStatus(p)
	}
	code, reason, ok := classify(err)
	if !ok {
		if _, isStatus := status.FromError(err); isStatus {
			return err
		}
		return status.Error(codes.Internal, err.Error())
	}
	return withReason(code, errors.New(Sentence(plain(err))), reason)
}

// classify is the status code and default reason for err's sentinel.
func classify(err error) (codes.Code, string, bool) {
	switch {
	case errors.Is(err, types.ErrNotFound):
		return codes.NotFound, ReasonNotFound, true
	case errors.Is(err, types.ErrAlreadyExists):
		return codes.AlreadyExists, ReasonAlreadyExists, true
	case errors.Is(err, types.ErrCapacityExhausted):
		return codes.FailedPrecondition, ReasonCapacityExhausted, true
	case errors.Is(err, types.ErrInvalidState):
		return codes.FailedPrecondition, ReasonInvalidState, true
	case errors.Is(err, types.ErrConflict):
		return codes.Aborted, ReasonConcurrentModification, true
	case errors.Is(err, types.ErrInvalidArgument):
		return codes.InvalidArgument, ReasonInvalidArgument, true
	case errors.Is(err, types.ErrUnimplemented):
		return codes.Unimplemented, ReasonUnimplemented, true
	}
	return codes.Unknown, "", false
}

// sentinels are the texts "%w: ..." puts in front of a message.
var sentinels = []error{
	types.ErrNotFound, types.ErrAlreadyExists, types.ErrCapacityExhausted,
	types.ErrInvalidState, types.ErrConflict, types.ErrInvalidArgument, types.ErrUnimplemented,
}

// plain is err's message without the sentinel texts in front of it.
func plain(err error) string {
	msg := err.Error()
	for again := true; again; {
		again = false
		for _, s := range sentinels {
			if rest, ok := strings.CutPrefix(msg, s.Error()+": "); ok {
				msg, again = rest, true
			}
		}
	}
	return msg
}

// Sentence makes msg read as one: a capital first letter and a closing full
// stop, so every message the API returns is written the same way whoever wrote
// it.
func Sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	r, n := utf8.DecodeRuneInString(msg)
	msg = string(unicode.ToUpper(r)) + msg[n:]
	if !strings.HasSuffix(msg, ".") && !strings.HasSuffix(msg, "?") {
		msg += "."
	}
	return msg
}

// problemStatus renders a Problem: its message, an ErrorInfo, and the detail
// its kind calls for.
func problemStatus(p *types.Problem) error {
	code, reason, _ := classify(p.Kind)
	if p.Reason != "" {
		reason = p.Reason
	}
	st := status.New(code, Sentence(p.Error()))
	details := []protoadapt.MessageV1{&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain, Metadata: p.Metadata}}
	switch {
	case p.Field != "":
		details = append(details, &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{Field: p.Field, Description: p.Error(), Reason: reason},
		}})
	case code == codes.FailedPrecondition && p.ResourceName != "":
		details = append(details, &errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{
			{Type: reason, Subject: p.ResourceName, Description: p.Error()},
		}})
	case p.ResourceName != "":
		details = append(details, &errdetails.ResourceInfo{
			ResourceType: p.ResourceType, ResourceName: p.ResourceName, Description: p.Fix,
		})
	}
	detailed, err := st.WithDetails(details...)
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
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

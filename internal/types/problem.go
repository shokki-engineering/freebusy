// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package types

// problem.go is how freebusy tells a developer what went wrong and what to do
// about it.
//
// Every error a caller sees should answer two questions: what is wrong, and
// what to do next. "not found" answers neither; "organization
// "organizations/acme" does not exist -- check the name, or list them with
// ListOrganizations (GET /v1/organizations)" answers both. A Problem carries
// the two separately, plus the one fact a client can act on in code: the field
// that was wrong, the resource that was missing, or the precondition that
// blocked the call. rpc.ToStatusErr renders it as the status message and the
// matching google.rpc detail (BadRequest, ResourceInfo, PreconditionFailure),
// with a stable ErrorInfo reason.

import "strings"

// Problem is an error a caller can act on. It wraps one of the sentinels
// above, which decides its status code.
type Problem struct {
	// Kind is the sentinel: ErrInvalidArgument, ErrNotFound, ErrInvalidState,
	// ErrAlreadyExists, ErrConflict, ErrCapacityExhausted.
	Kind error

	// Reason is the stable, machine-readable cause (ErrorInfo.reason), e.g.
	// ORGANIZATION_NOT_FOUND. Clients branch on it, never on the message.
	Reason string

	// What says what is wrong, in one sentence, naming the values involved.
	What string

	// Fix says what to do about it: the call to make first, the value to
	// send, where to look. Empty only when there is nothing the caller can do.
	Fix string

	// Field is the request field at fault (a BadRequest field violation),
	// e.g. "organizational_unit.organization".
	Field string

	// ResourceType and ResourceName are the resource that was missing or in
	// the way (ResourceInfo), e.g. "Organization", "organizations/acme".
	ResourceType string
	ResourceName string

	// Metadata are extra facts for ErrorInfo.metadata.
	Metadata map[string]string
}

// Error is What, then Fix.
func (p *Problem) Error() string {
	if p.Fix == "" {
		return p.What
	}
	return strings.TrimSuffix(p.What, ".") + ". " + p.Fix
}

// Unwrap is Kind, so errors.Is(p, ErrNotFound) holds for a missing resource.
func (p *Problem) Unwrap() error { return p.Kind }

// BadField is a request field that is wrong: missing, malformed, or naming
// something unusable.
func BadField(reason, field, what, fix string) *Problem {
	return &Problem{Kind: ErrInvalidArgument, Reason: reason, Field: field, What: what, Fix: fix}
}

// Missing is a resource that does not exist (or is soft-deleted, which a
// caller should treat the same: AIP-164).
func Missing(reason, resourceType, name, what, fix string) *Problem {
	return &Problem{Kind: ErrNotFound, Reason: reason, ResourceType: resourceType, ResourceName: name, What: what, Fix: fix}
}

// Blocked is a call the current state of a resource forbids: retrying the
// same call will not help until the state changes.
func Blocked(reason, resourceType, name, what, fix string) *Problem {
	return &Problem{Kind: ErrInvalidState, Reason: reason, ResourceType: resourceType, ResourceName: name, What: what, Fix: fix}
}

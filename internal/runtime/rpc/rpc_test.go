// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpc

import (
	"fmt"
	"testing"

	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestToStatusErrProblem(t *testing.T) {
	err := ToStatusErr(fmt.Errorf("wrapped: %w", types.BadField("ORGANIZATION_REQUIRED", "organizational_unit.organization",
		"an organizational unit belongs to an organization", "Create one first")))
	st := status.Convert(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %v", st.Code())
	}
	if want := "An organizational unit belongs to an organization. Create one first."; st.Message() != want {
		t.Fatalf("message = %q, want %q", st.Message(), want)
	}
	var reason, field string
	for _, d := range st.Details() {
		switch d := d.(type) {
		case *errdetails.ErrorInfo:
			reason = d.GetReason()
		case *errdetails.BadRequest:
			field = d.GetFieldViolations()[0].GetField()
		}
	}
	if reason != "ORGANIZATION_REQUIRED" || field != "organizational_unit.organization" {
		t.Fatalf("details: reason %q, field %q", reason, field)
	}
}

func TestToStatusErrPlain(t *testing.T) {
	st := status.Convert(ToStatusErr(fmt.Errorf("%w: window needs both ends", types.ErrInvalidArgument)))
	if st.Code() != codes.InvalidArgument || st.Message() != "Window needs both ends." {
		t.Fatalf("got %v %q", st.Code(), st.Message())
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"organization does not exist":            "Organization does not exist.",
		"update_mask names nothing":              "update_mask names nothing.",
		"organization.display_names is required": "organization.display_names is required.",
		"Already a sentence.":                    "Already a sentence.",
	} {
		if got := Sentence(in); got != want {
			t.Errorf("Sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpc

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/generated/servicex"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestToStatusErrProblem(t *testing.T) {
	err := ToStatusErr(fmt.Errorf("wrapped: %w", servicex.BadField("ORGANIZATION_REQUIRED", "organizational_unit.organization",
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
	st := status.Convert(ToStatusErr(fmt.Errorf("%w: window needs both ends", repox.ErrInvalidArgument)))
	if st.Code() != codes.InvalidArgument || st.Message() != "Window needs both ends." {
		t.Fatalf("got %v %q", st.Code(), st.Message())
	}
}

func TestToStatusErrReference(t *testing.T) {
	cases := []struct {
		err     error
		reason  string
		subject string
	}{
		{repox.ForeignKeyError(errors.New(`ERROR: insert or update on table "holds" violates foreign key constraint "fk_holds_organizational_unit_id" (SQLSTATE 23503)`)), repox.ReasonForeignKey, ""},
		{&repox.ReferenceError{Err: repox.ErrReferenceNotFound, Field: "customer", ResourceType: "example.com/User", Name: "users/ann"}, repox.ReasonReferenceNotFound, "users/ann"},
		{&repox.ReferenceError{Err: repox.ErrReferenced, ResourceType: "the-protobuf-project.org/Resource", Name: "resources/court-1"}, repox.ReasonReferenced, "resources/court-1"},
	}
	for _, tc := range cases {
		st := status.Convert(ToStatusErr(fmt.Errorf("create hold: %w", tc.err)))
		if st.Code() != codes.FailedPrecondition {
			t.Errorf("%v: code %v", tc.err, st.Code())
		}
		var info *errdetails.ErrorInfo
		var subject string
		for _, d := range st.Details() {
			switch d := d.(type) {
			case *errdetails.ErrorInfo:
				info = d
			case *errdetails.PreconditionFailure:
				subject = d.GetViolations()[0].GetSubject()
			}
		}
		if info.GetReason() != tc.reason || info.GetDomain() != repox.ErrorDomain || subject != tc.subject {
			t.Errorf("%v: ErrorInfo %v, subject %q", tc.err, info, subject)
		}
	}
	st := status.Convert(ToStatusErr(cases[0].err))
	if want := "The write names a resource that does not exist, or removes one that something still refers to (constraint fk_holds_organizational_unit_id); create the referenced resource first, or remove what refers to it."; st.Message() != want {
		t.Errorf("message %q", st.Message())
	}
}

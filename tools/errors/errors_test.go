// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"slices"
	"testing"
)

func TestScan(t *testing.T) {
	src := `package p

const (
	ReasonNotFound = "NOT_FOUND"
	ReasonNone Reason = iota
	other = "NOT_A_REASON_CONST"
)

func f(name, why string) error {
	_ = types.BadField("WINDOW_BACKWARDS", "window.end_time", "a", "b")
	_ = types.Missing("SITE_NOT_FOUND", "Site", name, "a", "b")
	_ = aborted("REQUEST_IN_FLIGHT", "msg")
	_ = &types.Problem{Kind: types.ErrAlreadyExists, Reason: "RESERVATION_ID_TAKEN"}
	_ = &types.Problem{Reason: why}
	_ = fmt.Sprintf("%s", "LOOKS_LIKE_ONE")
	return nil
}
`
	got, err := scan("p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{"NOT_FOUND", "REQUEST_IN_FLIGHT", "RESERVATION_ID_TAKEN", "SITE_NOT_FOUND", "WINDOW_BACKWARDS"}
	if !slices.Equal(got, want) {
		t.Errorf("scan = %v, want %v", got, want)
	}
}

func TestDocumented(t *testing.T) {
	page := "# Errors\n\n## Codes\n\n| `INVALID_ARGUMENT` | 400 |\n\n## Reasons\n\n### Any\n\n" +
		"| Reason | Code |\n|---|---|\n| `NOT_FOUND` | `NOT_FOUND` |\n| `SITE_NOT_FOUND` | `NOT_FOUND` |\n| `NOT_FOUND` | again |\n\n" +
		"## Keeping this page true\n\n| `AFTER` | x |\n"
	set, dups := documented(page)
	if len(set) != 2 || !set["NOT_FOUND"] || !set["SITE_NOT_FOUND"] {
		t.Errorf("documented = %v; want NOT_FOUND and SITE_NOT_FOUND only", set)
	}
	if !slices.Equal(dups, []string{"NOT_FOUND"}) {
		t.Errorf("dups = %v", dups)
	}
}

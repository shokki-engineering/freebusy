// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package livetest gates the live suites -- the tests that need a running
// Postgres or Hasura DDN engine -- on the environment variables naming them.
//
// Locally an unset variable skips what needs it, so `go test ./...` passes
// with neither running. In CI, FREEBUSY_TEST_REQUIRE_LIVE=1 makes an unset
// variable a failure instead: a job whose database never came up must not
// pass by testing nothing.
package livetest

import (
	"os"
	"testing"
)

// The variables the live suites read.
const (
	// PostgresDSN is the end-to-end suites' Postgres.
	PostgresDSN = "FREEBUSY_TEST_POSTGRES_DSN"
	// DatabaseURL is the repository and store suites' Postgres.
	DatabaseURL = "FREEBUSY_TEST_DATABASE_URL"
	// GraphQLURL is the Hasura DDN engine's GraphQL endpoint.
	GraphQLURL = "FREEBUSY_TEST_GRAPHQL_URL"
)

// Required reports whether live suites must run rather than skip.
func Required() bool { return os.Getenv("FREEBUSY_TEST_REQUIRE_LIVE") == "1" }

// Env returns the value of key. Unset, it skips t -- or fails it when live
// suites are required.
func Env(t testing.TB, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		if Required() {
			t.Fatalf("%s is not set, and FREEBUSY_TEST_REQUIRE_LIVE=1 requires the live suites", key)
		}
		t.Skipf("%s not set — live suite skipped", key)
	}
	return v
}

// Optional returns the value of key, for a backend a test covers only when it
// is there. Unset, it is "" -- or t fails when live suites are required.
func Optional(t testing.TB, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" && Required() {
		t.Fatalf("%s is not set, and FREEBUSY_TEST_REQUIRE_LIVE=1 requires every backend", key)
	}
	return v
}

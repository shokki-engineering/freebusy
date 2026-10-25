// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"testing"

	"github.com/shokki-engineering/freebusy/internal/config"
)

func TestDialReferences(t *testing.T) {
	if refs, err := DialReferences(nil); refs != nil || err != nil {
		t.Fatalf("no services: %v, %v", refs, err)
	}
	if _, err := DialReferences([]config.ReferenceServiceConfig{{Type: "users.shokki.engineering/User", Target: "localhost:1"}}); err == nil {
		t.Fatal("an entry without get was accepted")
	}
	t.Setenv("USERS_TARGET", "localhost:1")
	refs, err := DialReferences([]config.ReferenceServiceConfig{
		{Type: "users.shokki.engineering/User", Get: "users.v1.UserService/GetUser", Target: "${USERS_TARGET}", Plaintext: true},
		{Type: "other.example.com/Thing", Get: "other.v1.Things/GetThing", Target: "${UNSET_TARGET}"},
	})
	if err != nil || refs == nil {
		t.Fatalf("services: %v, %v", refs, err)
	}
}

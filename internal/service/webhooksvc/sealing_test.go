// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package webhooksvc

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func TestSealer(t *testing.T) {
	const secret = "whsec_c2VjcmV0"
	old, err := NewSealer(key(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal(secret)
	if err != nil || !strings.HasPrefix(sealed, sealedPrefix) || strings.Contains(sealed, "c2VjcmV0") {
		t.Fatalf("Seal = %q, %v; want a sealed value hiding the secret", sealed, err)
	}
	if again, _ := old.Seal(secret); again == sealed {
		t.Error("two seals of one secret are identical; the nonce is not random")
	}

	// Rotated key: what the old key sealed still opens; new seals use the new key.
	s, err := NewSealer(key(2), []string{key(1)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Open(sealed); err != nil || got != secret {
		t.Fatalf("Open under a previous key = %q, %v", got, err)
	}
	resealed, _ := s.Seal(secret)
	if _, err := old.Open(resealed); err == nil {
		t.Error("a sealer without the new key opened what it sealed")
	}

	// A secret stored before sealing is used as it is.
	if got, err := s.Open(secret); err != nil || got != secret {
		t.Errorf("Open(unsealed) = %q, %v", got, err)
	}
	// Without any key, a sealed value cannot be used, and says why.
	var none *Sealer
	if _, err := none.Open(sealed); err == nil || !strings.Contains(err.Error(), "secret_key") {
		t.Errorf("Open without a key: %v", err)
	}
	if got, _ := none.Seal(secret); got != secret {
		t.Errorf("a nil Sealer changed the secret: %q", got)
	}
	// Tampering is caught, not signed with garbage.
	tampered := sealed[:len(sealed)-4] + "AAA="
	if _, err := s.Open(tampered); err == nil {
		t.Error("a tampered secret opened")
	}

	for _, bad := range [][2]string{{"short", ""}, {"", key(1)}} {
		var prev []string
		if bad[1] != "" {
			prev = []string{bad[1]}
		}
		if _, err := NewSealer(bad[0], prev); err == nil {
			t.Errorf("NewSealer(%q, %v) accepted", bad[0], prev)
		}
	}
}

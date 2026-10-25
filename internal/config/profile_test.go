// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProfiles checks which layers apply. Configuration loads once per process,
// at init, so each case runs this test binary again in a directory and
// environment of its own and reads back the environment it ended with.
func TestProfiles(t *testing.T) {
	if os.Getenv("FREEBUSY_PROFILE_PROBE") == "1" {
		fmt.Printf("environment=%s\n", Get().Server.Environment)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A checkout: a development profile on disk, recognisable by its value.
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, "internal", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "internal", "config", "freebusy.dev.toml"), []byte("[server]\nenvironment = \"staging\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bare := t.TempDir()
	for _, tc := range []struct {
		name, dir string
		env       []string
		want      string
	}{
		{"a checkout's profile applies by default", checkout, nil, "staging"},
		{"release ignores a checkout's profile", checkout, []string{"FREEBUSY_PROFILE=release"}, "production"},
		{"no profile without a checkout", bare, nil, "production"},
		{"dev takes the embedded profile", bare, []string{"FREEBUSY_PROFILE=dev"}, "debug"},
		{"the environment wins", checkout, []string{"FREEBUSY_SERVER__ENVIRONMENT=development"}, "development"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=^TestProfiles$")
			cmd.Dir = tc.dir
			cmd.Env = append(append(os.Environ(), "FREEBUSY_PROFILE_PROBE=1", "FREEBUSY_PROFILE="), tc.env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(string(out), "environment="+tc.want+"\n") {
				t.Fatalf("want environment=%s, got:\n%s", tc.want, out)
			}
		})
	}
}

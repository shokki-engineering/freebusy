// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestCORSFromEnvironment checks [server.cors] defaults, comma-separated
// lists from FREEBUSY_SERVER__CORS__*, and the refusals. Configuration loads
// once per process, so each case runs this test binary again with an
// environment of its own, as TestProfiles does.
func TestCORSFromEnvironment(t *testing.T) {
	if os.Getenv("FREEBUSY_CORS_PROBE") == "1" {
		cfg, err := Load()
		if err != nil {
			fmt.Printf("error=%v\n", err)
			return
		}
		c := cfg.Server.CORS
		fmt.Printf("enabled=%t origins=%q headers=%q credentials=%t max_age=%d\n",
			c.Enabled, c.AllowedOrigins, c.AllowedHeaders, c.AllowCredentials, c.MaxAgeSeconds)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"off by default", nil, `enabled=false origins=[] headers=[] credentials=false max_age=600`},
		{"lists from the environment", []string{
			"FREEBUSY_SERVER__CORS__ENABLED=true",
			"FREEBUSY_SERVER__CORS__ALLOWED_ORIGINS=https://a.example,http://localhost:5173",
			"FREEBUSY_SERVER__CORS__ALLOWED_HEADERS=content-type,x-api-key",
			"FREEBUSY_SERVER__CORS__MAX_AGE_SECONDS=60",
		}, `enabled=true origins=["https://a.example" "http://localhost:5173"] headers=["content-type" "x-api-key"] credentials=false max_age=60`},
		{"enabled needs origins", []string{"FREEBUSY_SERVER__CORS__ENABLED=true"},
			"error=config invalid: server.cors.enabled requires server.cors.allowed_origins"},
		{"no credentials with *", []string{
			"FREEBUSY_SERVER__CORS__ENABLED=true",
			"FREEBUSY_SERVER__CORS__ALLOWED_ORIGINS=*",
			"FREEBUSY_SERVER__CORS__ALLOW_CREDENTIALS=true",
		}, "error=config invalid: server.cors.allow_credentials cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=^TestCORSFromEnvironment$")
			cmd.Dir = t.TempDir()
			cmd.Env = append(append(os.Environ(), "FREEBUSY_CORS_PROBE=1", "FREEBUSY_PROFILE=release"), tc.env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Fatalf("want %s, got:\n%s", tc.want, out)
			}
		})
	}
}

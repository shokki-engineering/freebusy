// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"strings"
	"testing"
	"time"

	"github.com/shokki-engineering/freebusy/internal/config"
)

func TestCORSOption(t *testing.T) {
	if opt, err := corsOption(config.CORSConfig{AllowedOrigins: []string{"*"}}); opt != nil || err != nil {
		t.Fatalf("disabled: option=%v err=%v, want neither", opt != nil, err)
	}
	if opt, err := corsOption(config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}); opt == nil || err != nil {
		t.Fatalf("enabled: option=%v err=%v, want an option", opt != nil, err)
	}
	for name, c := range map[string]config.CORSConfig{
		"* with credentials": {Enabled: true, AllowedOrigins: []string{"*"}, AllowCredentials: true},
		"no origins":         {Enabled: true},
		"origin with a path": {Enabled: true, AllowedOrigins: []string{"https://app.example.com/ui"}},
	} {
		if _, err := corsOption(c); err == nil || !strings.HasPrefix(err.Error(), "config invalid: server.cors:") {
			t.Errorf("%s: err = %v, want a server.cors config error", name, err)
		}
	}
}

func TestCORSPolicy(t *testing.T) {
	p := corsPolicy(config.CORSConfig{
		Enabled:          true,
		AllowedOrigins:   []string{"https://app.example.com"},
		ExposedHeaders:   []string{"x-request-id"},
		AllowCredentials: true,
		MaxAgeSeconds:    90,
	})
	if p.MaxAge != 90*time.Second || !p.AllowCredentials || len(p.ExposedHeaders) != 1 {
		t.Fatalf("policy = %+v", p)
	}
	// Empty lists are left for runtime-go's defaults.
	if p.AllowedMethods != nil || p.AllowedHeaders != nil {
		t.Fatalf("methods/headers = %v/%v, want the runtime defaults (nil)", p.AllowedMethods, p.AllowedHeaders)
	}
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"errors"
	"os"
	"reflect"
	"testing"
)

// TestFields checks that key/value pairs reach the logger as one map: it logs
// only its first data value, so pairs passed as they are lost all but the
// first key.
func TestFields(t *testing.T) {
	for _, tc := range []struct {
		in   []any
		want []any
	}{
		{nil, nil},
		{[]any{map[string]any{"a": 1}}, []any{map[string]any{"a": 1}}},
		{[]any{"projection", "resource_days", "error", errors.New("boom")}, []any{map[string]any{"projection": "resource_days", "error": "boom"}}},
		{[]any{"a", 1, "dangling"}, []any{map[string]any{"a": 1, "extra": "dangling"}}},
	} {
		if got := fields(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("fields(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestInstanceLabels(t *testing.T) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		t.Skip("no host name")
	}

	t.Setenv("FREEBUSY_TELEMETRY_INSTANCE_ID", "")
	got := instanceLabels()
	if got["service.instance.id"] != host || got["host.name"] != host {
		t.Errorf("default labels = %v, want instance id and host.name %q", got, host)
	}

	t.Setenv("FREEBUSY_TELEMETRY_INSTANCE_ID", "freebusy-abc12")
	got = instanceLabels()
	if got["service.instance.id"] != "freebusy-abc12" {
		t.Errorf("service.instance.id = %q, want the override", got["service.instance.id"])
	}
	if got["host.name"] != host {
		t.Errorf("host.name = %q, want %q", got["host.name"], host)
	}
}

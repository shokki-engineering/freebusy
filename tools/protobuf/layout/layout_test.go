// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package layout

import (
	"os"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// The cache and service outputs import the store's and the repositories'
// packages by the database and schema names the layout gives them, so the
// generators must read one layout. cache.yaml and service.yaml repeat it
// (protoc-gen-cache decodes its config strictly, service.yaml adds bindings,
// and store.backend.yaml carries keys only protoc-gen-store reads); this keeps
// the repetition honest.
func TestCacheLayoutMatchesStore(t *testing.T) {
	type layout struct {
		StripVersion      any `yaml:"strip_version"`
		DedupeSchemaTable any `yaml:"dedupe_schema_table"`
		Datasources       any `yaml:"datasources"`
	}
	read := func(path string) layout {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var l layout
		if err := yaml.Unmarshal(b, &l); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if l.Datasources == nil {
			t.Fatalf("%s has no datasources", path)
		}
		return l
	}
	const dir = "../buf/config/"
	store := read(dir + "store.backend.yaml")
	for _, name := range []string{"cache.yaml", "service.yaml"} {
		if other := read(dir + name); !reflect.DeepEqual(store, other) {
			t.Fatalf("%s's layout differs from store.backend.yaml's; copy the "+
				"strip_version, dedupe_schema_table and datasources keys across.\n\nstore: %+v\n%s: %+v", name, store, name, other)
		}
	}
}

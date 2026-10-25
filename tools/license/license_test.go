// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestWithHeader(t *testing.T) {
	cases := []struct {
		name string
		s    style
		src  string
		want string
	}{
		{"go package doc stays a doc comment",
			slashes, "// Package x does y.\npackage x\n",
			"// Copyright 2026 Srikanth K.\n// SPDX-License-Identifier: AGPL-3.0-or-later\n\n// Package x does y.\npackage x\n"},
		{"build constraint keeps its place before package",
			slashes, "//go:build linux\n\npackage x\n",
			"// Copyright 2026 Srikanth K.\n// SPDX-License-Identifier: AGPL-3.0-or-later\n\n//go:build linux\n\npackage x\n"},
		{"a foreign header is replaced",
			slashes, "// Copyright 2026 The Protobuf Project authors.\n// SPDX-License-Identifier: Apache-2.0\n\n// Code generated. DO NOT EDIT.\npackage x\n",
			"// Copyright 2026 Srikanth K.\n// SPDX-License-Identifier: AGPL-3.0-or-later\n\n// Code generated. DO NOT EDIT.\npackage x\n"},
		{"a shebang stays first",
			hashes, "#!/usr/bin/env bash\necho hi\n",
			"#!/usr/bin/env bash\n# Copyright 2026 Srikanth K.\n# SPDX-License-Identifier: AGPL-3.0-or-later\n\necho hi\n"},
		{"markdown",
			html, "# Title\n",
			"<!-- Copyright 2026 Srikanth K. -->\n<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->\n\n# Title\n"},
		{"an empty file",
			hashes, "",
			"# Copyright 2026 Srikanth K.\n# SPDX-License-Identifier: AGPL-3.0-or-later\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.s.header("AGPL-3.0-or-later")
			got := string(withHeader([]byte(tc.src), tc.s, want))
			if got != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
			if !hasHeader([]byte(got), tc.s, want) {
				t.Fatal("the written header does not check")
			}
			if again := string(withHeader([]byte(got), tc.s, want)); again != got {
				t.Fatalf("not idempotent:\n%q", again)
			}
		})
	}
}

func TestHasHeader(t *testing.T) {
	want := slashes.header("Apache-2.0")
	for src, ok := range map[string]bool{
		"// Copyright 2026 Srikanth K.\n// SPDX-License-Identifier: Apache-2.0\n":        true,
		"// Copyright 2025-2027 Srikanth K.\n// SPDX-License-Identifier: Apache-2.0\n":   true,
		"// Copyright 2026 Srikanth K.\n// SPDX-License-Identifier: AGPL-3.0-or-later\n": false, // wrong licence for the path
		"// Copyright 2026 Someone Else\n// SPDX-License-Identifier: Apache-2.0\n":       false,
		"package x\n": false,
	} {
		if got := hasHeader([]byte(src), slashes, want); got != ok {
			t.Errorf("hasHeader(%q) = %v, want %v", src, got, ok)
		}
	}
}

func TestRules(t *testing.T) {
	for file, want := range map[string]string{
		"internal/service/allocation/holds.go":              "AGPL-3.0-or-later",
		"protobuf/freebusy/webhook/v1/webhook.proto":        "Apache-2.0",
		"protobuf/generated/go/x/v1/xpbv1/x.pb.go":          "Apache-2.0",
		"deploy/hasura/freebusy/metadata/EventResource.hml": "AGPL-3.0-or-later",
	} {
		if got := licenseFor(file); got != want {
			t.Errorf("licenseFor(%s) = %s, want %s", file, got, want)
		}
		if _, ok := ruleFor(file); !ok {
			t.Errorf("no rule for %s", file)
		}
	}
	for _, file := range []string{"LICENSE", "protobuf/LICENSE", "go.sum", "a/b/config.json", "licenses/LICENSE-APACHE-2.0.txt"} {
		if r, ok := ruleFor(file); !ok || !r.exempt {
			t.Errorf("%s should be exempt", file)
		}
	}
	if r, ok := ruleFor("deploy/hasura/engine/Dockerfile.engine"); !ok || r.style != hashes {
		t.Error("a Dockerfile takes # comments")
	}
	if _, ok := ruleFor("something.unknownext"); ok {
		t.Error("an unknown type must have no rule, so the check fails on it")
	}
}

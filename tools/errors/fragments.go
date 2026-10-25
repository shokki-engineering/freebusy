// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// generated is where protoc-gen-service writes its errors.svc.md fragments:
// one table per error_domain enum, and servicex's of the standard reasons.
const generated = "internal/service/generated"

// marker matches a fragment's begin or end line and captures which.
var marker = regexp.MustCompile(`^<!-- protoc-gen-service:errors:(begin|end) (\S+) -->$`)

// fragments reads every generated fragment, keyed by the name its markers
// carry (a proto package, or "servicex").
func fragments(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "errors.svc.md" {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The license tool heads every generated file with its notice;
		// the fragment proper starts at its begin marker.
		text := strings.TrimRight(string(b), "\n")
		if i := strings.Index(text, "<!-- protoc-gen-service:errors:begin "); i > 0 {
			text = text[i:]
		}
		first, _, _ := strings.Cut(text, "\n")
		m := marker.FindStringSubmatch(first)
		if m == nil || m[1] != "begin" {
			return fmt.Errorf("%s does not start with a begin marker", path)
		}
		out[m[2]] = text
		return nil
	})
	return out, err
}

// splice replaces every marked block in page with the generated fragment of
// the same name. It fails on a block no fragment fills and on a fragment no
// block takes, so a new error_domain enum is placed in the page by hand once
// and kept current by the generator from then on.
func splice(page string, frags map[string]string) (string, error) {
	var out []string
	used := map[string]bool{}
	lines := strings.Split(page, "\n")
	for i := 0; i < len(lines); i++ {
		m := marker.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		if m[1] != "begin" {
			return "", fmt.Errorf("line %d: %s without a begin marker", i+1, m[0])
		}
		frag, ok := frags[m[2]]
		if !ok {
			return "", fmt.Errorf("line %d: no generated fragment is named %s", i+1, m[2])
		}
		end := i + 1
		for end < len(lines) && lines[end] != "<!-- protoc-gen-service:errors:end "+m[2]+" -->" {
			end++
		}
		if end == len(lines) {
			return "", fmt.Errorf("line %d: %s has no end marker", i+1, m[2])
		}
		out = append(out, frag)
		used[m[2]] = true
		i = end
	}
	for name := range frags {
		if !used[name] {
			return "", fmt.Errorf("the generated fragment %s has no place in %s: add a begin and an end marker for it", name, doc)
		}
	}
	return strings.Join(out, "\n"), nil
}

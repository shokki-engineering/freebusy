// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command errors checks that docs/errors.md -- the error contract clients
// branch on -- lists exactly the reasons the server can return: every
// ErrorInfo reason found in the Go source under internal/, and the few that
// come from libraries (external). A reason the page lacks, or one it lists
// that nothing returns any more, fails the check.
//
//	go run ./tools/errors
package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// doc is the contract page, relative to the repository root.
const doc = "docs/errors.md"

// external are reasons a library returns on freebusy's behalf.
var external = []string{
	"INVALID_REQUEST_FIELDS", // runtime-go/grpc: a request failing its buf.validate rules
}

func main() {
	files, err := sources()
	if err != nil {
		fail(err)
	}
	found := map[string]bool{}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fail(err)
		}
		reasons, err := scan(f, src)
		if err != nil {
			fail(err)
		}
		for _, r := range reasons {
			found[r] = true
		}
	}
	for _, r := range external {
		found[r] = true
	}
	page, err := os.ReadFile(doc)
	if err != nil {
		fail(err)
	}
	documented, dups := documented(string(page))

	var problems []string
	for _, r := range sorted(found) {
		if !documented[r] {
			problems = append(problems, fmt.Sprintf("%s is returned but not documented: add a row for it to %s", r, doc))
		}
	}
	for _, r := range sorted(documented) {
		if !found[r] {
			problems = append(problems, fmt.Sprintf("%s is documented in %s, but nothing returns it: remove its row", r, doc))
		}
	}
	for _, r := range dups {
		problems = append(problems, fmt.Sprintf("%s has more than one row in %s", r, doc))
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		os.Exit(1)
	}
}

// sources are the non-test Go files under internal/ that git tracks or would.
func sources() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "internal/*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && !strings.HasSuffix(f, "_test.go") {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

func sorted(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "errors:", err)
	os.Exit(2)
}

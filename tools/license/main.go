// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command license checks, and with -fix writes, the copyright and SPDX header
// every file in the repository carries:
//
//	// Copyright 2026 Srikanth K.
//	// SPDX-License-Identifier: AGPL-3.0-or-later
//
// in the comment syntax of the file's type. The server is AGPL-3.0-or-later;
// the contract under protobuf/ is Apache-2.0 (see LICENSE-COMMERCIAL.md).
//
// The files are the ones git tracks or would track, so .gitignore decides
// what is in scope. A file of a type with no rule fails the check: a new kind
// of file is a decision to make, not one to skip.
//
//	go run ./tools/license        # check; exits 1 listing every file without its header
//	go run ./tools/license -fix   # write the missing headers, replace foreign ones
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	fix := flag.Bool("fix", false, "write missing headers and replace foreign ones instead of reporting them")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		var err error
		if files, err = repoFiles(); err != nil {
			fmt.Fprintln(os.Stderr, "license:", err)
			os.Exit(2)
		}
	}

	var problems []string
	fixed := 0
	for _, path := range files {
		r, ok := ruleFor(path)
		if !ok {
			problems = append(problems, path+": no header rule for this type of file; add one to rules.go or exempt it")
			continue
		}
		if r.exempt {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted in the working tree
			}
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if bytes.IndexByte(src, 0) >= 0 {
			continue // binary
		}
		want := r.style.header(licenseFor(path))
		if hasHeader(src, r.style, want) {
			continue
		}
		if !*fix {
			problems = append(problems, path+": missing or wrong header; want\n\t"+strings.ReplaceAll(strings.TrimSpace(string(want)), "\n", "\n\t"))
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if err := os.WriteFile(path, withHeader(src, r.style, want), info.Mode().Perm()); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		fixed++
	}

	if *fix && fixed > 0 {
		fmt.Printf("license: wrote %d header(s)\n", fixed)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, p)
		}
		if !*fix {
			fmt.Fprintf(os.Stderr, "\nlicense: %d file(s) without their header; `go run ./tools/license -fix` writes them\n", len(problems))
		}
		os.Exit(1)
	}
}

// repoFiles lists what git tracks or would track, so ignored files -- build
// output, local certificates, .env files -- are out of scope.
func repoFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

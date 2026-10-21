// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Rendering the Envoy config from collected services.
package main

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"
)

// render writes the launch.yaml template filled with data.
func render(path string, data model) (err error) {
	tmpl, err := template.New("launch.yaml").Parse(launchTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return tmpl.Execute(f, data)
}

// --- helpers (mirrors tools/protobuf/openapi) ---

// findFiles returns every file under dir whose name ends with suffix, sorted.
func findFiles(dir, suffix string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), suffix) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// repoRoot walks up from the working directory to the directory holding go.mod
// so paths resolve regardless of where the command is invoked from.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func logf(level, format string, args ...any) {
	if level == "DEBUG" && !verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "%s %-7s %s\n", time.Now().Format("15:04:05"), level, fmt.Sprintf(format, args...))
}

func fatalf(format string, args ...any) {
	logf("ERROR", format, args...)
	os.Exit(1)
}

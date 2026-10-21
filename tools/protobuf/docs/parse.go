// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Proto-file discovery and the line-oriented parser filling the doc model.
package main

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoverModules finds all module directories under protoDir.
// A module directory is one that contains version subdirs (v1/, v2/) with .proto files.
func discoverModules(protoDir string) ([]Module, error) {
	var modules []Module

	err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}

		// Look for version sub-directories containing .proto files.
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil
		}

		var protoFiles []string
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "v") {
				continue
			}
			versionDir := filepath.Join(path, e.Name())
			files, _ := filepath.Glob(filepath.Join(versionDir, "*.proto"))
			protoFiles = append(protoFiles, files...)
		}

		if len(protoFiles) == 0 {
			return nil
		}

		mod := Module{Dir: path}
		sort.Strings(protoFiles)

		for _, pf := range protoFiles {
			if err := parseProtoFile(pf, &mod); err != nil {
				log.Printf("Warning: failed to parse %s: %v", pf, err)
			}
		}

		// Derive module name from directory.
		rel, _ := filepath.Rel(protoDir, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) > 0 {
			last := parts[len(parts)-1]
			mod.Name = titleCase(last)
		}

		modules = append(modules, mod)
		return filepath.SkipDir
	})

	return modules, err
}

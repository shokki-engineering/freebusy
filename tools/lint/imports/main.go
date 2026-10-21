// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// Default to scanning the current directory, or accept a path via CLI argument
	targetDir := "."
	if len(os.Args) > 1 {
		targetDir = os.Args[1]
	}

	fmt.Printf("Scanning project at: %s\n", targetDir)
	fmt.Println(strings.Repeat("-", 40))

	fset := token.NewFileSet()

	err := filepath.WalkDir(targetDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Optimization: Skip massive directories we usually don't want to scan
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		// We only care about Go files
		if filepath.Ext(path) != ".go" {
			return nil
		}

		// parser.ImportsOnly is much faster than parsing the entire syntax tree
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			// Skip files with syntax errors
			return nil
		}

		hasPrintedFileName := false

		// Iterate through all imports in the file
		for _, imp := range f.Imports {
			// imp.Name is non-nil if the import has an alias (including _ and .)
			if imp.Name != nil {
				if !hasPrintedFileName {
					fmt.Printf("\n📄 %s\n", path)
					hasPrintedFileName = true
				}
				fmt.Printf("   Alias: %-10s Path: %s\n", imp.Name.Name, imp.Path.Value)
			}
		}

		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning directory: %v\n", err)
		os.Exit(1)
	}
}

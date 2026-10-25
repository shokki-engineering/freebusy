// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command openapi merges the generated per-service OpenAPI specs under
// protobuf/generated/openapi into a single openapiv3-spec.yaml.
//
// It loads every *.openapi.yaml (paths + "weak" component definitions), overlays
// the hand-authored strong components from types/*.yaml, patches operations with
// a summary and a default 4xx response, derives the top-level tag list, prunes
// component schemas that nothing references, and writes the merged document.
//
// Usage:
//
//	go run ./tools/protobuf/openapi [spec-dir] [-o output] [-v]
//
// spec-dir defaults to protobuf/generated/openapi under the repo root; the
// output defaults to <spec-dir>/openapiv3-spec.yaml. This is the Go port of the
// former main.py and needs no Python/PyYAML.
package main

import (
	_ "embed"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// baseSpec is the merged document's skeleton — everything fixed rather than
// derived from the per-service specs. It is loaded from base.yaml in this
// directory (embedded so the tool stays self-contained); override it at runtime
// with -base.
//
//go:embed base.yaml
var baseSpec []byte

// badRequestResponse is the default 400 added to operations lacking any 4xx.
const badRequestResponse = `description: Bad Request
content:
  application/json:
    schema:
      $ref: '#/components/schemas/Status'
`

// httpMethods is the set of OpenAPI operation keys under a path item.
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true, "trace": true,
}

var verbose bool

func main() {
	root := repoRoot()
	defaultSpecDir := filepath.Join(root, "protobuf", "generated", "openapi")
	defaultOutput := filepath.Join(defaultSpecDir, "openapiv3-spec.yaml")

	var output string
	flag.StringVar(&output, "output", defaultOutput, "Path to write the merged spec")
	flag.StringVar(&output, "o", defaultOutput, "Path to write the merged spec (shorthand)")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose (debug) logging")
	flag.BoolVar(&verbose, "v", false, "Enable verbose (debug) logging (shorthand)")
	var basePath string
	flag.StringVar(&basePath, "base", "", "Path to the base spec YAML (default: embedded base.yaml)")
	flag.Parse()

	specDir := defaultSpecDir
	if flag.NArg() > 0 {
		specDir = flag.Arg(0)
	}
	specDir, _ = filepath.Abs(specDir)
	if fi, err := os.Stat(specDir); err != nil || !fi.IsDir() {
		fatalf("spec directory does not exist: %s", specDir)
	}

	base := baseSpec
	if basePath != "" {
		b, err := os.ReadFile(basePath)
		if err != nil {
			fatalf("read base spec %q: %v", basePath, err)
		}
		base = b
	}
	merged, err := parseYAML(string(base))
	if err != nil {
		fatalf("parse base spec: %v", err)
	}
	paths := mapGet(merged, "paths")
	paths.Style = 0 // base has `paths: {}` (flow); emit block once populated.
	components := mapGet(merged, "components")

	// Step 1: paths + weak components from *.openapi.yaml.
	mergePathsAndWeakComponents(specDir, paths, components)
	// Step 2: strong component definitions from types/*.yaml.
	mergeStrongComponents(specDir, components)
	// Step 3: patch operations with summary and 4xx responses.
	patchOperations(paths)
	// Step 4: populate the top-level tags list from operation tags, described
	// as the per-package documents describe them, and carry the packages' own
	// diagrams (ER diagrams, package-level flows) under info.x-mermaid.
	described, diagrams := packageDocs(specDir)
	mapSet(merged, "tags", collectTags(paths, described))
	if len(diagrams) > 0 {
		mapSet(mapGet(merged, "info"), "x-mermaid", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: diagrams})
	}
	// Step 5: drop components nothing references.
	pruneUnusedComponents(merged)

	// Step 6: write the merged output.
	output, _ = filepath.Abs(output)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		fatalf("create output dir: %v", err)
	}
	if err := writeYAML(output, merged); err != nil {
		fatalf("write output: %v", err)
	}

	tags := mapGet(merged, "tags")
	logf("INFO", "merged spec written to %s (%d paths, %d schemas, %d tags)",
		output, pairs(paths), pairs(schemasOf(components)), len(tags.Content))
}

// splitCamelCase inserts a space at each lower/digit -> upper boundary, e.g.
// "BookingService" -> "Booking Service".
func splitCamelCase(name string) string {
	var b strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			if p := runes[i-1]; (p >= 'a' && p <= 'z') || (p >= '0' && p <= '9') {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// humanizeOperationID turns "ServiceName_MethodName" into "Method Name".
func humanizeOperationID(opID string) string {
	if opID == "" {
		return ""
	}
	parts := strings.SplitN(opID, "_", 2)
	return splitCamelCase(parts[len(parts)-1])
}

// --- yaml.Node helpers ---

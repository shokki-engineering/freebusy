// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Discovering gRPC services from the OpenAPI specs.
package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// collectServices reads every *_service.openapi.yaml under specDir and returns
// the gRPC services to route, de-duplicated by full name and sorted by prefix.
func collectServices(specDir string) []service {
	files := findFiles(specDir, "_service.openapi.yaml")
	if len(files) == 0 {
		logf("WARN", "no *_service.openapi.yaml files found under %s", specDir)
	}

	seen := map[string]bool{}
	var services []service
	for _, file := range files {
		rel, _ := filepath.Rel(specDir, file)
		pkg := packageFromPath(filepath.Dir(rel))
		if pkg == "" {
			logf("WARN", "skipping %s: cannot derive package from path", rel)
			continue
		}
		name := serviceName(file)
		if name == "" {
			logf("WARN", "skipping %s: no operationId to derive a service name from", rel)
			continue
		}
		full := pkg + "." + name
		if seen[full] {
			continue
		}
		seen[full] = true
		services = append(services, service{
			Package: pkg,
			Name:    name,
			Prefix:  "/" + full + "/",
		})
		logf("DEBUG", "routing %s (from %s)", full, rel)
	}

	sort.Slice(services, func(i, j int) bool { return services[i].Prefix < services[j].Prefix })
	return services
}

// packageFromPath turns a relative directory like "freebusy/promocode/v1" into
// the proto package "freebusy.promocode.v1".
func packageFromPath(dir string) string {
	dir = filepath.ToSlash(dir)
	if dir == "" || dir == "." {
		return ""
	}
	return strings.ReplaceAll(dir, "/", ".")
}

// serviceName reads the first operationId from a service spec and returns the
// "<Service>" portion of its "<Service>_<Method>" convention.
func serviceName(file string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		logf("ERROR", "read %s: %v", file, err)
		return ""
	}
	var doc serviceDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		logf("ERROR", "parse %s: %v", file, err)
		return ""
	}
	// Paths/methods iterate in map order; collect candidate names so the result
	// is deterministic regardless of which operation we see first.
	names := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range item {
			if op.OperationID == "" {
				continue
			}
			if i := strings.IndexByte(op.OperationID, '_'); i > 0 {
				names[op.OperationID[:i]] = true
			}
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		for n := range names {
			return n
		}
	}
	// A single file is expected to define one service; if it somehow spans
	// several, pick the lexically first so output stays stable, and warn.
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	logf("WARN", "%s defines multiple services %v; using %q", filepath.Base(file), sorted, sorted[0])
	return sorted[0]
}

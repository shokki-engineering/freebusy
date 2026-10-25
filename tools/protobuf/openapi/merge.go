// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Merging per-service specs into one document: paths, weak/strong components.
package main

import (
	_ "embed"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// mergePathsAndWeakComponents loads every *.openapi.yaml (recursively) and folds
// its paths (last loaded wins) and components (first definition wins) in.
func mergePathsAndWeakComponents(specDir string, paths, components *yaml.Node) {
	files := findFiles(specDir, ".openapi.yaml")
	if len(files) == 0 {
		logf("WARN", "no *.openapi.yaml files found under %s", specDir)
	}
	for _, file := range files {
		doc, err := loadYAMLFile(file)
		if err != nil {
			logf("ERROR", "failed to parse %s: %v", file, err)
			continue
		}
		rel, _ := filepath.Rel(specDir, file)
		if doc == nil || doc.Kind != yaml.MappingNode {
			logf("WARN", "skipping non-object YAML: %s", rel)
			continue
		}
		logf("INFO", "merging %s", rel)

		if p := mapGet(doc, "paths"); p != nil && p.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(p.Content); i += 2 {
				key := p.Content[i].Value
				if mapHas(paths, key) {
					logf("WARN", "duplicate path %q; keeping last loaded", key)
				}
				mapSet(paths, key, p.Content[i+1])
			}
		}
		if c := mapGet(doc, "components"); c != nil {
			mergeComponentSections(components, c, false)
		}
	}
}

// mergeStrongComponents overlays the hand-authored strong component definitions
// from types/*.yaml, overriding any weak definitions of the same name.
func mergeStrongComponents(specDir string, components *yaml.Node) {
	files, _ := filepath.Glob(filepath.Join(specDir, "types", "*.yaml"))
	sort.Strings(files)
	for _, file := range files {
		doc, err := loadYAMLFile(file)
		if err != nil {
			logf("ERROR", "failed to parse type file %s: %v", file, err)
			continue
		}
		if doc == nil || doc.Kind != yaml.MappingNode || mapGet(doc, "components") == nil {
			logf("DEBUG", "skipping non-component file: %s", filepath.Base(file))
			continue
		}
		logf("INFO", "loading strong components from %s", filepath.Base(file))
		mergeComponentSections(components, mapGet(doc, "components"), true)
		carryDiagrams(components, mapGet(doc, "components"))
	}
}

// carryDiagrams gives each schema the paths reference by its short name
// ("HoldState") the diagram its package document drew for it under its full
// name ("freebusy.allocation.v1.HoldState"): the description that embeds the
// state diagram, and the raw diagram under x-mermaid.
func carryDiagrams(target, source *yaml.Node) {
	from, to := schemasOf(source), schemasOf(target)
	if from == nil || to == nil {
		return
	}
	for i := 0; i+1 < len(from.Content); i += 2 {
		full, schema := from.Content[i].Value, from.Content[i+1]
		if mapGet(schema, "x-mermaid") == nil {
			continue
		}
		short := full[strings.LastIndex(full, ".")+1:]
		weak := mapGet(to, short)
		if weak == nil || weak == schema {
			continue
		}
		for _, key := range []string{"description", "x-mermaid"} {
			if v := mapGet(schema, key); v != nil {
				mapSet(weak, key, v)
			}
		}
	}
}

// packageDocs reads what the per-package documents under types/ say beyond
// their components: each service tag (description and x-mermaid, by name) and
// each package's own diagrams (info.x-mermaid: its ER diagram and flows) and
// its lifecycle enums' state diagrams.
func packageDocs(specDir string) (tags map[string]*yaml.Node, diagrams []*yaml.Node) {
	tags = map[string]*yaml.Node{}
	files, _ := filepath.Glob(filepath.Join(specDir, "types", "*.yaml"))
	sort.Strings(files)
	for _, file := range files {
		doc, err := loadYAMLFile(file)
		if err != nil || doc == nil || doc.Kind != yaml.MappingNode {
			continue
		}
		if t := mapGet(doc, "tags"); t != nil && t.Kind == yaml.SequenceNode {
			for _, tag := range t.Content {
				if name := mapGet(tag, "name"); name != nil {
					// Operations are tagged "Hold Service" by the time the
					// tags are collected; the package documents say
					// "HoldService".
					tags[name.Value] = tag
					tags[splitCamelCase(name.Value)] = tag
				}
			}
		}
		if m := mapGet(mapGet(doc, "info"), "x-mermaid"); m != nil && m.Kind == yaml.SequenceNode {
			diagrams = append(diagrams, m.Content...)
		}
		// The lifecycle enums' state diagrams too: gnostic inlines most
		// enums, so the schema a diagram was drawn on may not survive the
		// merge, and the diagram must not go with it.
		if schemas := schemasOf(mapGet(doc, "components")); schemas != nil {
			for i := 0; i+1 < len(schemas.Content); i += 2 {
				if m := mapGet(schemas.Content[i+1], "x-mermaid"); m != nil && m.Kind == yaml.SequenceNode {
					diagrams = append(diagrams, m.Content...)
				}
			}
		}
	}
	return tags, diagrams
}

// mergeComponentSections merges each section of source into target. When a key
// already exists, preferStrong replaces it; otherwise the existing one is kept.
func mergeComponentSections(target, source *yaml.Node, preferStrong bool) {
	if source.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(source.Content); i += 2 {
		section := source.Content[i].Value
		content := source.Content[i+1]
		if content.Kind != yaml.MappingNode {
			continue
		}
		targetSection := mapGet(target, section)
		if targetSection == nil {
			targetSection = newMapping()
			mapSet(target, section, targetSection)
		}
		for j := 0; j+1 < len(content.Content); j += 2 {
			key := content.Content[j].Value
			if mapHas(targetSection, key) {
				if preferStrong {
					logf("DEBUG", "overriding component %q in %q with strong type definition", key, section)
				} else {
					logf("DEBUG", "duplicate component %q in %q; keeping existing definition", key, section)
					continue
				}
			}
			mapSet(targetSection, key, content.Content[j+1])
		}
	}
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Post-merge spec surgery: operation patches, tag collection, unused-component pruning.
package main

import (
	_ "embed"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// patchOperations gives every operation a summary (if missing), humanizes its
// tags, and adds a default 400 response when it declares no 4xx.
func patchOperations(paths *yaml.Node) {
	if paths.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		pathKey := paths.Content[i].Value
		item := paths.Content[i+1]
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := item.Content[j].Value
			op := item.Content[j+1]
			if !httpMethods[method] || op.Kind != yaml.MappingNode {
				continue
			}
			if tags := mapGet(op, "tags"); tags != nil && tags.Kind == yaml.SequenceNode {
				for _, t := range tags.Content {
					if t.Kind == yaml.ScalarNode {
						t.Value = splitCamelCase(t.Value)
					}
				}
			}
			if !mapHas(op, "summary") {
				summary := humanizeOperationID(scalar(mapGet(op, "operationId")))
				if summary == "" {
					summary = strings.ToUpper(method) + " " + pathKey
				}
				mapSet(op, "summary", strNode(summary))
			}
			responses := mapGet(op, "responses")
			if responses == nil {
				responses = newMapping()
				mapSet(op, "responses", responses)
			}
			if responses.Kind == yaml.MappingNode && !has4xx(responses) {
				if r, err := parseYAML(badRequestResponse); err == nil {
					mapSet(responses, "400", r)
				}
			}
		}
	}
}

// collectTags builds the top-level tags sequence from the tags used across all
// operations, de-duplicated and sorted.
func collectTags(paths *yaml.Node) *yaml.Node {
	names := map[string]bool{}
	if paths.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(paths.Content); i += 2 {
			item := paths.Content[i+1]
			if item.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(item.Content); j += 2 {
				if !httpMethods[item.Content[j].Value] {
					continue
				}
				if tags := mapGet(item.Content[j+1], "tags"); tags != nil {
					for _, t := range tags.Content {
						if t.Kind == yaml.ScalarNode {
							names[t.Value] = true
						}
					}
				}
			}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, n := range sorted {
		m := newMapping()
		mapSet(m, "name", strNode(n))
		seq.Content = append(seq.Content, m)
	}
	return seq
}

// pruneUnusedComponents removes component schemas that nothing references,
// iterating because removing a schema can orphan others.
func pruneUnusedComponents(root *yaml.Node) {
	components := mapGet(root, "components")
	schemas := schemasOf(components)
	if schemas == nil || len(schemas.Content) == 0 {
		return
	}
	paths := mapGet(root, "paths")

	referenced := func() map[string]bool {
		refs := map[string]bool{}
		findRefs(paths, refs)
		findRefs(components, refs)
		names := map[string]bool{}
		for ref := range refs {
			if strings.HasPrefix(ref, "#/components/schemas/") {
				names[ref[strings.LastIndex(ref, "/")+1:]] = true
			}
		}
		return names
	}

	removed := 0
	for {
		used := referenced()
		var unused []string
		for i := 0; i+1 < len(schemas.Content); i += 2 {
			if name := schemas.Content[i].Value; !used[name] {
				unused = append(unused, name)
			}
		}
		if len(unused) == 0 {
			break
		}
		for _, name := range unused {
			deleteKey(schemas, name)
		}
		removed += len(unused)
	}
	if removed > 0 {
		logf("INFO", "pruned %d unreferenced component schema(s)", removed)
	}
}

// findRefs collects every $ref string reachable from n.
func findRefs(n *yaml.Node, refs map[string]bool) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "$ref" && n.Content[i+1].Kind == yaml.ScalarNode {
				refs[n.Content[i+1].Value] = true
			}
			findRefs(n.Content[i+1], refs)
		}
		return
	}
	for _, c := range n.Content { // sequence / document
		findRefs(c, refs)
	}
}

// --- text helpers ---

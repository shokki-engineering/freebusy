// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// yaml.Node plumbing: tiny typed accessors over the mutable tree.
package main

import (
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func parseYAML(s string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		return nil, err
	}
	return docRoot(&doc), nil
}

func loadYAMLFile(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return docRoot(&doc), nil
}

func writeYAML(path string, root *yaml.Node) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// Surface a close error (e.g. a failed final flush) when the write itself
	// otherwise succeeded, rather than silently dropping it.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	enc := yaml.NewEncoder(f)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	return enc.Close()
}

func docRoot(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapHas(m *yaml.Node, key string) bool { return mapGet(m, key) != nil }

// mapSet updates key in place when present, otherwise appends it (preserving
// insertion order, like the Python dict).
func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, strNode(key), val)
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func newMapping() *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}

func scalar(n *yaml.Node) string {
	if n != nil && n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return ""
}

func has4xx(responses *yaml.Node) bool {
	for i := 0; i+1 < len(responses.Content); i += 2 {
		if strings.HasPrefix(responses.Content[i].Value, "4") {
			return true
		}
	}
	return false
}

func schemasOf(components *yaml.Node) *yaml.Node {
	if components == nil {
		return nil
	}
	return mapGet(components, "schemas")
}

func pairs(m *yaml.Node) int {
	if m == nil {
		return 0
	}
	return len(m.Content) / 2
}

// --- misc helpers ---

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

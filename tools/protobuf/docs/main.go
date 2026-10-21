// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// protodoc generates documentation for a tree of protobuf definitions.
//
// For every module directory (a directory whose version sub-dirs — v1/, v2/ …
// — contain .proto files) it writes a per-module README.md describing the
// services, RPCs, messages, and enums found there. It then writes a single
// top-level README.md summarising every module and rendering a Mermaid graph
// of the local import relationships between them (see summary.go).
//
// Usage:
//
//	go run ./tools/protobuf/docs [protobuf-dir]
//
// If no argument is given, protobuf-dir defaults to "protobuf".
//
// All generated files carry an "auto-generated, do not edit" banner and are
// overwritten on every run. The Markdown is emitted in a markdownlint-clean
// style (padded table pipes, no blank lines inside blockquotes).
package main

import (
	"log"
	"os"
	"regexp"
)

// --- Data Model ---

type Module struct {
	Name     string   // e.g., "Interaction"
	Package  string   // e.g., "engine.interaction.v1"
	Dir      string   // path to module dir (relative to the CWD, as walked)
	Imports  []string // raw import paths collected from all .proto files
	Services []Service
	Messages []Message
	Enums    []Enum
}

type Service struct {
	Name    string
	Comment string
	RPCs    []RPC
}

type RPC struct {
	Name         string
	Comment      string
	Request      string
	Response     string
	ServerStream bool
}

type Message struct {
	Name    string
	Comment string
	Fields  []Field
}

type Field struct {
	Name     string
	Type     string
	Comment  string
	Required bool   // true if REQUIRED or IDENTIFIER
	Repeated bool   // true for `repeated` fields (rendered as a list)
	Behavior string // e.g. REQUIRED, OPTIONAL, OUTPUT_ONLY, IDENTIFIER
}

type Enum struct {
	Name    string
	Comment string
	Values  []EnumValue
}

type EnumValue struct {
	Name    string
	Number  string
	Comment string
}

// --- Regex patterns ---

var (
	rePackage       = regexp.MustCompile(`^package\s+([\w.]+)\s*;`)
	reImport        = regexp.MustCompile(`^import\s+"([^"]+)"\s*;`)
	reService       = regexp.MustCompile(`^service\s+(\w+)\s*\{`)
	reRPC           = regexp.MustCompile(`^\s*rpc\s+(\w+)\s*\(\s*(\w+)\s*\)\s*returns\s*\(\s*(stream\s+)?(\w+)\s*\)`)
	reMessage       = regexp.MustCompile(`^message\s+(\w+)\s*\{`)
	reEnum          = regexp.MustCompile(`^enum\s+(\w+)\s*\{`)
	reField         = regexp.MustCompile(`^\s*(?:optional\s+|repeated\s+)?(\S+)\s+(\w+)\s*=\s*\d+`)
	reEnumVal       = regexp.MustCompile(`^\s*(\w+)\s*=\s*(\d+)`)
	reComment       = regexp.MustCompile(`^\s*//\s?(.*)`)
	reFieldBehavior = regexp.MustCompile(`\(google\.api\.field_behavior\)\s*=\s*(\w+)`)
)

func main() {
	protoDir := "protobuf"
	if len(os.Args) > 1 {
		protoDir = os.Args[1]
	}

	modules, err := discoverModules(protoDir)
	if err != nil {
		log.Fatalf("Failed to discover modules: %v", err)
	}

	for _, mod := range modules {
		if err := generateReadme(mod); err != nil {
			log.Printf("Warning: failed to generate README for %s: %v", mod.Dir, err)
			continue
		}
		log.Printf("Generated README.md for %s (%d services, %d messages, %d enums)",
			mod.Package, len(mod.Services), len(mod.Messages), len(mod.Enums))
	}

	// Top-level summary + dependency graph spanning every module.
	if err := generateRootReadme(protoDir, modules); err != nil {
		log.Printf("Warning: failed to generate root README: %v", err)
	} else {
		log.Printf("Generated root summary README.md for %s/", protoDir)
	}

	log.Printf("Done. Generated %d module READMEs + 1 root summary.", len(modules))
}

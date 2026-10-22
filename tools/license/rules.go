// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"path"
	"strings"
)

// Holder is who every file is copyright of.
const Holder = "Srikanth K."

// Year is written into new headers. An existing header may carry any year or
// range, so a file written in 2026 keeps its year.
const Year = "2026"

// licenseFor is a file's SPDX licence: the contract under protobuf/ is
// Apache-2.0, so anyone may build a client; everything else, the server, is
// AGPL-3.0-or-later (or commercial, see LICENSE-COMMERCIAL.md).
func licenseFor(file string) string {
	if strings.HasPrefix(file, "protobuf/") {
		return "Apache-2.0"
	}
	return "AGPL-3.0-or-later"
}

// rule is how one kind of file carries its header.
type rule struct {
	style  style
	exempt bool
}

var (
	slashes = style{open: "// "}
	hashes  = style{open: "# "}
	dashes  = style{open: "-- "}
	html    = style{open: "<!-- ", close: " -->"}
	gotmpl  = style{open: "{{/* ", close: " */}}"}
	exempt  = rule{exempt: true}
)

// byName are rules for whole file names, checked first.
var byName = map[string]rule{
	"go.mod":         {style: slashes},
	"go.work":        {style: slashes},
	"justfile":       {style: hashes},
	".gitignore":     {style: hashes},
	".gitattributes": {style: hashes},
	".bazelrc":       {style: hashes},
	".ddnignore":     {style: hashes},
	".env":           {style: hashes},

	// Legal texts carry their own terms; a header would amend them.
	"LICENSE":               exempt,
	"NOTICE":                exempt,
	"CLA.md":                exempt,
	"LICENSE-COMMERCIAL.md": exempt,

	// Machine-written, and their formats have no comments, or the tool that
	// owns them rewrites the whole file.
	"go.sum":            exempt,
	"go.work.sum":       exempt,
	"buf.lock":          exempt,
	"MODULE.bazel.lock": exempt,
	".bazelversion":     exempt,
	".keep":             exempt,
}

// byExt are rules for file extensions.
var byExt = map[string]rule{
	".go":     {style: slashes},
	".proto":  {style: slashes},
	".prisma": {style: slashes},
	".ts":     {style: slashes},

	".tpl": {style: gotmpl}, // Helm's named templates

	".yaml":    {style: hashes},
	".yml":     {style: hashes},
	".hml":     {style: hashes},
	".toml":    {style: hashes},
	".bazel":   {style: hashes},
	".just":    {style: hashes},
	".sh":      {style: hashes},
	".example": {style: hashes},
	".tmpl":    {style: hashes},

	".sql": {style: dashes},
	".md":  {style: html},

	// No comment syntax.
	".json": exempt,
	".txt":  exempt,
	".sum":  exempt,
}

// ruleFor finds a file's rule: by name, then by the Dockerfile convention,
// then by extension. ok is false when no rule covers it.
func ruleFor(file string) (rule, bool) {
	if strings.HasPrefix(file, "licenses/") {
		return exempt, true // licence texts, verbatim
	}
	name := path.Base(file)
	if r, ok := byName[name]; ok {
		return r, true
	}
	if name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") {
		return rule{style: hashes}, true
	}
	r, ok := byExt[path.Ext(name)]
	return r, ok
}

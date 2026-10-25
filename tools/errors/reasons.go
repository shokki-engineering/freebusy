// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// constructors take a reason as their first argument: servicex's
// BadField, Missing and Blocked, and the idempotency interceptor's aborted.
var constructors = map[string]bool{"BadField": true, "Missing": true, "Blocked": true, "aborted": true}

var reasonShape = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$|^[A-Z]{3,}$`)

// scan returns the reasons one Go file can return: the string constants named
// Reason* (rpc's defaults, which every unclassified error falls back on, and
// which a Problem may name as rpc.ReasonX), the first argument of a
// constructor, and a literal Reason field in a composite literal (a
// types.Problem). Values computed at run time are not reasons the page can
// list, and are skipped.
func scan(filename string, src []byte) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), filename, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(e ast.Expr) {
		if r, ok := reasonOf(e); ok {
			out = append(out, r)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if strings.HasPrefix(name.Name, "Reason") && i < len(n.Values) {
					add(n.Values[i])
				}
			}
		case *ast.CallExpr:
			if constructors[calleeName(n.Fun)] && len(n.Args) > 0 {
				add(n.Args[0])
			}
		case *ast.KeyValueExpr:
			if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Reason" {
				add(n.Value)
			}
		}
		return true
	})
	return out, nil
}

// reasonOf is the reason e spells: a string literal shaped like one.
func reasonOf(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil || !reasonShape.MatchString(s) {
		return "", false
	}
	return s, true
}

func calleeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

var row = regexp.MustCompile("^\\| `([A-Z][A-Z0-9_]*)` \\|")

// documented is the set of reasons the page's tables list, from its
// "## Reasons" section on (the codes table above it lists gRPC codes, not
// reasons), and any listed twice.
func documented(page string) (map[string]bool, []string) {
	set := map[string]bool{}
	var dups []string
	inReasons := false
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(line, "## ") {
			inReasons = strings.TrimSpace(line) == "## Reasons"
			continue
		}
		if !inReasons {
			continue
		}
		if m := row.FindStringSubmatch(line); m != nil {
			if set[m[1]] {
				dups = append(dups, m[1])
			}
			set[m[1]] = true
		}
	}
	return set, dups
}

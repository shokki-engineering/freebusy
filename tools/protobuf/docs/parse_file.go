// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// The line-oriented .proto parser filling the doc model.
package main

import (
	"bufio"
	"os"
	"strings"
)

// parseProtoFile parses a single .proto file and appends to the module.
func parseProtoFile(path string, mod *Module) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // read-only; close error is not meaningful

	scanner := bufio.NewScanner(f)
	var commentBuf []string
	braceDepth := 0
	var currentService *Service
	var currentMessage *Message
	var currentEnum *Enum

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Track brace depth.
		braceDepth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")

		// Collect comments.
		if m := reComment.FindStringSubmatch(trimmed); m != nil {
			commentBuf = append(commentBuf, m[1])
			continue
		}

		comment := strings.TrimSpace(strings.Join(commentBuf, " "))

		// Import (used to build the cross-module dependency graph).
		if m := reImport.FindStringSubmatch(trimmed); m != nil {
			mod.Imports = append(mod.Imports, m[1])
			commentBuf = nil
			continue
		}

		// Package.
		if m := rePackage.FindStringSubmatch(trimmed); m != nil {
			if mod.Package == "" {
				mod.Package = m[1]
			}
			commentBuf = nil
			continue
		}

		// Service declaration.
		if m := reService.FindStringSubmatch(trimmed); m != nil {
			svc := Service{Name: m[1], Comment: comment}
			mod.Services = append(mod.Services, svc)
			currentService = &mod.Services[len(mod.Services)-1]
			currentMessage = nil
			currentEnum = nil
			commentBuf = nil
			continue
		}

		// RPC declaration (inside service).
		if currentService != nil && braceDepth >= 1 {
			if m := reRPC.FindStringSubmatch(trimmed); m != nil {
				rpc := RPC{
					Name:         m[1],
					Request:      m[2],
					Response:     m[4],
					Comment:      comment,
					ServerStream: m[3] != "",
				}
				currentService.RPCs = append(currentService.RPCs, rpc)
				commentBuf = nil
				continue
			}
		}

		// Closing brace — exit current block.
		if trimmed == "}" {
			if braceDepth == 0 {
				currentService = nil
				currentMessage = nil
				currentEnum = nil
			}
			commentBuf = nil
			continue
		}

		// Message declaration (top-level only).
		if currentService == nil && currentMessage == nil && currentEnum == nil {
			if m := reMessage.FindStringSubmatch(trimmed); m != nil {
				msg := Message{Name: m[1], Comment: comment}
				mod.Messages = append(mod.Messages, msg)
				currentMessage = &mod.Messages[len(mod.Messages)-1]
				currentService = nil
				currentEnum = nil
				commentBuf = nil
				continue
			}
		}

		// Field declaration (inside message).
		if currentMessage != nil && braceDepth >= 1 {
			if m := reField.FindStringSubmatch(trimmed); m != nil {
				// Collect full field text (options may span multiple lines).
				fullField := line
				for !strings.Contains(fullField, ";") && scanner.Scan() {
					next := scanner.Text()
					fullField += " " + strings.TrimSpace(next)
					braceDepth += strings.Count(next, "{") - strings.Count(next, "}")
				}
				behavior := extractFieldBehavior(fullField)
				field := Field{
					Type:     simplifyType(m[1]),
					Name:     m[2],
					Comment:  comment,
					Required: behavior == "REQUIRED" || behavior == "IDENTIFIER",
					Repeated: strings.HasPrefix(trimmed, "repeated "),
					Behavior: behavior,
				}
				currentMessage.Fields = append(currentMessage.Fields, field)
				commentBuf = nil
				continue
			}
		}

		// Enum declaration (top-level only).
		if currentService == nil && currentMessage == nil && currentEnum == nil {
			if m := reEnum.FindStringSubmatch(trimmed); m != nil {
				en := Enum{Name: m[1], Comment: comment}
				mod.Enums = append(mod.Enums, en)
				currentEnum = &mod.Enums[len(mod.Enums)-1]
				currentService = nil
				currentMessage = nil
				commentBuf = nil
				continue
			}
		}

		// Enum value (inside enum).
		if currentEnum != nil && braceDepth >= 1 {
			if m := reEnumVal.FindStringSubmatch(trimmed); m != nil {
				val := EnumValue{Name: m[1], Number: m[2], Comment: comment}
				currentEnum.Values = append(currentEnum.Values, val)
				commentBuf = nil
				continue
			}
		}

		// Non-matching line — clear comment buffer.
		commentBuf = nil
	}

	return scanner.Err()
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"runtime/debug"
)

// Stamped at link time by Bazel with --config=release (tools/bazel/status.sh
// and the x_defs on //:freebusy): the release tag and the commit it was built
// from. Empty in an unstamped build.
var (
	buildVersion string
	buildCommit  string
)

// buildRevision is the commit the binary was built from: the stamp, else the
// VCS revision `go build` records, else "dev".
func buildRevision() string {
	if buildCommit != "" {
		return buildCommit
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "dev"
}

// versionString is what `freebusy --version` prints: the stamped release tag
// (or "dev") and the commit.
func versionString() string {
	version := buildVersion
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("freebusy %s (commit %s)", version, buildRevision())
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

// Meter lives apart from telemetry.go: the SDK's package and runtime-go's
// metrics contract are both named telemetry, so each file imports one.

import "github.com/the-protobuf-project/runtime-go/telemetry"

// Meter is the process pipeline's meter, or one that records nothing when
// there is no pipeline.
func Meter() telemetry.Meter {
	if t := Telemetry; t != nil {
		return t.Meter()
	}
	return telemetry.NoopMeter
}

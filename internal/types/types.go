// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package types holds the shared, provider-agnostic vocabulary for the freebusy
// API conventions (Google AIP): sentinel errors and offset pagination. Its
// error sentinels alias the generated repox ones, so hand-written and generated
// repositories are indistinguishable to the gRPC layer.
package types

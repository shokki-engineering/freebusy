// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

// log.go lives apart from opensearch.go, which imports runtime-go's metrics
// contract: freebusy's telemetry package has the same name.

import "github.com/shokki-engineering/freebusy/internal/telemetry"

// warnFallback logs a search served from Postgres because OpenSearch failed.
var warnFallback = telemetry.Warn

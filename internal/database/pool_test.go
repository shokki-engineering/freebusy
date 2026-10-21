// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"testing"
	"time"
)

// The monitor only applies to pooled providers: nil connections and the
// HTTP-backed Hasura provider must be silent no-ops, not panics.
func TestStartPoolMonitorNoOpWithoutPool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartPoolMonitor(ctx, nil, time.Millisecond)
	StartPoolMonitor(ctx, &Connection{Provider: ProviderHasura}, time.Millisecond)
	StartPoolMonitor(ctx, &Connection{Provider: ProviderGorm}, time.Millisecond) // gorm without a handle
}

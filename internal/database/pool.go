// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"time"

	"github.com/shokki-engineering/freebusy/shared"
)

// defaultPoolSampleInterval is how often the pool monitor samples sql.DB.Stats.
const defaultPoolSampleInterval = 30 * time.Second

// poolMetrics is one sample of the shared Postgres pool, emitted as deltas:
// gauges are OTel up-down counters recorded via Add, so each sample
// contributes the change since the previous one and the instrument tracks the
// level. WaitCount/WaitMs are cumulative in Stats and likewise emitted as the
// delta per interval; a rising wait rate means the pool cap is saturated and
// requests are queueing — the signal that traffic outgrew the bounds.
type poolMetrics struct {
	Open    int64 `opentelementry:"metric:gauge:db.pool.open"`
	InUse   int64 `opentelementry:"metric:gauge:db.pool.in_use"`
	Idle    int64 `opentelementry:"metric:gauge:db.pool.idle"`
	MaxOpen int64 `opentelementry:"metric:gauge:db.pool.max_open"`
	Waits   int64 `opentelementry:"metric:counter:db.pool.wait_count"`
	WaitMs  int64 `opentelementry:"metric:counter:db.pool.wait_ms"`
}

// StartPoolMonitor launches a background loop publishing the connection-pool
// health of the process's single shared Connection until ctx is cancelled. It
// is a no-op for providers without a pool (Hasura speaks HTTP). Non-blocking;
// the goroutine exits when ctx is done.
func StartPoolMonitor(ctx context.Context, conn *Connection, interval time.Duration) {
	if conn == nil || conn.Provider != ProviderGorm || conn.PgSQLConn == nil {
		return
	}
	sqlDB, err := conn.PgSQLConn.DB()
	if err != nil {
		_ = shared.Telemetry.Logger.Error("pool monitor: no pool handle", map[string]any{"error": err.Error()})
		return
	}
	if interval <= 0 {
		interval = defaultPoolSampleInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var last sql.DBStats
		sample(sqlDB, &last)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sample(sqlDB, &last)
			}
		}
	}()
}

// sample emits one delta sample against *last and advances it.
func sample(sqlDB *sql.DB, last *sql.DBStats) {
	s := sqlDB.Stats()
	m := poolMetrics{
		Open:    int64(s.OpenConnections - last.OpenConnections),
		InUse:   int64(s.InUse - last.InUse),
		Idle:    int64(s.Idle - last.Idle),
		MaxOpen: int64(s.MaxOpenConnections - last.MaxOpenConnections),
		Waits:   s.WaitCount - last.WaitCount,
		WaitMs:  (s.WaitDuration - last.WaitDuration).Milliseconds(),
	}
	*last = s
	if err := shared.Telemetry.Metrics.Record(m); err != nil {
		_ = shared.Telemetry.Logger.Error("pool monitor: record failed", map[string]any{"error": err.Error()})
	}
}

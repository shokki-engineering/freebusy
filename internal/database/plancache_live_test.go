// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database_test

import (
	"cmp"
	"context"
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shokki-engineering/freebusy/internal/config"
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/test/live"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Every pooled connection the server opens from [database.postgres] runs
// with plan_cache_mode = force_custom_plan, sent as a startup parameter, so
// no connection switches the read model's search to a generic plan after
// five runs; "database" leaves the server's own setting.
func TestLivePlanCacheMode(t *testing.T) {
	parsed, err := pgconn.ParseConfig(live.Env(t, live.DatabaseURL))
	if err != nil {
		t.Fatal(err)
	}
	pg := config.PostgresConfig{
		Host: parsed.Host, Port: int(parsed.Port), User: parsed.User, Password: parsed.Password,
		DBName: parsed.Database, SSLMode: "disable", TimeZone: "UTC", MaxOpenConns: 4,
	}
	if parsed.TLSConfig != nil {
		pg.SSLMode = "require"
	}
	for mode, want := range map[string]string{"": "force_custom_plan", "auto": "auto", config.PlanCacheModeDatabase: ""} {
		t.Run(cmp.Or(mode, "default"), func(t *testing.T) {
			pg.PlanCacheMode = mode
			db, err := database.OpenGorm(pg.DSN(), &gorm.Config{Logger: logger.Discard})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.BoundPool(db, pg.Pool()); err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			if want == "" {
				if err := sqlDB.QueryRow("SELECT reset_val FROM pg_settings WHERE name = 'plan_cache_mode'").Scan(&want); err != nil {
					t.Fatal(err)
				}
			}
			// Hold the whole pool at once so each SHOW runs on its own connection.
			ctx := context.Background()
			conns := make([]*sql.Conn, pg.MaxOpenConns)
			for i := range conns {
				if conns[i], err = sqlDB.Conn(ctx); err != nil {
					t.Fatal(err)
				}
				defer conns[i].Close()
			}
			for i, c := range conns {
				var got string
				if err := c.QueryRowContext(ctx, "SHOW plan_cache_mode").Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("connection %d: plan_cache_mode = %q, want %q", i, got, want)
				}
			}
		})
	}
}

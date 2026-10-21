// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// wrappingDialector is the Postgres dialector with one change: constraint
// errors are translated by wrapping, not replacing.
//
// With gorm.Config.TranslateError on, the stock dialector swaps a unique
// violation for the bare gorm.ErrDuplicatedKey. That is what the generated
// repox.MapGormErr matches to turn a taken name into AlreadyExists -- without
// it every duplicate in every GORM service surfaced as Internal -- but the
// swap also throws away the *pgconn.PgError, so the log no longer says which
// constraint fired, and a check or foreign-key violation degrades from
// `violates check constraint "chk_resource_state"` to `violates check
// constraint`. Wrapping keeps both: errors.Is finds the gorm sentinel,
// errors.As still finds the PgError, and the message carries its detail.
type wrappingDialector struct {
	*postgres.Dialector
}

// pgSentinels mirrors gorm.io/driver/postgres's own code table.
var pgSentinels = map[string]error{
	"23505": gorm.ErrDuplicatedKey,
	"23503": gorm.ErrForeignKeyViolated,
	"23514": gorm.ErrCheckConstraintViolated,
	"42703": gorm.ErrInvalidField,
}

// Translate implements gorm.ErrorTranslator.
func (wrappingDialector) Translate(err error) error {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}
	if sentinel, ok := pgSentinels[pg.Code]; ok {
		return fmt.Errorf("%w: %w", sentinel, err)
	}
	return err
}

// OpenGorm opens Postgres at dsn the way the server does: through
// wrappingDialector with TranslateError on. Anything that needs the server's
// error behaviour -- the live integration suites in particular -- opens its
// connection here rather than with a bare gorm.Open, which would surface
// duplicate keys as Internal again.
func OpenGorm(dsn string, cfg *gorm.Config) (*gorm.DB, error) {
	cfg.TranslateError = true
	return gorm.Open(wrappingDialector{postgres.Open(dsn).(*postgres.Dialector)}, cfg)
}

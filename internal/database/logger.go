// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// expectedLogger is GORM's default logger without the errors a booking engine
// expects: a missing row, a taken name, and the race for a time window being
// lost. repox.MapGormErr turns each into a not-found, already-exists or
// conflict outcome, and the statement's span records it, so logging it at
// ERROR as well made every contended hold look like a fault. Everything else
// -- other errors, slow queries -- still logs.
type expectedLogger struct {
	logger.Interface
}

// expectedStates are the SQLSTATEs repox.MapGormErr maps to a caller-facing
// outcome: unique and exclusion violations, deadlocks, serialization failures.
var expectedStates = map[string]bool{"23505": true, "23P01": true, "40P01": true, "40001": true}

// LogMode implements logger.Interface, keeping the filter at any level.
func (l expectedLogger) LogMode(level logger.LogLevel) logger.Interface {
	return expectedLogger{l.Interface.LogMode(level)}
}

// Trace implements logger.Interface.
func (l expectedLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if expected(err) {
		err = nil
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

func expected(err error) bool {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && expectedStates[pg.Code]
}

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// traced records the error each Trace call passes on.
type traced struct {
	logger.Interface
	errs []error
}

func (t *traced) Trace(_ context.Context, _ time.Time, _ func() (string, int64), err error) {
	t.errs = append(t.errs, err)
}

func TestExpectedLoggerDropsExpectedErrors(t *testing.T) {
	sql := func() (string, int64) { return "INSERT ...", 0 }
	for _, tc := range []struct {
		err  error
		kept bool
	}{
		{gorm.ErrRecordNotFound, false},
		{wrappingDialector{}.Translate(&pgconn.PgError{Code: "23505"}), false},
		{fmt.Errorf("insert hold: %w", &pgconn.PgError{Code: "23P01"}), false},
		{&pgconn.PgError{Code: "40P01"}, false},
		{&pgconn.PgError{Code: "23514"}, true},
		{errors.New("connection refused"), true},
	} {
		inner := &traced{Interface: logger.Discard}
		expectedLogger{inner}.Trace(context.Background(), time.Now(), sql, tc.err)
		if got := inner.errs[0] != nil; got != tc.kept {
			t.Errorf("Trace(%v) logged an error: %v, want %v", tc.err, got, tc.kept)
		}
	}
}

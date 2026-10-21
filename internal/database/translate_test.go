package database

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"gorm.io/gorm"
)

func TestTranslateWrapsAndKeepsDetail(t *testing.T) {
	raw := &pgconn.PgError{Code: "23505", Message: "duplicate key value", ConstraintName: "resource_pkey"}
	err := wrappingDialector{}.Translate(raw)
	if !errors.Is(err, gorm.ErrDuplicatedKey) || !errors.Is(repox.MapGormErr(err), repox.ErrAlreadyExists) {
		t.Fatalf("duplicate not recognised: %v", err)
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != "resource_pkey" {
		t.Fatal("the original PgError was lost")
	}
	check := wrappingDialector{}.Translate(&pgconn.PgError{Code: "23514", Message: "violates check constraint", ConstraintName: "chk_resource_state"})
	if !errors.Is(check, gorm.ErrCheckConstraintViolated) || !errors.As(check, &pg) || pg.ConstraintName != "chk_resource_state" {
		t.Fatalf("check violation: %v", check)
	}
	other := errors.New("connection reset")
	if (wrappingDialector{}).Translate(other) != other {
		t.Fatal("non-Postgres errors must pass through untouched")
	}
}

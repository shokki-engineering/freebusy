package e2e

import (
	"net/url"
	"testing"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/hasura/freebusyql"
	"github.com/oh-tarnished/freebusy/internal/livetest"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTestGorm gates on FREEBUSY_TEST_POSTGRES_DSN and opens the live Postgres
// exactly as the server does (database.OpenGorm), with silent logging, so
// repository error mapping behaves as in production.
func openTestGorm(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := livetest.Env(t, livetest.PostgresDSN)
	db, err := database.OpenGorm(dsn, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	return db
}

// connectTestGraphQL gates on FREEBUSY_TEST_GRAPHQL_URL and connects the typed
// client to the live DDN engine.
func connectTestGraphQL(t *testing.T) *freebusyql.Service {
	t.Helper()
	raw := livetest.Env(t, livetest.GraphQLURL)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	svc, err := freebusyql.Connect(u)
	if err != nil {
		t.Fatalf("connect %s: %v", raw, err)
	}
	return svc
}

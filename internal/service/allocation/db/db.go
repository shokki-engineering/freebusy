// Package db picks the availability engine's Store for the configured backend.
// It sits apart from package allocation so that the service depends only on the
// Store contract, and each adapter can import the service's types without a
// cycle.
package db

import (
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/db/gorm"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/db/hasura"
)

// New returns the Store for conn's provider.
func New(conn *database.Connection) allocation.Store {
	if conn.Provider == database.ProviderHasura {
		return hasura.New(conn.Hasura)
	}
	return gorm.New(conn.PgSQLConn)
}

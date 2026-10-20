// Package db picks ReservationService's Store for the configured backend.
package db

import (
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/internal/service/reservation/db/gorm"
	"github.com/oh-tarnished/freebusy/internal/service/reservation/db/hasura"
)

// New returns the Store for conn's provider.
func New(conn *database.Connection) reservation.Store {
	if conn.Provider == database.ProviderHasura {
		return hasura.New(conn.Hasura)
	}
	return gorm.New(conn.PgSQLConn)
}

package internal

import (
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/reservation"
	"github.com/oh-tarnished/freebusy/internal/service/reservation/db"
)

// reservationStore opens the reservation store on conn. It lives in a file of
// its own because the engine store's package is named db too.
func reservationStore(conn *database.Connection) reservation.Store { return db.New(conn) }

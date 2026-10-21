package internal

import (
	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/service/allocation"
	"github.com/oh-tarnished/freebusy/internal/service/allocation/db"
)

// allocationStore opens the availability engine's store on conn. It lives in
// a file of its own because the reservation store's package is named db too.
func allocationStore(conn *database.Connection) allocation.Store { return db.New(conn) }

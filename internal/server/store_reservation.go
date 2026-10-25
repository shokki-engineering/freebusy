// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/service/reservation"
	"github.com/shokki-engineering/freebusy/internal/service/reservation/db"
)

// reservationStore opens the reservation store on conn. It lives in a file of
// its own because the engine store's package is named db too.
func reservationStore(conn *database.Connection) reservation.Store { return db.New(conn) }

// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
)

// allocationStore opens the availability engine's store on conn. It lives in
// a file of its own because the reservation store's package is named db too.
func allocationStore(conn *database.Connection) allocation.Store { return db.New(conn) }

// readerOn is the engine's store reading through c alone: what the read
// model's writer reads its sources with, inside the write it projects.
func readerOn(c repox.Conn) allocation.Store { return db.On(c) }

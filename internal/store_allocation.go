// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package internal

import (
	"github.com/shokki-engineering/freebusy/internal/database"
	"github.com/shokki-engineering/freebusy/internal/service/allocation"
	"github.com/shokki-engineering/freebusy/internal/service/allocation/db"
)

// allocationStore opens the availability engine's store on conn. It lives in
// a file of its own because the reservation store's package is named db too.
func allocationStore(conn *database.Connection) allocation.Store { return db.New(conn) }

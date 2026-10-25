// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// A file of its own because the generated reservation repositories and the
// reservation service both declare package reservation.

import (
	"github.com/shokki-engineering/freebusy/internal/database/repository/freebusy/reservation"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
)

// referrerSources are the generated ReferrerSources of every schema with a
// reference field no foreign key covers -- today a reservation's customer.
func referrerSources(conn repox.Conn) []repox.ReferrerSource {
	return reservation.ReferrerSources(reservation.New(conn))
}

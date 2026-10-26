// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

// HoldName rebuilds a hold's name from the id a reservation row stores.
func HoldName(holdID string) string {
	if holdID == "" {
		return ""
	}
	return "holds/" + holdID
}

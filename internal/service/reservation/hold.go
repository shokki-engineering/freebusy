// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package reservation

import "strings"

// HoldName rebuilds a hold's name from the two parts a reservation row stores:
// its site and the hold's id.
func HoldName(siteID, holdID string) string {
	if siteID == "" || holdID == "" {
		return ""
	}
	return "organizationalUnits/" + siteID + "/holds/" + holdID
}

// SiteID is the organizational unit id in a reservation name,
// "organizationalUnits/{ou}/reservations/{id}".
func SiteID(name string) string {
	if parts := strings.Split(name, "/"); len(parts) == 4 {
		return parts[1]
	}
	return ""
}

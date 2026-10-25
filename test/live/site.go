// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package live

import (
	"testing"

	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/organization"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/rfc/orgunit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Site stores the site organizationalUnits/{id}, under an organization of its
// own, and returns its name. Holds, reservations and allocations carry a
// foreign key to their site, so a suite writing them seeds the site first.
func Site(t testing.TB, db *gorm.DB, id string) string {
	t.Helper()
	org := &organization.Organization{ID: id + "-org", Name: "organizations/" + id + "-org", DisplayNames: []string{id}}
	unit := &orgunit.OrganizationalUnit{ID: id, Name: "organizationalUnits/" + id, DisplayNames: []string{id}, Organization: &org.Name}
	for _, row := range []any{org, unit} {
		if err := db.Omit(clause.Associations).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			t.Fatalf("seed site %s: %v", id, err)
		}
	}
	return unit.Name
}

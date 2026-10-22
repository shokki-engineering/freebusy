// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/service/rfcsvc"
)

// purgeFlow pins that the purger's writes land on both backends: it stamps
// expire_time on a soft-deleted organization, and clears it once the
// organization is undeleted. The retention is long enough that the sweep
// purges nothing in the shared database.
func purgeFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()
	org, err := c.organizations.CreateOrganization(ctx, &organizationv1.CreateOrganizationRequest{
		OrganizationId: "purge-" + c.suffix,
		Organization:   &organizationv1.Organization{DisplayNames: []string{"e2e purge"}},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Organizations.Purge, org.GetName()) })
	if _, err := c.organizations.DeleteOrganization(ctx, &organizationv1.DeleteOrganizationRequest{Name: org.GetName()}); err != nil {
		t.Fatalf("delete organization: %v", err)
	}

	const retention = 1000 * 24 * time.Hour
	purger := rfcsvc.NewPurger(c.repos, retention)
	stored := func() *organizationv1.Organization {
		t.Helper()
		rows, _, err := c.repos.Organizations.List(ctx, repox.ListInput{PageSize: 1, ShowDeleted: true,
			Where: []filterx.Condition{{Field: "name", Op: filterx.OpEq, Value: org.GetName()}}})
		if err != nil || len(rows) != 1 {
			t.Fatalf("read %s back: %v (%d rows)", org.GetName(), err, len(rows))
		}
		return rows[0]
	}

	if _, err := purger.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	got := stored()
	if got.GetDeleteTime() == nil || got.GetExpireTime() == nil {
		t.Fatalf("deleted organization after a sweep = %v, want expire_time stamped", got)
	}
	if want := got.GetDeleteTime().AsTime().Add(retention); !got.GetExpireTime().AsTime().Equal(want) {
		t.Errorf("expire_time = %v, want delete_time + retention = %v", got.GetExpireTime().AsTime(), want)
	}

	if _, err := c.organizations.UndeleteOrganization(ctx, &organizationv1.UndeleteOrganizationRequest{Name: org.GetName()}); err != nil {
		t.Fatalf("undelete organization: %v", err)
	}
	if _, err := purger.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if got := stored(); got.GetDeleteTime() != nil || got.GetExpireTime() != nil {
		t.Errorf("undeleted organization after a sweep = %v, want live with no expire_time", got)
	}
}

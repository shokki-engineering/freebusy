// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/allocation/v1/allocationpbv1"
	"github.com/shokki-engineering/freebusy/protobuf/generated/go/reservation/v1/reservationpbv1"
	"google.golang.org/genproto/googleapis/type/interval"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// site creates an organization and a site of it, named id, over the wire, and
// purges both when the test ends. Nothing is served at a site that does not
// exist, so every flow booking anything starts here.
func (c *e2eClients) site(t *testing.T, id string) string {
	t.Helper()
	ctx := context.Background()
	org, err := c.organizations.CreateOrganization(ctx, &organizationv1.CreateOrganizationRequest{
		OrganizationId: id + "-org",
		Organization:   &organizationv1.Organization{DisplayNames: []string{"e2e " + id}},
	})
	if err != nil {
		t.Fatalf("create organization: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.Organizations.Purge, org.GetName()) })
	ou, err := c.units.CreateOrganizationalUnit(ctx, &organizational_unitv1.CreateOrganizationalUnitRequest{
		OrganizationalUnitId: id,
		OrganizationalUnit: &organizational_unitv1.OrganizationalUnit{
			DisplayNames: []string{"e2e " + id},
			Organization: org.GetName(),
		},
	})
	if err != nil {
		t.Fatalf("create organizational unit: %v", err)
	}
	t.Cleanup(func() { c.purge(t, c.repos.OrganizationalUnits.Purge, ou.GetName()) })
	return ou.GetName()
}

// tenancyFlow pins the tenancy rule end to end: a site needs a live
// organization, nothing is served at a site that is missing or deleted, an
// organization with live sites cannot be deleted, and Undelete brings it all
// back.
func tenancyFlow(t *testing.T, c *e2eClients) {
	t.Helper()
	ctx := context.Background()
	id := "ten-" + c.suffix
	start := time.Now().UTC().AddDate(0, 0, 50).Truncate(time.Hour)
	window := &interval.Interval{StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour))}
	check := func(site string) error {
		_, err := c.availability.CheckAvailability(ctx, &allocationpbv1.CheckAvailabilityRequest{Parent: site, Window: window})
		return err
	}

	// No site yet: nothing answers there.
	wantCode(t, check("organizationalUnits/"+id), codes.NotFound, "a site nobody created")
	_, err := c.reservations.ListReservations(ctx, &reservationpbv1.ListReservationsRequest{Parent: "organizationalUnits/" + id})
	wantCode(t, err, codes.NotFound, "listing at a site nobody created")

	// A site needs an organization, and a live one.
	_, err = c.units.CreateOrganizationalUnit(ctx, &organizational_unitv1.CreateOrganizationalUnitRequest{
		OrganizationalUnitId: id + "-orphan",
		OrganizationalUnit:   &organizational_unitv1.OrganizationalUnit{DisplayNames: []string{"orphan"}},
	})
	wantCode(t, err, codes.InvalidArgument, "a site with no organization")
	// No organization yet: told to create one, not merely that the field is required.
	wantHint(t, err, "Create the organization first with CreateOrganization (POST /v1/organizations)")
	_, err = c.units.CreateOrganizationalUnit(ctx, &organizational_unitv1.CreateOrganizationalUnitRequest{
		OrganizationalUnitId: id + "-orphan",
		OrganizationalUnit:   &organizational_unitv1.OrganizationalUnit{DisplayNames: []string{"orphan"}, Organization: "organizations/" + id + "-nobody"},
	})
	wantCode(t, err, codes.InvalidArgument, "a site of an organization nobody created")
	// An organization named but not there: told it was not found, and where to look.
	wantHint(t, err, `organization "organizations/`+id+`-nobody" does not exist`)
	wantHint(t, err, "ListOrganizations (GET /v1/organizations)")

	// A resource needs a site.
	_, err = c.resources.CreateResource(ctx, &resourcev1.CreateResourceRequest{Resource: &resourcev1.Resource{}})
	wantCode(t, err, codes.InvalidArgument, "a resource at no site")

	site := c.site(t, id)
	org := "organizations/" + id + "-org"
	if err := check(site); err != nil {
		t.Fatalf("a live site: %v", err)
	}
	if _, err := c.availability.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Parent: org, Window: window}); err != nil {
		t.Fatalf("searching a live organization: %v", err)
	}

	// An organization with a live site is not deleted from under it.
	_, err = c.organizations.DeleteOrganization(ctx, &organizationv1.DeleteOrganizationRequest{Name: org})
	wantCode(t, err, codes.FailedPrecondition, "deleting an organization with a live site")

	// A deleted site answers nothing; restored, it answers again.
	if _, err := c.units.DeleteOrganizationalUnit(ctx, &organizational_unitv1.DeleteOrganizationalUnitRequest{Name: site}); err != nil {
		t.Fatalf("delete site: %v", err)
	}
	wantCode(t, check(site), codes.NotFound, "a deleted site")
	if got, err := c.units.GetOrganizationalUnit(ctx, &organizational_unitv1.GetOrganizationalUnitRequest{Name: site}); err != nil || got.GetDeleteTime() == nil {
		t.Fatalf("a deleted site still reads, marked deleted: %v, %v", got, err)
	}

	// With its last site gone the organization can go; its sites go with it.
	if _, err := c.organizations.DeleteOrganization(ctx, &organizationv1.DeleteOrganizationRequest{Name: org}); err != nil {
		t.Fatalf("delete organization: %v", err)
	}
	_, err = c.availability.SearchAvailability(ctx, &allocationpbv1.SearchAvailabilityRequest{Parent: org, Window: window})
	wantCode(t, err, codes.NotFound, "searching a deleted organization")
	_, err = c.units.UndeleteOrganizationalUnit(ctx, &organizational_unitv1.UndeleteOrganizationalUnitRequest{Name: site})
	wantCode(t, err, codes.FailedPrecondition, "restoring a site of a deleted organization")

	if _, err := c.organizations.UndeleteOrganization(ctx, &organizationv1.UndeleteOrganizationRequest{Name: org}); err != nil {
		t.Fatalf("undelete organization: %v", err)
	}
	if _, err := c.units.UndeleteOrganizationalUnit(ctx, &organizational_unitv1.UndeleteOrganizationalUnitRequest{Name: site}); err != nil {
		t.Fatalf("undelete site: %v", err)
	}
	if err := check(site); err != nil {
		t.Fatalf("a restored site: %v", err)
	}
}

// wantHint asserts err's message carries what to do.
func wantHint(t *testing.T, err error, want string) {
	t.Helper()
	if msg := status.Convert(err).Message(); !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
		t.Fatalf("message %q does not say %q", msg, want)
	}
}

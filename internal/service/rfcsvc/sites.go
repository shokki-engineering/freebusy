// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

// sites.go is the tenancy rule: everything freebusy books belongs to an
// organizational unit -- a site -- and every site to an organization. A call
// naming a site that does not exist, or that is soft-deleted, or whose
// organization is either, is answered as if the site were not there.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/types"
)

const (
	organizationsCollection       = "organizations"
	organizationalUnitsCollection = "organizationalUnits"
)

// Sites answers whether an organization or a site is live.
type Sites struct {
	repos Repositories
}

// NewSites returns the checker over repos.
func NewSites(repos Repositories) *Sites {
	return &Sites{repos: repos}
}

// Organization fails unless name is a live organization: Missing when there
// is none by that name, Blocked when it is soft-deleted (AIP-164 has a deleted
// organization answer only Get, List with show_deleted, and Undelete).
func (s *Sites) Organization(ctx context.Context, name string) error {
	org, err := s.repos.Organizations.Get(ctx, name)
	switch {
	case errors.Is(err, types.ErrNotFound):
		return types.Missing("ORGANIZATION_NOT_FOUND", hints.TypeOf(name), name,
			fmt.Sprintf("organization %q does not exist", name),
			"Check the name -- it is the full name CreateOrganization returned, like organizations/acme -- "+
				"or list organizations with "+hints.Call("ListOrganizations", nil)+
				"; to start from nothing, create one with "+hints.Call("CreateOrganization", nil))
	case err != nil:
		return err
	case org.GetDeleteTime() != nil:
		return types.Missing("ORGANIZATION_DELETED", hints.TypeOf(name), name,
			fmt.Sprintf("organization %q is deleted", name),
			"Restore it with "+hints.Call("UndeleteOrganization", map[string]string{"name": name})+
				", or use a live organization")
	}
	return nil
}

// Site fails unless name is a live organizational unit of a live
// organization.
func (s *Sites) Site(ctx context.Context, name string) error {
	ou, err := s.repos.OrganizationalUnits.Get(ctx, name)
	switch {
	case errors.Is(err, types.ErrNotFound):
		return types.Missing("SITE_NOT_FOUND", hints.TypeOf(name), name,
			fmt.Sprintf("organizational unit %q does not exist", name),
			"Check the name, or list sites with "+hints.Call("ListOrganizationalUnits", nil)+
				"; to add one, create it under an organization with "+hints.Call("CreateOrganizationalUnit", nil))
	case err != nil:
		return err
	case ou.GetDeleteTime() != nil:
		return types.Missing("SITE_DELETED", hints.TypeOf(name), name,
			fmt.Sprintf("organizational unit %q is deleted", name),
			"Restore it with "+hints.Call("UndeleteOrganizationalUnit", map[string]string{"name": name})+
				", or use a live site")
	case ou.GetOrganization() == "":
		return types.Blocked("SITE_WITHOUT_ORGANIZATION", hints.TypeOf(name), name,
			fmt.Sprintf("organizational unit %q belongs to no organization, and nothing is served at a site without one", name),
			"Set its organization with "+hints.Call("UpdateOrganizationalUnit", map[string]string{"organizational_unit.name": name})+
				" and update_mask=organization")
	}
	if err := s.Organization(ctx, ou.GetOrganization()); err != nil {
		var p *types.Problem
		if errors.As(err, &p) {
			// The site exists; its organization is what is missing. Say so,
			// or the caller goes looking for a site that is right there.
			p.What = fmt.Sprintf("organizational unit %q belongs to %s", name, p.What)
		}
		return err
	}
	return nil
}

// inField restates a failed check of a name the caller sent in a request
// field as a problem with that field: the request is what needs fixing, so it
// is INVALID_ARGUMENT with a BadRequest violation naming the field, whatever
// the name pointed at.
func inField(err error, field string) error {
	var p *types.Problem
	if !errors.As(err, &p) {
		return err
	}
	return &types.Problem{
		Kind: types.ErrInvalidArgument, Reason: p.Reason, Field: field,
		What: p.What, Fix: p.Fix, ResourceType: p.ResourceType, ResourceName: p.ResourceName,
	}
}

// checkName refuses a name that is not shaped like collection/id: the
// caller sent an id, or a URL, where a resource name was wanted.
func checkName(field, name, collection, example string) error {
	if c, id, ok := head(name); ok && c == collection && id != "" && strings.Count(name, "/") == 1 {
		return nil
	}
	return types.BadField("MALFORMED_NAME", field,
		fmt.Sprintf("%s must be a resource name like %s/{id}; got %q", field, collection, name),
		"Send the full name the create call returned, e.g. "+example)
}

// Named checks the organization or site at the head of a resource name or
// parent -- "organizationalUnits/x/holds/y" checks site x -- and accepts any
// other name. An AIP-159 wildcard ("organizationalUnits/-") names every site
// rather than one, so there is nothing to check.
func (s *Sites) Named(ctx context.Context, name string) error {
	collection, id, ok := head(name)
	if !ok || id == "-" {
		return nil
	}
	switch collection {
	case organizationsCollection:
		return s.Organization(ctx, collection+"/"+id)
	case organizationalUnitsCollection:
		return s.Site(ctx, collection+"/"+id)
	}
	return nil
}

// head splits the first collection/id pair off a resource name.
func head(name string) (collection, id string, ok bool) {
	collection, rest, ok := strings.Cut(name, "/")
	if !ok || collection == "" {
		return "", "", false
	}
	id, _, _ = strings.Cut(rest, "/")
	return collection, id, id != ""
}

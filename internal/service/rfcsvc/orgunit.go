// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

import (
	"context"
	"fmt"
	"slices"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1grpc"
	organizational_unitv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organizational_unit/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/emptypb"
)

// OrganizationalUnitServer serves RFC 4519 organizational units: the sites
// resources are booked at. A site must belong to a live organization -- RFC
// 4519 leaves the reference optional, freebusy does not -- and everything
// under organizationalUnits/{site} is refused while the site, or its
// organization, is missing or deleted (see Sites).
type OrganizationalUnitServer struct {
	organizational_unitv1grpc.UnimplementedOrganizationalUnitsServer
	repos Repositories
	sites *Sites
}

// NewOrganizationalUnitServer returns an OrganizationalUnitServer over repos.
func NewOrganizationalUnitServer(repos Repositories) *OrganizationalUnitServer {
	return &OrganizationalUnitServer{repos: repos, sites: NewSites(repos)}
}

const organizationalUnitService = "OrganizationalUnits"

func (s *OrganizationalUnitServer) GetOrganizationalUnit(ctx context.Context, req *organizational_unitv1.GetOrganizationalUnitRequest) (*organizational_unitv1.OrganizationalUnit, error) {
	var out *organizational_unitv1.OrganizationalUnit
	err := rpc.Traced(ctx, organizationalUnitService, "GetOrganizationalUnit", func(ctx context.Context) (err error) {
		out, err = s.repos.OrganizationalUnits.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationalUnitServer) ListOrganizationalUnits(ctx context.Context, req *organizational_unitv1.ListOrganizationalUnitsRequest) (*organizational_unitv1.ListOrganizationalUnitsResponse, error) {
	out := &organizational_unitv1.ListOrganizationalUnitsResponse{}
	err := rpc.Traced(ctx, organizationalUnitService, "ListOrganizationalUnits", func(ctx context.Context) (err error) {
		out.OrganizationalUnits, out.NextPageToken, err = s.repos.OrganizationalUnits.List(ctx,
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationalUnitServer) CreateOrganizationalUnit(ctx context.Context, req *organizational_unitv1.CreateOrganizationalUnitRequest) (*organizational_unitv1.OrganizationalUnit, error) {
	var out *organizational_unitv1.OrganizationalUnit
	err := rpc.Traced(ctx, organizationalUnitService, "CreateOrganizationalUnit", func(ctx context.Context) error {
		if err := checkID("organizational_unit_id", req.GetOrganizationalUnitId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetOrganizationalUnit()
		if err := s.belongs(ctx, in.GetOrganization()); err != nil {
			return rpc.ToStatusErr(err)
		}
		name := ""
		if id := req.GetOrganizationalUnitId(); id != "" {
			name = organizationalUnitsCollection + "/" + id
		}
		if err := PrepareCreate(in, name); err != nil {
			return rpc.ToStatusErr(err)
		}
		var err error
		out, err = s.repos.OrganizationalUnits.Create(ctx, in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationalUnitServer) UpdateOrganizationalUnit(ctx context.Context, req *organizational_unitv1.UpdateOrganizationalUnitRequest) (*organizational_unitv1.OrganizationalUnit, error) {
	var out *organizational_unitv1.OrganizationalUnit
	err := rpc.Traced(ctx, organizationalUnitService, "UpdateOrganizationalUnit", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetOrganizationalUnit(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		// An empty mask replaces every mutable field, organization included.
		if len(paths) == 0 || slices.Contains(paths, "organization") {
			if err := s.belongs(ctx, req.GetOrganizationalUnit().GetOrganization()); err != nil {
				return rpc.ToStatusErr(err)
			}
		}
		out, err = s.repos.OrganizationalUnits.Update(ctx, req.GetOrganizationalUnit(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// DeleteOrganizationalUnit soft-deletes a site that has no live resources.
// As with organizations, there is no force flag, so a site still holding
// resources is refused rather than emptied.
func (s *OrganizationalUnitServer) DeleteOrganizationalUnit(ctx context.Context, req *organizational_unitv1.DeleteOrganizationalUnitRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, organizationalUnitService, "DeleteOrganizationalUnit", func(ctx context.Context) error {
		held, _, err := s.repos.Resources.List(ctx, repox.ListInput{
			PageSize: 1,
			Where:    []filterx.Condition{{Field: "organizational_unit", Op: filterx.OpEq, Value: req.GetName()}},
		})
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if len(held) > 0 {
			return rpc.ToStatusErr(types.Blocked("SITE_HAS_RESOURCES", hints.TypeOf(req.GetName()), req.GetName(),
				fmt.Sprintf("organizational unit %q still has resources, %s among them, and deleting it would leave them unbookable", req.GetName(), held[0].GetName()),
				"Delete each with "+hints.Call("DeleteResource", map[string]string{"name": held[0].GetName()})+
					", or move it to another site with "+hints.Call("UpdateResource", map[string]string{"resource.name": held[0].GetName()})+
					"; list them with "+hints.Call("ListResources", nil)+" and filter organizational_unit = \""+req.GetName()+"\""))
		}
		return rpc.ToStatusErr(s.repos.OrganizationalUnits.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// UndeleteOrganizationalUnit restores a site. Its organization must be live:
// a site restored under a deleted organization would still answer nothing.
func (s *OrganizationalUnitServer) UndeleteOrganizationalUnit(ctx context.Context, req *organizational_unitv1.UndeleteOrganizationalUnitRequest) (*organizational_unitv1.OrganizationalUnit, error) {
	var out *organizational_unitv1.OrganizationalUnit
	err := rpc.Traced(ctx, organizationalUnitService, "UndeleteOrganizationalUnit", func(ctx context.Context) error {
		ou, err := s.repos.OrganizationalUnits.Get(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if org := ou.GetOrganization(); org != "" {
			if err := s.sites.Organization(ctx, org); err != nil {
				return rpc.ToStatusErr(types.Blocked("ORGANIZATION_DELETED", hints.TypeOf(req.GetName()), req.GetName(),
					fmt.Sprintf("organizational unit %s belongs to %s, which is not live, and a restored site would still answer nothing", req.GetName(), org),
					"Restore the organization first with "+hints.Call("UndeleteOrganization", map[string]string{"name": org})+
						", or move the site to a live organization once restored"))
			}
		}
		out, err = s.repos.OrganizationalUnits.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// belongs checks the organization a site names: required, shaped like an
// organization name, and live. Each failure says what to do, so a caller who
// has no organization yet is told to create one, and one who named it wrong is
// told the name was not found -- not merely that the field is required.
func (s *OrganizationalUnitServer) belongs(ctx context.Context, org string) error {
	const field = "organizational_unit.organization"
	if org == "" {
		return types.BadField("ORGANIZATION_REQUIRED", field,
			"an organizational unit belongs to an organization, and this request names none",
			"Create the organization first with "+hints.Call("CreateOrganization", nil)+
				", then send its name here, e.g. organization: \"organizations/acme\"; "+
				"to use one that exists, list them with "+hints.Call("ListOrganizations", nil))
	}
	if err := checkName(field, org, organizationsCollection, "organizations/acme"); err != nil {
		return err
	}
	return inField(s.sites.Organization(ctx, org), field)
}

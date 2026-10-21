// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

import (
	"context"
	"fmt"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organization/v1/organizationv1grpc"
	organizationv1 "buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc4519/organization/v1"
	"github.com/shokki-engineering/freebusy/internal/database/gorm/filterx"
	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
	"github.com/shokki-engineering/freebusy/internal/runtime/hints"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"github.com/shokki-engineering/freebusy/internal/types"
	"google.golang.org/protobuf/types/known/emptypb"
)

// OrganizationServer serves RFC 4519 organizations: the tenants everything
// else belongs to. Every site names one, and nothing is served at a site whose
// organization is missing or deleted (see Sites).
//
// Delete is a soft delete (AIP-164): the organization keeps its row with a
// delete_time, its sites stop answering, and Undelete brings both back.
type OrganizationServer struct {
	organizationv1grpc.UnimplementedOrganizationsServer
	repos Repositories
}

// NewOrganizationServer returns an OrganizationServer over repos.
func NewOrganizationServer(repos Repositories) *OrganizationServer {
	return &OrganizationServer{repos: repos}
}

const organizationService = "Organizations"

func (s *OrganizationServer) GetOrganization(ctx context.Context, req *organizationv1.GetOrganizationRequest) (*organizationv1.Organization, error) {
	var out *organizationv1.Organization
	err := rpc.Traced(ctx, organizationService, "GetOrganization", func(ctx context.Context) (err error) {
		out, err = s.repos.Organizations.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationServer) ListOrganizations(ctx context.Context, req *organizationv1.ListOrganizationsRequest) (*organizationv1.ListOrganizationsResponse, error) {
	out := &organizationv1.ListOrganizationsResponse{}
	err := rpc.Traced(ctx, organizationService, "ListOrganizations", func(ctx context.Context) (err error) {
		out.Organizations, out.NextPageToken, err = s.repos.Organizations.List(ctx,
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationServer) CreateOrganization(ctx context.Context, req *organizationv1.CreateOrganizationRequest) (*organizationv1.Organization, error) {
	var out *organizationv1.Organization
	err := rpc.Traced(ctx, organizationService, "CreateOrganization", func(ctx context.Context) error {
		if err := checkID("organization_id", req.GetOrganizationId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetOrganization()
		name := ""
		if id := req.GetOrganizationId(); id != "" {
			name = organizationsCollection + "/" + id
		}
		if err := PrepareCreate(in, name); err != nil {
			return rpc.ToStatusErr(err)
		}
		var err error
		out, err = s.repos.Organizations.Create(ctx, in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *OrganizationServer) UpdateOrganization(ctx context.Context, req *organizationv1.UpdateOrganizationRequest) (*organizationv1.Organization, error) {
	var out *organizationv1.Organization
	err := rpc.Traced(ctx, organizationService, "UpdateOrganization", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetOrganization(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Organizations.Update(ctx, req.GetOrganization(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

// DeleteOrganization soft-deletes an organization that has no live sites.
// The request carries no force flag, so AIP-135 has it refuse rather than take
// the sites with it: delete them, or move them to another organization, first.
func (s *OrganizationServer) DeleteOrganization(ctx context.Context, req *organizationv1.DeleteOrganizationRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, organizationService, "DeleteOrganization", func(ctx context.Context) error {
		sites, _, err := s.repos.OrganizationalUnits.List(ctx, repox.ListInput{
			PageSize: 1,
			Where:    []filterx.Condition{{Field: "organization", Op: filterx.OpEq, Value: req.GetName()}},
		})
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		if len(sites) > 0 {
			return rpc.ToStatusErr(types.Blocked("ORGANIZATION_HAS_SITES", hints.TypeOf(req.GetName()), req.GetName(),
				fmt.Sprintf("organization %q still has organizational units, %s among them, and deleting it would leave them serving nothing", req.GetName(), sites[0].GetName()),
				"Delete each with "+hints.Call("DeleteOrganizationalUnit", map[string]string{"name": sites[0].GetName()})+
					", or move it to another organization with "+hints.Call("UpdateOrganizationalUnit", map[string]string{"organizational_unit.name": sites[0].GetName()})+
					"; list them with "+hints.Call("ListOrganizationalUnits", nil)+" and filter organization = \""+req.GetName()+"\""))
		}
		return rpc.ToStatusErr(s.repos.Organizations.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *OrganizationServer) UndeleteOrganization(ctx context.Context, req *organizationv1.UndeleteOrganizationRequest) (*organizationv1.Organization, error) {
	var out *organizationv1.Organization
	err := rpc.Traced(ctx, organizationService, "UndeleteOrganization", func(ctx context.Context) (err error) {
		out, err = s.repos.Organizations.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

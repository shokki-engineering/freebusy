// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package rfcsvc

import (
	"context"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc9073/resource/v1/resourcev1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/protocolbuffers/go/protobuf/rfc9073/resource/v1"
	"github.com/shokki-engineering/freebusy/internal/runtime/rpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ResourceServer serves RFC 9073 resources: the bookable things themselves.
// A soft-deleted resource stays visible to the engine, which counts it but
// never offers it, so in-flight reservations on it still resolve.
type ResourceServer struct {
	resourcev1grpc.UnimplementedResourcesServer
	repos Repositories
}

// NewResourceServer returns a ResourceServer over repos.
func NewResourceServer(repos Repositories) *ResourceServer {
	return &ResourceServer{repos: repos}
}

const resourceService = "Resources"

func (s *ResourceServer) GetResource(ctx context.Context, req *resourcev1.GetResourceRequest) (*resourcev1.Resource, error) {
	var out *resourcev1.Resource
	err := rpc.Traced(ctx, resourceService, "GetResource", func(ctx context.Context) (err error) {
		out, err = s.repos.Resources.Get(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *ResourceServer) ListResources(ctx context.Context, req *resourcev1.ListResourcesRequest) (*resourcev1.ListResourcesResponse, error) {
	out := &resourcev1.ListResourcesResponse{}
	err := rpc.Traced(ctx, resourceService, "ListResources", func(ctx context.Context) (err error) {
		out.Resources, out.NextPageToken, err = s.repos.Resources.List(ctx,
			listInput(req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetShowDeleted()))
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *ResourceServer) CreateResource(ctx context.Context, req *resourcev1.CreateResourceRequest) (*resourcev1.Resource, error) {
	var out *resourcev1.Resource
	err := rpc.Traced(ctx, resourceService, "CreateResource", func(ctx context.Context) error {
		if err := checkID("resource_id", req.GetResourceId()); err != nil {
			return rpc.ToStatusErr(err)
		}
		in := req.GetResource()
		name := ""
		if id := req.GetResourceId(); id != "" {
			name = "resources/" + id
		}
		PrepareCreate(in, name)
		var err error
		out, err = s.repos.Resources.Create(ctx, in)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *ResourceServer) UpdateResource(ctx context.Context, req *resourcev1.UpdateResourceRequest) (*resourcev1.Resource, error) {
	var out *resourcev1.Resource
	err := rpc.Traced(ctx, resourceService, "UpdateResource", func(ctx context.Context) error {
		paths, err := updatePaths(req.GetResource(), req.GetUpdateMask())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out, err = s.repos.Resources.Update(ctx, req.GetResource(), paths)
		return rpc.ToStatusErr(err)
	})
	return out, err
}

func (s *ResourceServer) DeleteResource(ctx context.Context, req *resourcev1.DeleteResourceRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, resourceService, "DeleteResource", func(ctx context.Context) error {
		return rpc.ToStatusErr(s.repos.Resources.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *ResourceServer) UndeleteResource(ctx context.Context, req *resourcev1.UndeleteResourceRequest) (*resourcev1.Resource, error) {
	var out *resourcev1.Resource
	err := rpc.Traced(ctx, resourceService, "UndeleteResource", func(ctx context.Context) (err error) {
		out, err = s.repos.Resources.Undelete(ctx, req.GetName())
		return rpc.ToStatusErr(err)
	})
	return out, err
}

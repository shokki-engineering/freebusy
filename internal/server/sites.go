// Copyright 2026 Srikanth K.
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

// sites.go: refuses a call that names a site or an organization that is
// not live, before its handler runs.
//
// Everything freebusy books sits at an organizational unit of an organization:
// holds, allocations and reservations under organizationalUnits/{site},
// availability searched across organizations/{organization}, a resource at the
// site it names. A call addressing a site that does not exist -- or that is
// soft-deleted, or whose organization is either -- is answered NOT_FOUND
// rather than served against a tenant nobody created.
//
// It runs as a unary interceptor so the rule holds on every surface at once:
// gRPC, the HTTP gateway (which dials gRPC) and MCP (which runs the same
// chain; see assembly.go). The Organizations and OrganizationalUnits services are the way to
// create and restore what it checks, so they are left to their own rules.

import (
	"context"

	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organization/v1/organizationv1grpc"
	"buf.build/gen/go/the-protobuf-project/rfc/grpc/go/protobuf/rfc4519/organizational_unit/v1/organizational_unitv1grpc"
	"github.com/shokki-engineering/freebusy/internal/service/directory"
	"github.com/shokki-engineering/freebusy/internal/service/rpc"
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// exempt are the services whose calls are not checked.
var exempt = map[string]bool{
	organizationv1grpc.Organizations_ServiceDesc.ServiceName:              true,
	organizational_unitv1grpc.OrganizationalUnits_ServiceDesc.ServiceName: true,
}

// sitesInterceptor checks every site and organization a request names with s.
func sitesInterceptor(s *directory.Sites) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if exempt[serviceOf(info.FullMethod)] {
			return handler(ctx, req)
		}
		if m, ok := req.(proto.Message); ok {
			for _, name := range siteNames(m) {
				if err := s.Named(ctx, name); err != nil {
					return nil, rpc.ToStatusErr(err)
				}
			}
		}
		return handler(ctx, req)
	}
}

// serviceOf is the service of a full method name, "/pkg.Service/Method".
func serviceOf(fullMethod string) string {
	for i := len(fullMethod) - 1; i > 0; i-- {
		if fullMethod[i] == '/' {
			return fullMethod[1:i]
		}
	}
	return ""
}

// addressing are the fields that name a resource by convention: AIP-122's
// name, AIP-132/133's parent, and the RFC 4519 references to a site or an
// organization. A field carrying (google.api.resource_reference) counts too.
var addressing = map[protoreflect.Name]bool{
	"name":                 true,
	"parent":               true,
	"organization":         true,
	"organizations":        true,
	"organizational_unit":  true,
	"organizational_units": true,
}

// siteNames returns every distinct resource name m addresses: its parent or name,
// a batch's per-entry parents, the sites a search is narrowed to. It does not
// look inside the resource a create or update carries -- the site a new
// resource names is the service's to check, field by field -- and it never
// reads free text, so a description mentioning "organizations/x" checks
// nothing.
func siteNames(m proto.Message) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	var walk func(protoreflect.Message, int)
	walk = func(r protoreflect.Message, depth int) {
		if depth > 8 {
			return
		}
		r.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
				switch {
				case isResource(fd.Message()):
					// The resource being written. What it references -- the
					// site a resource belongs to -- is the service's to
					// check, and it reports the field that is wrong; this
					// guard is about what the call itself addresses.
				case fd.IsMap():
				case fd.IsList():
					for i := 0; i < v.List().Len(); i++ {
						walk(v.List().Get(i).Message(), depth+1)
					}
				default:
					walk(v.Message(), depth+1)
				}
			case fd.Kind() == protoreflect.StringKind && addresses(fd):
				if fd.IsList() {
					for i := 0; i < v.List().Len(); i++ {
						add(v.List().Get(i).String())
					}
				} else if !fd.IsMap() {
					add(v.String())
				}
			}
			return true
		})
	}
	walk(m.ProtoReflect(), 0)
	return out
}

func isResource(md protoreflect.MessageDescriptor) bool {
	res, _ := proto.GetExtension(md.Options(), annotations.E_Resource).(*annotations.ResourceDescriptor)
	return res != nil
}

func addresses(fd protoreflect.FieldDescriptor) bool {
	if addressing[fd.Name()] {
		return true
	}
	ref, _ := proto.GetExtension(fd.Options(), annotations.E_ResourceReference).(*annotations.ResourceReference)
	return ref != nil && (ref.GetType() != "" || ref.GetChildType() != "")
}

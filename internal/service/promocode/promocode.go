// Package promocode is the gRPC/protobuf layer for the PromoCode service: it
// implements promocodepbv1.PromoCodeServiceServer, owning request validation,
// observability, and the mapping of repository errors to gRPC status codes. All
// protobuf concerns live here; persistence stays behind the provider-agnostic
// db.PromoCodeRepository, so the database layer is agnostic to protobuf and gRPC.
package promocode

import (
	"context"
	"errors"
	"github.com/oh-tarnished/freebusy/internal/database"

	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/runtime/rpc"
	"github.com/oh-tarnished/freebusy/internal/service/promocode/db"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements promocodepbv1.PromoCodeServiceServer on top of a
// provider-agnostic db.PromoCodeRepository, which is selected (GORM or Hasura) by
// the promocode db factory.
type Server struct {
	promocodepbv1.UnimplementedPromoCodeServiceServer
	repo db.PromoCodeRepository
}

// New builds the promocode service on conn: the provider-selected repository
// wrapped in the gRPC server implementation.
func New(conn *database.Connection) *Server {
	return NewServer(db.New(conn))
}

// NewServer returns a Server backed by repo.
func NewServer(repo db.PromoCodeRepository) *Server {
	return &Server{repo: repo}
}

// ListPromoCodes returns a page of promo codes for the given pagination request.
func (s *Server) ListPromoCodes(ctx context.Context, req *promocodepbv1.ListPromoCodesRequest) (*promocodepbv1.ListPromoCodesResponse, error) {
	var out *promocodepbv1.ListPromoCodesResponse
	err := rpc.Traced(ctx, "PromoCodeService", "ListPromoCodes", func(ctx context.Context) error {
		items, next, err := s.repo.List(ctx, repox.ListInput{
			PageSize:  req.GetPageSize(),
			PageToken: req.GetPageToken(),
			OrderBy:   req.GetOrderBy(),
			Filter:    req.GetFilter(),
		})
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = &promocodepbv1.ListPromoCodesResponse{PromoCodes: items, NextPageToken: next}
		return nil
	})
	return out, err
}

// GetPromoCode returns a single promo code by resource name.
func (s *Server) GetPromoCode(ctx context.Context, req *promocodepbv1.GetPromoCodeRequest) (*promocodepbv1.PromoCode, error) {
	var out *promocodepbv1.PromoCode
	err := rpc.Traced(ctx, "PromoCodeService", "GetPromoCode", func(ctx context.Context) error {
		pc, err := s.repo.Get(ctx, req.GetName())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = pc
		return nil
	})
	return out, err
}

// CreatePromoCode creates a promo code. The code is the caller's
// promo_code.code, or a generated one when that is empty, and which it was is
// recorded as code_generation; a caller-supplied promo_code_id fixes the
// resource name; validate_only runs request validation without persisting.
func (s *Server) CreatePromoCode(ctx context.Context, req *promocodepbv1.CreatePromoCodeRequest) (*promocodepbv1.PromoCode, error) {
	pc := req.GetPromoCode()
	code, generation := ResolveCode(req)
	// Clone so the resolved code and caller-chosen name don't mutate the inbound
	// request proto.
	pc = proto.Clone(pc).(*promocodepbv1.PromoCode)
	pc.Code = code
	pc.CodeGeneration = generation
	if id := req.GetPromoCodeId(); id != "" {
		name, nerr := types.PromoCodeName(id)
		if nerr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid promo_code_id")
		}
		pc.Name = name
	}
	if req.GetValidateOnly() {
		// Dry run: surface what a real create would reject (duplicate code) without
		// persisting, rather than blindly echoing the request.
		var out *promocodepbv1.PromoCode
		err := rpc.Traced(ctx, "PromoCodeService", "CreatePromoCode.validateOnly", func(ctx context.Context) error {
			switch _, err := s.repo.FindByCode(ctx, pc.GetCode()); {
			case err == nil:
				return status.Error(codes.AlreadyExists, "a promo code with this code already exists")
			case !errors.Is(err, types.ErrNotFound):
				return rpc.ToStatusErr(err)
			}
			out = pc
			return nil
		})
		return out, err
	}
	var out *promocodepbv1.PromoCode
	err := rpc.Traced(ctx, "PromoCodeService", "CreatePromoCode", func(ctx context.Context) error {
		created, err := s.repo.Create(ctx, pc)
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = created
		return nil
	})
	return out, err
}

// UpdatePromoCode applies the update mask to an existing promo code. An empty mask
// replaces every mutable field; validate_only skips persistence.
func (s *Server) UpdatePromoCode(ctx context.Context, req *promocodepbv1.UpdatePromoCodeRequest) (*promocodepbv1.PromoCode, error) {
	pc := req.GetPromoCode()
	if req.GetValidateOnly() {
		// Dry run: confirm the target exists and the etag matches (the checks a real
		// update would enforce) without persisting, rather than echoing the request.
		var out *promocodepbv1.PromoCode
		err := rpc.Traced(ctx, "PromoCodeService", "UpdatePromoCode.validateOnly", func(ctx context.Context) error {
			existing, err := s.repo.Get(ctx, pc.GetName())
			if err != nil {
				return rpc.ToStatusErr(err)
			}
			if pc.GetEtag() != "" && existing.GetEtag() != "" && pc.GetEtag() != existing.GetEtag() {
				return status.Error(codes.Aborted, "etag mismatch")
			}
			out = pc
			return nil
		})
		return out, err
	}
	var out *promocodepbv1.PromoCode
	err := rpc.Traced(ctx, "PromoCodeService", "UpdatePromoCode", func(ctx context.Context) error {
		updated, err := s.repo.Update(ctx, pc, req.GetUpdateMask().GetPaths())
		if err != nil {
			return rpc.ToStatusErr(err)
		}
		out = updated
		return nil
	})
	return out, err
}

// DeletePromoCode removes a promo code by resource name.
func (s *Server) DeletePromoCode(ctx context.Context, req *promocodepbv1.DeletePromoCodeRequest) (*emptypb.Empty, error) {
	err := rpc.Traced(ctx, "PromoCodeService", "DeletePromoCode", func(ctx context.Context) error {
		return rpc.ToStatusErr(s.repo.Delete(ctx, req.GetName()))
	})
	if err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

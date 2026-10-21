// Package idempotency makes the API's `request_id` field mean what its
// documentation has always claimed: reusing an id returns what the first call
// returned, instead of attempting the write a second time.
//
// It is a single unary interceptor rather than per-repository logic, because
// `request_id` is declared on thirteen request messages across six services and
// the rule is identical for all of them. The interceptor finds the field by
// reflection, so an RPC opts in simply by declaring `string request_id` — there
// is nothing to register, and RPCs without the field are untouched.
//
// The record is claimed before the handler runs and settled after it returns:
//
//	claim (INSERT IN_FLIGHT)  ->  handler  ->  settle (UPDATE DONE + response)
//
// The claim is what makes this safe under concurrency. The key's primary key is
// a digest of (method, request_id), so two callers replaying the same id race on
// a unique index and exactly one wins the INSERT. The loser reads the winner's
// row: DONE means it is a replay and gets the first response verbatim; IN_FLIGHT
// means the original call is still running, which is a genuine concurrent
// duplicate and not something we can answer yet. A handler that fails deletes
// its key, so a retry after a real failure is free to proceed.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/oh-tarnished/freebusy/internal/database"
	"github.com/oh-tarnished/freebusy/internal/database/repository/freebusy/shared"
	"github.com/oh-tarnished/freebusy/internal/database/repository/repox"
	"github.com/oh-tarnished/freebusy/internal/types"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/shared/v1/sharedpbv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// requestIDField is the proto field an RPC declares to opt into idempotency.
const requestIDField = "request_id"

// validateOnlyField marks a dry run. A validate_only call places no hold and
// writes nothing, so recording a key for it would burn the caller's request_id
// on a request that never happened — the next real call with that id would
// replay an empty dry-run response instead of creating anything.
const validateOnlyField = "validate_only"

// staleAfter is how long a claim may sit IN_FLIGHT before it is treated as
// abandoned. A process that dies between the handler committing and the key
// settling would otherwise wedge that request_id on Aborted forever. It is far
// longer than any handler runs, so reclaiming a key this old cannot race a
// handler that is still working.
const staleAfter = 10 * time.Minute

// New builds the interceptor over conn, following whichever provider the
// connection was opened for: the generated shared repository speaks both GORM
// and GraphQL, so idempotency works identically on either backend.
func New(conn *database.Connection) grpc.UnaryServerInterceptor {
	repos := shared.New(repox.Conn{Gorm: conn.PgSQLConn, GraphQL: conn.Hasura})
	return Interceptor(repos.IdempotencyKeys)
}

// Interceptor returns the unary interceptor that enforces request_id semantics
// against repo. Build repo from the live connection (see New) so it follows the
// configured provider — the generated repository speaks both GORM and GraphQL.
func Interceptor(repo shared.IdempotencyKeyRepository) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		msg, ok := req.(proto.Message)
		if !ok {
			return handler(ctx, req)
		}
		requestID := stringField(msg, requestIDField)
		if requestID == "" || boolField(msg, validateOnlyField) {
			// Either the RPC does not offer idempotency, or the caller declined
			// it, or it is a dry run that writes nothing. Nothing to dedupe.
			return handler(ctx, req)
		}

		name := keyName(info.FullMethod, requestID)
		key := &sharedpbv1.IdempotencyKey{
			Name:      name,
			Method:    info.FullMethod,
			RequestId: requestID,
			State:     sharedpbv1.IdempotencyState_IDEMPOTENCY_STATE_IN_FLIGHT,
		}

		if _, err := repo.Create(ctx, key); err != nil {
			// We lost the race to claim this id, or the store is unwell. Only the
			// key itself can tell us which, so go and read it.
			return replay(ctx, repo, name, err)
		}

		resp, err := handler(ctx, req)
		if err != nil {
			// The call genuinely failed and wrote nothing worth remembering.
			// Release the id so the caller may retry it for real.
			if derr := repo.Delete(ctx, name); derr != nil {
				logError("idempotency: release key after handler error",
					"method", info.FullMethod, "request_id", requestID, "error", derr)
			}
			return nil, err
		}

		settle(ctx, repo, key, resp, info.FullMethod, requestID)
		return resp, nil
	}
}

// replay answers a caller whose claim lost the race. A DONE key is a retry of a
// call that already succeeded, and gets that call's response verbatim; an
// IN_FLIGHT key means the first call is still running, which is a concurrent
// duplicate we cannot answer without guessing. createErr is the failure that
// sent us here, returned when the key turns out not to explain it.
func replay(ctx context.Context, repo shared.IdempotencyKeyRepository, name string, createErr error) (any, error) {
	existing, err := repo.Get(ctx, name)
	if err != nil {
		if errors.Is(err, types.ErrNotFound) {
			// The create failed for some reason other than a duplicate key, so
			// idempotency has nothing to say about it. Surface the real error.
			return nil, repox.MapGormErr(createErr)
		}
		return nil, status.Error(codes.Internal, "idempotency: read key: "+err.Error())
	}

	if existing.GetState() == sharedpbv1.IdempotencyState_IDEMPOTENCY_STATE_DONE {
		resp, err := decode(existing.GetResponse())
		if err != nil {
			return nil, status.Error(codes.Internal, "idempotency: decode recorded response: "+err.Error())
		}
		return resp, nil
	}

	// IN_FLIGHT. If the claim is older than any handler could possibly run for,
	// the process that made it died before settling; drop it so the next attempt
	// claims cleanly rather than wedging this id forever.
	if created := existing.GetCreateTime(); created != nil && time.Since(created.AsTime()) > staleAfter {
		if err := repo.Delete(ctx, name); err != nil {
			return nil, status.Error(codes.Internal, "idempotency: reclaim abandoned key: "+err.Error())
		}
		return nil, status.Error(codes.Aborted, "a previous attempt with this request_id was abandoned; retry")
	}
	return nil, status.Error(codes.Aborted, "a request with this request_id is already in flight; retry")
}

// settle records what the handler returned, so a later retry of this request_id
// replays it. A failure here is logged and swallowed: the write the caller asked
// for has already happened and returning an error would tell them otherwise. The
// cost is that their retry re-runs the handler and hits the underlying
// constraint instead of replaying — the pre-existing behaviour, not a new bug.
func settle(ctx context.Context, repo shared.IdempotencyKeyRepository, key *sharedpbv1.IdempotencyKey, resp any, method, requestID string) {
	msg, ok := resp.(proto.Message)
	if !ok {
		return
	}
	encoded, err := encode(msg)
	if err != nil {
		logError("idempotency: encode response",
			"method", method, "request_id", requestID, "error", err)
		return
	}
	key.State = sharedpbv1.IdempotencyState_IDEMPOTENCY_STATE_DONE
	key.Response = encoded
	if _, err := repo.Update(ctx, key, []string{"state", "response"}); err != nil {
		logError("idempotency: settle key",
			"method", method, "request_id", requestID, "error", err)
	}
}

// encode packs msg into an Any and renders it as protojson. The Any carries the
// message's type, so a replay can rebuild the response without knowing which RPC
// recorded it.
func encode(msg proto.Message) (string, error) {
	any, err := anypb.New(msg)
	if err != nil {
		return "", err
	}
	out, err := protojson.Marshal(any)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// decode rebuilds the response recorded by encode.
func decode(s string) (proto.Message, error) {
	var any anypb.Any
	if err := protojson.Unmarshal([]byte(s), &any); err != nil {
		return nil, err
	}
	return any.UnmarshalNew()
}

// keyName addresses the key for (method, request_id). The digest keeps the name
// a fixed, safe length whatever the caller sent, and makes the primary key
// itself the uniqueness constraint that decides who wins a concurrent claim.
func keyName(method, requestID string) string {
	sum := sha256.Sum256([]byte(method + "\x00" + requestID))
	return "idempotencyKeys/" + hex.EncodeToString(sum[:])
}

// stringField reads a string field from msg by name, or "" when the message
// does not declare it. This is what lets one interceptor serve every RPC that
// declares request_id without knowing any of their types.
func stringField(msg proto.Message, name string) string {
	fd := field(msg, name)
	if fd == nil || fd.Kind() != protoreflect.StringKind {
		return ""
	}
	return msg.ProtoReflect().Get(fd).String()
}

// boolField reads a bool field from msg by name, or false when absent.
func boolField(msg proto.Message, name string) bool {
	fd := field(msg, name)
	if fd == nil || fd.Kind() != protoreflect.BoolKind {
		return false
	}
	return msg.ProtoReflect().Get(fd).Bool()
}

// field looks up a non-repeated field descriptor by name.
func field(msg proto.Message, name string) protoreflect.FieldDescriptor {
	fd := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil || fd.IsList() || fd.IsMap() {
		return nil
	}
	return fd
}

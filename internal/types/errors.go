package types

import (
	"errors"

	"github.com/shokki-engineering/freebusy/internal/database/repository/repox"
)

// Sentinel errors returned by repositories. Adapters translate backend-specific
// failures into these values so the service layer can map them onto gRPC status
// codes without importing GORM or GraphQL packages. Compare with errors.Is.
//
// The values alias the generated repox sentinels, so hand-written and generated
// repositories are indistinguishable to the gRPC layer during the migration.
var (
	// ErrNotFound indicates the requested record does not exist.
	ErrNotFound = repox.ErrNotFound
	// ErrAlreadyExists indicates a uniqueness conflict on create.
	ErrAlreadyExists = repox.ErrAlreadyExists
	// ErrConflict indicates an optimistic-concurrency (etag/CAS) mismatch: the
	// record changed under us between read and write. Retrying is meaningful.
	ErrConflict = repox.ErrConflict
	// ErrCapacityExhausted indicates the unit has no room left for the requested
	// window. The request is well-formed and the booking is in a fine state — the
	// inventory simply isn't there.
	ErrCapacityExhausted = errors.New("no capacity for the requested window")
	// ErrInvalidState indicates the resource's current state forbids the
	// transition (cancelling a lapsed hold, confirming a cancelled booking).
	// Distinct from ErrCapacityExhausted: nothing about the inventory is wrong,
	// and retrying will not help.
	//
	// These three were one sentinel until callers pointed out that "conflict"
	// could not tell an idempotent re-cancel apart from someone taking the last
	// room. Keep them apart.
	ErrInvalidState = errors.New("invalid state for the requested transition")
	// ErrInvalidArgument indicates a caller-supplied argument was rejected (e.g.
	// an order_by field outside the sortable allowlist).
	ErrInvalidArgument = repox.ErrInvalidArgument
	// ErrUnimplemented indicates the configured provider does not support the
	// operation yet (e.g. a repository method pending backend regeneration).
	ErrUnimplemented = errors.New("unimplemented")
)

package idempotency

import "github.com/oh-tarnished/freebusy/shared"

// logError reports a failure the interceptor cannot return to the caller. It
// lives apart from idempotency.go, which imports the generated repository
// package also named shared.
func logError(msg string, kv ...any) {
	_ = shared.Telemetry.Logger.Error(msg, kv...)
}

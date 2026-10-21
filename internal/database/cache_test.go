package database

import (
	"context"
	"testing"
)

// TestOpenCacheProviderDisabled pins the documented "no cache configured" path:
// a nil Provider and no error, so a caller falls back to the undecorated store
// rather than treating an intentionally-off cache as a startup failure.
func TestOpenCacheProviderDisabled(t *testing.T) {
	// config.Get() reads the embedded release defaults, where cache.enabled is
	// false; the dev overlay that turns it on is not loaded in a unit test.
	p, err := OpenCacheProvider(context.Background())
	if err != nil {
		t.Fatalf("OpenCacheProvider with cache disabled: %v", err)
	}
	if p != nil {
		t.Fatalf("OpenCacheProvider = %v, want nil when disabled", p)
	}
}

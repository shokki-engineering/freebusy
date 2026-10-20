package promocode

import (
	"testing"

	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
)

func TestResolveCode(t *testing.T) {
	req := func(code string) *promocodepbv1.CreatePromoCodeRequest {
		return &promocodepbv1.CreatePromoCodeRequest{PromoCode: &promocodepbv1.PromoCode{Code: code}}
	}
	if c, g := ResolveCode(req(" SUMMER25 ")); c != "SUMMER25" || g != promocodepbv1.CodeGeneration_CODE_GENERATION_MANUAL {
		t.Fatalf("supplied code: %q %v", c, g)
	}
	for _, empty := range []string{"", "   "} {
		c, g := ResolveCode(req(empty))
		if c == "" || g != promocodepbv1.CodeGeneration_CODE_GENERATION_AUTO {
			t.Fatalf("empty code %q: got %q %v", empty, c, g)
		}
	}
}

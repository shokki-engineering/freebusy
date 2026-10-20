package promocode

import (
	"strings"

	"github.com/oh-tarnished/freebusy/internal/service/promocode/code"
	"github.com/oh-tarnished/freebusy/protobuf/generated/go/promocode/v1/promocodepbv1"
)

// ResolveCode picks the human-entered code for a create, and says how it was
// chosen: the caller's promo_code.code verbatim when given (MANUAL), or a
// freshly generated one when it was left empty (AUTO).
func ResolveCode(req *promocodepbv1.CreatePromoCodeRequest) (string, promocodepbv1.CodeGeneration) {
	if c := strings.TrimSpace(req.GetPromoCode().GetCode()); c != "" {
		return c, promocodepbv1.CodeGeneration_CODE_GENERATION_MANUAL
	}
	return code.Generate(), promocodepbv1.CodeGeneration_CODE_GENERATION_AUTO
}

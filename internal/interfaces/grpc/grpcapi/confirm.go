package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
)

// ConfirmOperator 从调用方身份构造确认票操作者（匿名 = 零值）。
func ConfirmOperator(ctx context.Context) governance.ConfirmOperator {
	if id, ok := authn.FromContext(ctx); ok {
		return governance.ConfirmOperator{TokenID: id.TokenID, UserID: id.UserID, TeamID: id.TeamID}
	}
	return governance.ConfirmOperator{}
}

// SHA256Hex 是确认票摘要的确定性哈希（多段以换行分隔，避免拼接歧义）。
func SHA256Hex(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

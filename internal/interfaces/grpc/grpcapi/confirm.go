package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
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

// ConfirmPeekFunc 是确认票取证腿的 peek 消费面（票面锚经 peek 取不删；
// datagrpc 三类型与 structuregrpc 的 Tickets 面同签名——helper 按此中立）。
type ConfirmPeekFunc func(ticket string) (governance.ConfirmTicketPayload, governance.ConfirmPeekStatus)

// ConfirmRedeemFact 铸确认动词（confirm.redeem/confirm.skip）的 LedgerOnly
// 取证行单源（确认票取证行家族——data 三类型 Update、app teardown、volume
// purge 同形收编）：skip 通道落 proof=skipped；peek 命中落 proof=redeemed
// 票面锚（verb/preview_digest/request_digest）。peek 传 nil = 无取证腿
// （purge 形态——烧票后 peek 不可见或面本身免 peek，AfterFP 留空）；tail
// 非空时在 redeemed 行尾追加票面衍生段（teardown 的 "; hook_row=…" 锚）。
// 调用时点约束沿家族先例：须在烧票之前（烧后 peek 只见 tombstone）。
func ConfirmRedeemFact(ctx context.Context, aggregate, id, ticket string, skip bool,
	peek ConfirmPeekFunc, tail func(payload governance.ConfirmTicketPayload) string) *writes.FactRow {
	fr := writes.FactRow{
		Aggregate: aggregate, ID: id,
		Action: "confirm.redeem", Resource: aggregate + "/" + id,
		Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		// LedgerOnly：取证面单行（流面事实由主 fact 承载）。
		LedgerOnly: true,
	}
	if skip {
		fr.Action = "confirm.skip"
		fr.AfterFP = "proof=skipped"
		return &fr
	}
	if peek != nil {
		if payload, status := peek(ticket); status == governance.ConfirmPeekHit {
			fr.AfterFP = fmt.Sprintf("proof=redeemed; verb=%s; preview_digest=%s; request_digest=%s",
				payload.Verb, payload.PreviewDigest, payload.RequestDigest)
			if tail != nil {
				fr.AfterFP += tail(payload)
			}
		}
	}
	return &fr
}

// PrependConfirmFact 把确认取证行前置到事实集（confirm 行在场时居前、主
// fact 殿后——确认事实与业务事实同事务同序；teardownConfirmFacts 形态的
// grpcapi 推广，nil 容忍由本面承载）。
func PrependConfirmFact(confirmFact *writes.FactRow, fact func() writes.FactRow) []func() writes.FactRow {
	if confirmFact == nil {
		return []func() writes.FactRow{fact}
	}
	cf := *confirmFact
	return []func() writes.FactRow{func() writes.FactRow { return cf }, fact}
}

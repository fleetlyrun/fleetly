package governance

// ConfirmGuard 拦截器单测（B1，设计稿 2026-10-30 §1.2/§1.4）：表外放行、
// 无票 REQUIRED、skip 逃生门、peek 四态辨因映射、Verb 锚、请求者身份锚。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// ticketState 是 peek 四态的最小内存票面（辨因可控注入）。
type confirmFakeStore struct {
	payload map[string]ConfirmTicketPayload
	status  map[string]ConfirmPeekStatus
	burned  []string
}

func (f *confirmFakeStore) PeekConfirm(ticket string) (ConfirmTicketPayload, ConfirmPeekStatus) {
	if st, ok := f.status[ticket]; ok {
		return ConfirmTicketPayload{}, st
	}
	return f.payload[ticket], ConfirmPeekHit
}

func (f *confirmFakeStore) DiscardConfirm(ticket string) bool {
	if _, ok := f.payload[ticket]; !ok {
		return false
	}
	delete(f.payload, ticket)
	f.burned = append(f.burned, ticket)
	return true
}

const deployMethod = "/fleetly.delivery.v1.DeploymentsService/Deploy"

// callConfirm 经拦截器驱动一次 Deploy 请求（identity 注入请求者）。
func callConfirm(ctx context.Context, store ConfirmTicketStore, method string, req any) (bool, string) {
	guard := NewConfirmGuard(store)
	reached := false
	var code string
	_, err := guard.Unary()(ctx, req, &grpc.UnaryServerInfo{FullMethod: method}, func(context.Context, any) (any, error) {
		reached = true
		return nil, nil
	})
	if err != nil {
		if e, ok := err.(*apperr.Error); ok {
			code = e.Code()
		} else {
			code = "non-apperr"
		}
	}
	return reached, code
}

func operatorCtx(tokenID, userID, teamID string) context.Context {
	return authn.WithIdentity(context.Background(), &authn.Identity{
		TokenID: tokenID, UserID: userID, TeamID: teamID,
	})
}

func TestConfirmGuardEnforcement(t *testing.T) {
	deployReq := func(ticket string, skip bool) *deliveryv1.DeployRequest {
		return &deliveryv1.DeployRequest{
			AppId: "app-1", Image: "nginx:1.27",
			Confirmation: &deliveryv1.Confirmation{Ticket: ticket, Skip: skip},
		}
	}
	validTicket := func(verb string) (string, *confirmFakeStore) {
		store := &confirmFakeStore{payload: map[string]ConfirmTicketPayload{
			"tk-good": {Verb: verb, TargetApp: "app-1", Operator: ConfirmOperator{TokenID: "tok-1", UserID: "u-1", TeamID: "default"}},
		}}
		return "tk-good", store
	}

	t.Run("off-table verb passes through untouched", func(t *testing.T) {
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), &confirmFakeStore{},
			"/fleetly.delivery.v1.DeploymentsService/RestartProcess",
			&deliveryv1.RestartProcessRequest{AppId: "app-1", ProcessName: "web"})
		assert.True(t, reached)
		assert.Empty(t, code, "Restart/Scale exemption family never reaches the confirm face")
	})

	t.Run("no ticket and no skip is refused with REQUIRED", func(t *testing.T) {
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), &confirmFakeStore{},
			deployMethod, deployReq("", false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_REQUIRED", code)
	})

	t.Run("nil confirmation is refused with REQUIRED", func(t *testing.T) {
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), &confirmFakeStore{},
			deployMethod, &deliveryv1.DeployRequest{AppId: "app-1", Image: "nginx:1.27"})
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_REQUIRED", code)
	})

	t.Run("skip is the --yes escape hatch and passes", func(t *testing.T) {
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), &confirmFakeStore{},
			deployMethod, deployReq("", true))
		assert.True(t, reached)
		assert.Empty(t, code)
	})

	t.Run("unknown (forged) ticket is MISMATCH", func(t *testing.T) {
		store := &confirmFakeStore{status: map[string]ConfirmPeekStatus{"tk-bad": ConfirmPeekUnknown}}
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), store, deployMethod, deployReq("tk-bad", false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_MISMATCH", code)
	})

	t.Run("expired ticket is STALE", func(t *testing.T) {
		store := &confirmFakeStore{status: map[string]ConfirmPeekStatus{"tk-old": ConfirmPeekExpired}}
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), store, deployMethod, deployReq("tk-old", false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_STALE", code)
	})

	t.Run("consumed ticket is MISMATCH (already redeemed, distinguishable)", func(t *testing.T) {
		store := &confirmFakeStore{status: map[string]ConfirmPeekStatus{"tk-used": ConfirmPeekConsumed}}
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), store, deployMethod, deployReq("tk-used", false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_MISMATCH", code)
	})

	t.Run("verb mismatch is MISMATCH", func(t *testing.T) {
		ticket, store := validTicket("/fleetly.structure.v1.AppsService/DeleteApp")
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), store, deployMethod, deployReq(ticket, false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_MISMATCH", code)
	})

	t.Run("operator mismatch is MISMATCH (tickets do not cross operators)", func(t *testing.T) {
		ticket, store := validTicket(deployMethod)
		reached, code := callConfirm(operatorCtx("tok-other", "u-1", "default"), store, deployMethod, deployReq(ticket, false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_MISMATCH", code)
	})

	t.Run("unauthenticated caller is MISMATCH (fail-closed)", func(t *testing.T) {
		ticket, store := validTicket(deployMethod)
		reached, code := callConfirm(context.Background(), store, deployMethod, deployReq(ticket, false))
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_MISMATCH", code)
	})

	t.Run("valid ticket passes through to the handler", func(t *testing.T) {
		ticket, store := validTicket(deployMethod)
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), store, deployMethod, deployReq(ticket, false))
		assert.True(t, reached)
		assert.Empty(t, code)
	})

	t.Run("DeleteApp request carries the confirmation field and enforces too", func(t *testing.T) {
		reached, code := callConfirm(operatorCtx("tok-1", "u-1", "default"), &confirmFakeStore{},
			"/fleetly.structure.v1.AppsService/DeleteApp",
			&structurev1.DeleteAppRequest{Id: "app-1"})
		assert.False(t, reached)
		assert.Equal(t, "E_CONFIRMATION_REQUIRED", code)
	})
}

// ConfirmableVerbs 与立法面一致（confirmguard 守卫另有 proto 对账——此处
// 钉表内容的用例级下限）。
func TestConfirmableVerbsLedger(t *testing.T) {
	require.Len(t, ConfirmableVerbs, 3, "the mandatory set is Deploy + Rollback + DeleteApp (design 2026-10-30 §1.2)")
	for verb, reason := range ConfirmableVerbs {
		assert.NotEmpty(t, reason, "%s must carry a legislative reason", verb)
	}
}

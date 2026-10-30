package engine

// Confirm Ticket 受理临界区白盒测试（B1，设计稿 2026-10-30 §1.4 甲'）：
// ④request_digest 比对、⑤base 多锚逐锚漂移、烧票契约（queue-full 不烧 +
// 重试成功）、双跑竞态腿（预览后 webhook 落新冻结体 → STALE）、LedgerOnly
// 审计行（确认/跳过两形态）、并发同票第二请求拒。

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
)

// fakeConfirmTickets 是 confirm 票据面的内存假件（辨因语义与真实票据店
// 同构：烧 → Consumed 短窗；缺 TTL 面——过期腿在 fleetlygrpc 票据店单测）。
type fakeConfirmTickets struct {
	mu      sync.Mutex
	tickets map[string]governance.ConfirmTicketPayload
	burned  map[string]bool
}

func newFakeConfirmTickets() *fakeConfirmTickets {
	return &fakeConfirmTickets{tickets: map[string]governance.ConfirmTicketPayload{}, burned: map[string]bool{}}
}

func (f *fakeConfirmTickets) PeekConfirm(ticket string) (governance.ConfirmTicketPayload, governance.ConfirmPeekStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.burned[ticket] {
		return governance.ConfirmTicketPayload{}, governance.ConfirmPeekConsumed
	}
	p, ok := f.tickets[ticket]
	if !ok {
		return governance.ConfirmTicketPayload{}, governance.ConfirmPeekUnknown
	}
	return p, governance.ConfirmPeekHit
}

func (f *fakeConfirmTickets) DiscardConfirm(ticket string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tickets[ticket]; !ok || f.burned[ticket] {
		return false
	}
	delete(f.tickets, ticket)
	f.burned[ticket] = true
	return true
}

func (f *fakeConfirmTickets) mint(p governance.ConfirmTicketPayload) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := "tk-" + p.RequestDigest[:8]
	f.tickets[tok] = p
	return tok
}

// confirmTestEnv 铸带票受理的夹具：引擎 + 注入票据面 + 预览侧事实（以
// 同参数冻结一次拿规范形与 base 锚——内容寻址保证重冻结同 digest，W2a
// 预览管线的合成同参语义在此模拟）→ 票据。
func confirmTestEnv(t *testing.T, spec *specv1.AppSpec, sem governance.ConfirmSemantics) (*Engine, *fakeConfirmTickets, string, string) {
	t.Helper()
	e, _, _ := newTestEngine(t)
	tickets := newFakeConfirmTickets()
	e.BindConfirmTickets(tickets)
	rev, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{AppID: tAppID, Spec: spec})
	require.NoError(t, err)
	base := currentConfirmBase(t, e)
	ticket := tickets.mint(governance.ConfirmTicketPayload{
		Verb:          "/fleetly.delivery.v1.DeploymentsService/Deploy",
		TargetApp:     tAppID,
		RequestDigest: governance.ConfirmRequestDigest(rev.Spec, sem),
		Semantics:     sem,
		Base:          base,
		PreviewDigest: "preview-digest-test",
	})
	return e, tickets, ticket, rev.ID
}

// currentConfirmBase 采集当前 base 多锚（漂移注入前的世界快照）。
func currentConfirmBase(t *testing.T, e *Engine) governance.ConfirmBase {
	t.Helper()
	ctx := context.Background()
	var base governance.ConfirmBase
	latest, err := e.repos.Revisions.Latest(ctx, tAppID)
	require.NoError(t, err)
	base.LatestFrozenDigest = latest.Digest
	env := map[string]string{}
	if vars, verr := e.repos.SharedVariables.ListActive(ctx, tProjectID); verr == nil {
		for _, v := range vars {
			env[v.Name] = v.Value
		}
	}
	if vars, verr := e.repos.AppVariables.ListActive(ctx, tAppID); verr == nil {
		for _, v := range vars {
			env[v.Name] = v.Value
		}
	}
	base.VarEnvDigest = governance.ConfirmVarEnvDigest(env)
	return base
}

// ④ request_digest 不符 → MISMATCH；票不烧（拒绝零副作用）。
func TestSubmitSpecConfirmDigestMismatch(t *testing.T) {
	sem := governance.ConfirmSemantics{}
	spec := submitImageSpec(tAppID, "nginx:1.27", nil)
	e, tickets, ticket, _ := confirmTestEnv(t, spec, sem)
	ctx := context.Background()

	// 同 spec 但语义位不同（supersede=true）——指纹半边漂移即 MISMATCH。
	_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil),
		Submit:       &SubmitOptions{},
		Confirmation: &ConfirmInput{Ticket: ticket, Semantics: governance.ConfirmSemantics{Supersede: true}},
	})
	require.ErrorIs(t, err, ErrConfirmMismatch)
	_, status := tickets.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekHit, status, "a rejected submit must not burn the ticket")

	// 内容漂移（不同 image）同 MISMATCH。
	_, _, _, err = e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.29", nil),
		Submit:       &SubmitOptions{},
		Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
	})
	require.ErrorIs(t, err, ErrConfirmMismatch)
	_, status = tickets.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekHit, status)
}

// ⑤ base 多锚逐锚漂移 → STALE：变量环境 / hook 行 / 外来新冻结体 /
// LatestSucceeded。外来新冻结体腿同时是「双跑竞态腿」：预览后 webhook
// 落新冻结体，确认时最新冻结体既非票面锚也非本请求冻结体 → STALE。
func TestSubmitSpecConfirmBaseDrift(t *testing.T) {
	sem := governance.ConfirmSemantics{}

	t.Run("variable environment drift", func(t *testing.T) {
		spec := submitImageSpec(tAppID, "nginx:1.27", nil)
		e, _, ticket, _ := confirmTestEnv(t, spec, sem)
		require.NoError(t, e.repos.SharedVariables.Upsert(context.Background(),
			&sharedvariable.SharedVariable{ProjectID: tProjectID, Name: "LOG_LEVEL", Value: "debug"}))
		_, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
			Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
		})
		require.ErrorIs(t, err, ErrConfirmStale)
	})

	t.Run("hook row drift", func(t *testing.T) {
		spec := submitImageSpec(tAppID, "nginx:1.27", nil)
		e, _, ticket, _ := confirmTestEnv(t, spec, sem)
		require.NoError(t, e.repos.Hooks.Create(context.Background(), &hook.Hook{
			AppID: tAppID, Repo: "https://github.com/acme/web.git", Branch: "main",
			UpdatedAt: "v2", SecretCiphertext: []byte("envelope"),
		}))
		_, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
			Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
		})
		require.ErrorIs(t, err, ErrConfirmStale)
	})

	t.Run("webhook drops a newer frozen revision between preview and confirm", func(t *testing.T) {
		spec := submitImageSpec(tAppID, "nginx:1.27", nil)
		e, _, ticket, _ := confirmTestEnv(t, spec, sem)
		// webhook 腿：外来 SubmitSpec 只冻结（不部署）——冻结相在 appMu
		// 外，可穿插在预览与确认之间（甲'裁决的场景半边）。
		_, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.30", nil),
		})
		require.NoError(t, err)
		_, _, _, err = e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
			Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
		})
		require.ErrorIs(t, err, ErrConfirmStale,
			"the newest frozen revision is neither the preview anchor nor this submit's own freeze — the confirmed payload was computed against a stale world")
	})

	t.Run("latest succeeded drift", func(t *testing.T) {
		spec := submitImageSpec(tAppID, "nginx:1.27", nil)
		e, _, ticket, _ := confirmTestEnv(t, spec, sem)
		r1 := freezeSpec(t, e, 1, imageSpecFor("nginx:1.26"))
		deployToSucceeded(t, e, r1)
		_, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
			Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
		})
		require.ErrorIs(t, err, ErrConfirmStale)
	})

	t.Run("no drift admits and burns", func(t *testing.T) {
		spec := submitImageSpec(tAppID, "nginx:1.27", nil)
		e, tickets, ticket, _ := confirmTestEnv(t, spec, sem)
		_, d, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
			AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
			Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
		})
		require.NoError(t, err)
		assert.Equal(t, deployment.StateQueued, d.State)
		_, status := tickets.PeekConfirm(ticket)
		assert.Equal(t, governance.ConfirmPeekConsumed, status, "a successful admission must burn the ticket")
	})
}

// 烧票契约（设计稿 §1.4）：queue-full 时冻结成功而 admission 拒——不烧、
// 票保留；重试经冻结体内容寻址复用，成功受理后烧。容量注入 1（engine
// 既有 queue-full 先例形态）；占位与带票请求同一冻结体（内容寻址复用），
// 最新冻结体锚保持在票面值（self 形态，不制造额外漂移）。
func TestSubmitSpecConfirmQueueFullKeepsTicketForRetry(t *testing.T) {
	sem := governance.ConfirmSemantics{}
	spec := submitImageSpec(tAppID, "nginx:1.27", nil)
	e, tickets, ticket, _ := confirmTestEnv(t, spec, sem)
	e.opts.Deploy.QueueCapacity = 1
	ctx := context.Background()

	// 占位一条 queued（无票——豁免面之外的内部受理形态）。
	_, _, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: spec, Submit: &SubmitOptions{IdempotencyKey: "fill"},
	})
	require.NoError(t, err)

	// 带票 Deploy：queue full → admission 拒 → 票保留（冻结成功不烧）。
	_, _, _, err = e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: spec, Submit: &SubmitOptions{IdempotencyKey: "ticketed"},
		Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
	})
	require.ErrorIs(t, err, ErrQueueFull)
	_, status := tickets.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekHit, status, "queue-full must not burn the ticket (freeze succeeded, admission refused)")

	// 清空排队（cancel 占位行）——cancelled 非 succeeded，LatestSucceeded
	// 锚不动，世界与预览时一致。
	active, err := e.repos.Deployments.ActiveByApp(ctx, tAppID)
	require.NoError(t, err)
	require.Len(t, active, 1)
	_, cerr := e.Cancel(ctx, active[0].ID)
	require.NoError(t, cerr)

	// 重试：内容寻址复用冻结体，⑤④照过，admission 成功，烧票。
	_, d, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: spec, Submit: &SubmitOptions{IdempotencyKey: "ticketed"},
		Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
	})
	require.NoError(t, err, "retry after queue drain must admit (content-addressed freeze reuse)")
	assert.Equal(t, deployment.StateQueued, d.State)
	_, status = tickets.PeekConfirm(ticket)
	assert.Equal(t, governance.ConfirmPeekConsumed, status, "the retried admission must burn the ticket")
}

// LedgerOnly 审计行（设计稿 §1.2 审计 A8）：确认（proof=redeemed，带票面
// 全锚 → deployment ID）与跳过（proof=skipped）两形态取证面可辨。
func TestSubmitSpecConfirmLedgerRows(t *testing.T) {
	sem := governance.ConfirmSemantics{}
	spec := submitImageSpec(tAppID, "nginx:1.27", nil)
	e, _, ticket, _ := confirmTestEnv(t, spec, sem)
	ctx := context.Background()

	_, d, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
		Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
	})
	require.NoError(t, err)
	require.NotEmpty(t, d.ID)

	_, d2, _, err := e.SubmitSpec(ctx, SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.31", nil), Submit: &SubmitOptions{},
		Confirmation: &ConfirmInput{Skip: true},
	})
	require.NoError(t, err)
	require.NotEmpty(t, d2.ID)

	rows, err := e.repos.Audits.List(ctx, 500)
	require.NoError(t, err)
	redeemed, skipped, redeemedResource := "", "", ""
	for _, r := range rows {
		switch r.Action {
		case "confirm.redeem":
			redeemed, redeemedResource = r.AfterFP, r.Resource
		case "confirm.skip":
			skipped = r.AfterFP
		}
	}
	require.NotEmpty(t, redeemed, "a ticketed admission must leave a confirm.redeem ledger row")
	assert.Equal(t, "deployment/"+d.ID, redeemedResource, "the ledger row must anchor the admitted deployment id")
	assert.Contains(t, redeemed, "proof=redeemed")
	assert.Contains(t, redeemed, "preview_digest=preview-digest-test")
	assert.Contains(t, redeemed, "request_digest=")
	assert.Contains(t, redeemed, "base_frozen=")
	assert.Contains(t, redeemed, "var_env=")
	require.NotEmpty(t, skipped, "a --yes admission must leave a confirm.skip ledger row")
	assert.Equal(t, "proof=skipped", skipped)
}

// 并发同票第二请求：单临界区一次闭合——恰一成功，其余撞已烧票 MISMATCH。
func TestSubmitSpecConfirmConcurrentSameTicket(t *testing.T) {
	sem := governance.ConfirmSemantics{}
	spec := submitImageSpec(tAppID, "nginx:1.27", nil)
	e, _, ticket, _ := confirmTestEnv(t, spec, sem)
	ctx := context.Background()

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _, errs[i] = e.SubmitSpec(ctx, SubmitSpecRequest{
				AppID: tAppID, Spec: spec, Submit: &SubmitOptions{IdempotencyKey: fmt.Sprintf("same-ticket-%d", i)},
				Confirmation: &ConfirmInput{Ticket: ticket, Semantics: sem},
			})
		}(i)
	}
	wg.Wait()
	var ok, mismatch int
	for _, err := range errs {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, ErrConfirmMismatch)
			mismatch++
		}
	}
	assert.Equal(t, 1, ok, "exactly one concurrent submit may redeem the ticket")
	assert.Equal(t, n-1, mismatch, "the rest must hit the burned-ticket tombstone")
}

// skip 逃生门：不验票不烧票，受理照常（--yes 形态）。
func TestSubmitSpecConfirmSkipBypass(t *testing.T) {
	e, _, _ := newTestEngine(t)
	tickets := newFakeConfirmTickets()
	e.BindConfirmTickets(tickets)
	_, d, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
		AppID: tAppID, Spec: submitImageSpec(tAppID, "nginx:1.27", nil), Submit: &SubmitOptions{},
		Confirmation: &ConfirmInput{Skip: true},
	})
	require.NoError(t, err)
	assert.Equal(t, deployment.StateQueued, d.State)
	assert.Empty(t, tickets.tickets, "skip must neither peek nor burn")
}

// 目标锚：票绑别的 App → MISMATCH（跨目标重放拒绝）。
func TestSubmitSpecConfirmTargetAppMismatch(t *testing.T) {
	sem := governance.ConfirmSemantics{}
	spec := submitImageSpec(tAppID, "nginx:1.27", nil)
	e, _, _, _ := confirmTestEnv(t, spec, sem)
	tickets := e.confirmTickets.(*fakeConfirmTickets)
	cross := tickets.mint(governance.ConfirmTicketPayload{
		Verb:          "/fleetly.delivery.v1.DeploymentsService/Deploy",
		TargetApp:     tApp2ID,
		RequestDigest: governance.ConfirmRequestDigest(frozenSpecFor(t, e, spec), sem),
		Semantics:     sem,
	})
	_, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{
		AppID: tAppID, Spec: spec, Submit: &SubmitOptions{},
		Confirmation: &ConfirmInput{Ticket: cross, Semantics: sem},
	})
	require.ErrorIs(t, err, ErrConfirmMismatch)
}

// frozenSpecFor 返回 spec 的冻结规范形（内容寻址复用语义下与受理产出
// 逐字节一致）。
func frozenSpecFor(t *testing.T, e *Engine, spec *specv1.AppSpec) []byte {
	t.Helper()
	rev, _, _, err := e.SubmitSpec(context.Background(), SubmitSpecRequest{AppID: tAppID, Spec: spec})
	require.NoError(t, err)
	return rev.Spec
}

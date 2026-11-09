package browses

// Browse 会话域应用模块白盒（F3.6，ADR-0051；P5 wave-3 随域拆包自 engine 包
// 迁入，断言面零漂移）：收敛（方言投影/材料/第五轴 ns）/双路由（entry
// 免门禁 + ForwardAuth 门禁）/grant 兑换与校验/硬 TTL 与空闲回收/重启行
// 恢复（grant 重铸）。夹具：hermetic 应用模块（真 SQLite 台账 repo + Runtime
// 三动词假面 + ensure cache 协议端口的本地实现——ensureMu 单锁语义留核，白盒面
// 持等价实现）；受理四件一拍与 ephemeral 路由合并的核侧集成面经 gRPC
// apitest 与 engine 环回承载。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	browserepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/browse"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 测试常量（engine 包同源夹具 ID——断言面零漂移）。
const (
	tProjectID      = "01JD0PROJ00000000000000000"
	tDatabaseID     = "01JD0DB000000000000000000"
	tDatabaseNm     = "shop"
	tBrowseSession  = "01JDBRWSE000000000000000"
	tBrowseHostSufx = "browse.test"
	tBrowseGateway  = "http://127.0.0.1:9081"
)

// ensureCall 是一次 Ensure 的记录面（ns + 期望集 + 代次 + 材料）。
type ensureCall struct {
	NS        capability.NamespaceRef
	ByID      map[string]capability.Workload
	Gen       capability.Generation
	Materials capability.Materials
}

// fakeRuntime 是应用模块消费面（Ensure/Remove/Addresses）的假底座（其余
// Runtime 扩展接口 nil 嵌入兜底——browses 只触达三动词）。
type fakeRuntime struct {
	capability.Runtime
	mu      sync.Mutex
	ensures []ensureCall
	removed []capability.NamespaceRef
}

func (f *fakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-runtime", Capability: capability.KindRuntime, Version: "test"}
}

func (f *fakeRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	byID := map[string]capability.Workload{}
	for _, w := range ws {
		byID[w.ID] = w
	}
	f.ensures = append(f.ensures, ensureCall{NS: ns, ByID: byID, Gen: gen, Materials: m})
	return capability.EnsureReport{}, nil
}

func (f *fakeRuntime) Remove(_ context.Context, ns capability.NamespaceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

func (f *fakeRuntime) Addresses(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload) ([]capability.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(ws) == 0 {
		return nil, nil
	}
	return []capability.Endpoint{{Addr: "10.42.0.7", Port: ws[0].Ports[0].Port}}, nil
}

func (f *fakeRuntime) calls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ensureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

func (f *fakeRuntime) removedSnapshot() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.removed))
	copy(out, f.removed)
	return out
}

// memoBox 是 ensure cache 协议端口的本地实现（与核 ensureMu 族等价的锁语义——
// 白盒夹具；重放节拍不参与本夹具的断言面）。
type memoBox struct {
	mu sync.Mutex
}

func (m *memoBox) fresh(memo map[string]contract.Memo, key, sig string, _ time.Time) (contract.Memo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := memo[key]
	if !ok || rec.Sig != sig {
		return contract.Memo{}, false
	}
	return rec, true
}

func (m *memoBox) remember(memo map[string]contract.Memo, key string, rec contract.Memo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	memo[key] = rec
}

func (m *memoBox) forget(memo map[string]contract.Memo, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(memo, key)
}

// newBrowseFixture 组装 browse 收敛夹具：项目/网络/凭证 Secret +
// browse 台账行（database 行本体不必须——库名经 databaseNameOf 点查，
// 缺行时名字空但渲染走 Secret URL 的 host——方言断言面足够）。
func newBrowseFixture(t *testing.T, engineName, connectURL string, readOnly bool) (*Browses, *fakeRuntime, *browse.Session, *statetest.FakeClock) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := &fakeRuntime{}
	cipher, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	run := db.Runner()
	stores := transact.Stores{
		Projects: projectrepo.New(clock, run), Networks: networkrepo.New(clock, run),
		Secrets: secretrepo.New(clock, run), Databases: dbrepo.New(clock, run),
		Browse: browserepo.New(clock, run),
	}
	memo := &memoBox{}
	b := New(Deps{
		Runtime: rt,
		Repos:   stores,
		Cipher:  cipher,
		Log:     nil, // 域内错误日志经 nil 契约测试（夹具零噪音；日志面有 e2e 覆盖）
		Clock:   clock,
		BrowseConfig: func() BrowseConfig {
			return BrowseConfig{HostSuffix: tBrowseHostSufx, GatewayURL: tBrowseGateway, TLSMode: "none"}
		},
		LockMaintenance: func() func() { return func() {} },
		BoundedStep: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 30*time.Second)
		},
		EnsureFresh:                  memo.fresh,
		EnsureRemember:               memo.remember,
		EnsureForget:                 memo.forget,
		ProjectTeam:                  func(context.Context, string) (string, error) { return "default", nil },
		ProjectNetworkFactsNamesOnly: func(context.Context, string) []string { return []string{"default"} },
		RecordOwners:                 func(string, uint64, []capability.Workload) {},
		RecordExpected:               func(string, uint64) {},
		KickBrowse:                   func() {},
		PublishRoutesNow:             func() {},
		EnsureMemo:                   make(map[string]contract.Memo),
	})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, run).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock, run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := contract.DBCredentialSecretName(tDatabaseNm)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(clock, run).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	// database 行（databaseNameOf 的点查目标——凭证 Secret 名的输入）。
	require.NoError(t, dbrepo.New(clock, run).Create(ctx, &database.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseNm,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	row := &browse.Session{
		ID: tBrowseSession, ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: engineName, ReadOnly: readOnly,
		CreatedAt: clock.Now().Format(time.RFC3339),
		ExpiresAt: clock.Now().Add(BrowseHardTTL).Format(time.RFC3339),
	}
	require.NoError(t, stores.Browse.Create(ctx, row))
	return b, rt, row, clock
}

// TestBrowseStepConverges：台账行 → 采纳 → Ensure（第五轴 ns + pgweb
// 钉版镜像 + pgpass 材料 + 项目网挂靠 + 寻址名）。
func TestBrowseStepConverges(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, rt, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()

	b.Step(ctx)
	b.Step(ctx) // 签名短路后幂等：不重投影不重 Ensure

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Browse: tBrowseSession}, last.NS)
	require.Len(t, last.ByID, 1)
	var w capability.Workload
	for _, x := range last.ByID {
		w = x
	}
	assert.Equal(t, tBrowseSession, w.ID)
	assert.Equal(t, "pgweb", w.Process)
	assert.Contains(t, w.Image, "sosedoff/pgweb:0.17.0@sha256:")
	// 只读形态：--readonly 旗标 + URL options 服务端执法参数。
	assert.Contains(t, w.Command, "--readonly")
	var urlArg string
	for _, a := range w.Command {
		if len(a) > 6 && a[:6] == "--url=" {
			urlArg = a[6:]
		}
	}
	assert.Contains(t, urlArg, "options=-c%20default_transaction_read_only%3Don")
	// 密码走 pgpass 材料（argv/env 恒净）。
	assert.Contains(t, string(last.Materials.SecretFiles["pgpass"]), "secretpw")
	assert.NotContains(t, urlArg, "secretpw")
	// 项目网挂靠 + 寻址名。
	assert.Equal(t, []string{"default"}, w.Networks)
	assert.Equal(t, BrowseDNSName(tBrowseSession), w.Addressing[0].Name)
}

// TestBrowseCapabilityRoutes：双路由形态——entry（免门禁，后端 =
// gateway）+ 工具路由（ForwardAuth 指向 gateway authorize；后端经
// Addresses 解析）。冷启动（无投影）时工具路由缺席（诚实 404 窗）。
func TestBrowseCapabilityRoutes(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()

	// 冷启动（已注册未收敛）：只有 entry 路由——工具后端解析不到投影
	// 期望集（诚实 404 窗，下拍收敛）。
	_, err := b.RegisterBrowseSession(BrowseInput{
		SessionID: tBrowseSession, TeamID: "default", ProjectID: tProjectID,
		DatabaseID: tDatabaseID, DatabaseName: tDatabaseNm, EngineName: "postgres", ReadOnly: true,
	})
	require.NoError(t, err)
	routes := b.BrowseCapabilityRoutes(ctx)
	require.Len(t, routes, 1)
	assert.Equal(t, "/v1/browse/entry", routes[0].Path)
	assert.Equal(t, "127.0.0.1:9081", routes[0].BackendAddr)
	assert.Nil(t, routes[0].Auth)

	// 收敛后：双路由（fake runtime 的 Addresses 解析投影期望集）。
	b.Step(ctx)
	routes = b.BrowseCapabilityRoutes(ctx)
	require.Len(t, routes, 2)
	tool := routes[1]
	assert.Equal(t, "", tool.Path)
	require.NotNil(t, tool.Auth)
	// 会话绑定（H4）：ForwardAuth 地址必须携带本会话的 ?session=，authorize
	// 面据此拒绝他会话的 cookie；entry 路由不带 Auth（上面已断言 nil）。
	assert.Equal(t, tBrowseGateway+"/v1/browse/authorize?session="+tBrowseSession, tool.Auth.Address)
	assert.NotEqual(t, "", tool.BackendAddr)
	assert.Equal(t, "browse-"+strings.ToLower(tBrowseSession)+"."+tBrowseHostSufx, tool.Host)
}

// TestBrowseGrantLifecycle：grant 兑换（entry 面）→ cookie 校验（authorize
// 面）→ 错值拒绝（常量时间闸的行为面）。
func TestBrowseGrantLifecycle(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	b.Step(ctx) // 采纳进注册表

	sid, grant, ok := splitGrant3(b.BrowseSessionGrant(tBrowseSession))
	require.True(t, ok)
	assert.Equal(t, tBrowseSession, sid)
	assert.NotEmpty(t, grant)
	assert.True(t, b.BrowseValidateGrant(tBrowseSession, grant))
	assert.False(t, b.BrowseValidateGrant(tBrowseSession, "wrong-grant-value"))
	assert.False(t, b.BrowseValidateGrant("01JDNOSUCH000000000000000A", grant))
	_ = ctx
}

// splitGrant3 拆 BrowseSessionGrant 的返回（测试助手）。
func splitGrant3(cookieValue string, maxAge int, ok bool) (sid, grant string, _ bool) {
	if !ok {
		return "", "", false
	}
	for i := 0; i < len(cookieValue); i++ {
		if cookieValue[i] == '.' {
			return cookieValue[:i], cookieValue[i+1:], true
		}
	}
	return "", "", false
}

// TestBrowseHardTTLReclaim：行到期 → Remove（第五轴 ns）+ 删行。
func TestBrowseHardTTLReclaim(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, rt, _, clock := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	b.Step(ctx)
	require.NotEmpty(t, rt.calls())

	clock.Advance(BrowseHardTTL + time.Minute)
	b.Step(ctx)

	rows, err := b.repos.Browse.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "expired session row must be deleted")
	assert.Contains(t, rt.removedSnapshot(), capability.NamespaceRef{Team: "default", Project: tProjectID, Browse: tBrowseSession})
}

// TestBrowseIdleReclaim：无接触超空闲窗 → 回收；entry/authorize 接触续活。
func TestBrowseIdleReclaim(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, _, _, clock := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	b.Step(ctx)

	// 半窗接触续活（grant 兑换即接触）。
	clock.Advance(browseIdleTTL / 2)
	_, _, ok := b.BrowseSessionGrant(tBrowseSession)
	require.True(t, ok)
	clock.Advance(browseIdleTTL / 2)
	b.Step(ctx)
	rows, err := b.repos.Browse.List(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "touched session stays within the idle window")

	// 全窗无接触 → 回收。
	clock.Advance(browseIdleTTL + time.Minute)
	b.Step(ctx)
	rows, err = b.repos.Browse.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "idle session must be reclaimed")
}

// TestBrowseRestartRecovery：注册表丢失（进程重启语义）→ 行恢复（grant
// 重铸——旧 cookie 失效）。P5 wave-3 拆包后注册表是应用模块内态，白盒
// 直取（原 e.sessions 注入形态同义）。
func TestBrowseRestartRecovery(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	b.Step(ctx)
	_, oldGrant, ok := splitGrant3(b.BrowseSessionGrant(tBrowseSession))
	require.True(t, ok)

	// 重启语义：注册表清空，行还在。
	b.mu.Lock()
	b.sessions = map[string]*browseSession{}
	b.mu.Unlock()
	b.Step(ctx)

	_, newGrant, ok := splitGrant3(b.BrowseSessionGrant(tBrowseSession))
	require.True(t, ok, "row must re-adopt into the registry")
	assert.NotEqual(t, oldGrant, newGrant, "grant is re-minted on recovery (old cookies invalid)")
	assert.False(t, b.BrowseValidateGrant(tBrowseSession, oldGrant), "stale grant must not validate")
}

// TestBrowseQuotaGate：per-Team 并发上限（受理面 quota 闸与计数）。
func TestBrowseQuotaGate(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	b, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	for i := 0; i < BrowseMaxSessionsPerTeam; i++ {
		_, err := b.RegisterBrowseSession(BrowseInput{
			SessionID: fmt.Sprintf("01JDBRWSE00000000000000%02d", i), TeamID: "default",
			ProjectID: tProjectID, DatabaseID: tDatabaseID, DatabaseName: tDatabaseNm,
			EngineName: "postgres", ReadOnly: true,
		})
		require.NoError(t, err)
	}
	// 在册数已达上限：本受理（+1）超限拒绝；计数面对账。
	assert.ErrorIs(t, b.BrowseCheckQuota("default"), ErrBrowseQuota, "the next session exceeds the team quota")
	assert.Equal(t, BrowseMaxSessionsPerTeam, b.BrowseCountByTeam("default"))
}

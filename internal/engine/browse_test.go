package engine

// Browse 会话域测试（F3.6，ADR-0051）：收敛（方言投影/材料/第五轴 ns）/
// 双路由（entry 免门禁 + ForwardAuth 门禁）/grant 兑换与校验/硬 TTL 与
// 空闲回收/重启行恢复（grant 重铸）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	browserepo "github.com/fleetlyrun/fleetly/internal/state/browse"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

const tBrowseSessionID = "01JDBRWSE000000000000000"

// newBrowseFixture 组装 browse 收敛夹具：项目/网络/凭证 Secret +
// browse 台账行（database 行本体不必须——库名经 databaseNameOf 点查，
// 缺行时名字空但渲染走 Secret URL 的 host——方言断言面足够）。
func newBrowseFixture(t *testing.T, engineName, connectURL string, readOnly bool) (*Engine, *fakeRuntime, *browserepo.Session, *statertest.FakeClock) {
	t.Helper()
	db, clock := statertest.New(t)
	rt := newFakeRuntime()
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	e := New(Deps{DB: db, Runtime: rt, Cipher: cipher, Logger: discardLogger()}, Options{
		Browse: BrowseConfig{
			HostSuffix: "browse.test", GatewayURL: "http://127.0.0.1:9081", TLSMode: "none",
		},
	})
	ctx := context.Background()
	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock).Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := DBCredentialSecretName(tDatabaseName)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secret.New(clock).Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	// database 行（databaseNameOf 的点查目标——凭证 Secret 名的输入）。
	require.NoError(t, dbrepo.New(clock).Create(ctx, db.Runner(), &dbrepo.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseName,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	row := &browserepo.Session{
		ID: tBrowseSessionID, ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: engineName, ReadOnly: readOnly,
		CreatedAt: clock.Now().Format(time.RFC3339),
		ExpiresAt: clock.Now().Add(BrowseHardTTL).Format(time.RFC3339),
	}
	require.NoError(t, e.browseRepo.Create(ctx, db.Runner(), row))
	return e, rt, row, clock
}

// TestBrowseStepConverges：台账行 → 采纳 → Ensure（第五轴 ns + pgweb
// 钉版镜像 + pgpass 材料 + 项目网挂靠 + 寻址名）。
func TestBrowseStepConverges(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, rt, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()

	e.browseStep(ctx)
	e.browseStep(ctx) // 签名短路后幂等：不重投影不重 Ensure

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Browse: tBrowseSessionID}, last.NS)
	require.Len(t, last.ByID, 1)
	var w capability.Workload
	for _, x := range last.ByID {
		w = x
	}
	assert.Equal(t, tBrowseSessionID, w.ID)
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
	assert.Equal(t, BrowseDNSName(tBrowseSessionID), w.Addressing[0].Name)
}

// TestBrowseCapabilityRoutes：双路由形态——entry（免门禁，后端 =
// gateway）+ 工具路由（ForwardAuth 指向 gateway authorize；后端经
// Addresses 解析）。冷启动（无投影）时工具路由缺席（诚实 404 窗）。
func TestBrowseCapabilityRoutes(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()

	// 冷启动（已注册未收敛）：只有 entry 路由——工具后端解析不到投影
	// 期望集（诚实 404 窗，下拍收敛）。
	_, err := e.RegisterBrowseSession(BrowseInput{
		SessionID: tBrowseSessionID, TeamID: "default", ProjectID: tProjectID,
		DatabaseID: tDatabaseID, DatabaseName: tDatabaseName, EngineName: "postgres", ReadOnly: true,
	})
	require.NoError(t, err)
	routes := e.browseCapabilityRoutes(ctx)
	require.Len(t, routes, 1)
	assert.Equal(t, "/v1/browse/entry", routes[0].Path)
	assert.Equal(t, "127.0.0.1:9081", routes[0].BackendAddr)
	assert.Nil(t, routes[0].Auth)

	// 收敛后：双路由（fake runtime 的 Addresses 解析投影期望集）。
	e.browseStep(ctx)
	routes = e.browseCapabilityRoutes(ctx)
	require.Len(t, routes, 2)
	tool := routes[1]
	assert.Equal(t, "", tool.Path)
	require.NotNil(t, tool.Auth)
	assert.Equal(t, "http://127.0.0.1:9081/v1/browse/authorize", tool.Auth.Address)
	assert.NotEqual(t, "", tool.BackendAddr)
	assert.Equal(t, "browse-"+strings.ToLower(tBrowseSessionID)+".browse.test", tool.Host)
}

// TestBrowseGrantLifecycle：grant 兑换（entry 面）→ cookie 校验（authorize
// 面）→ 错值拒绝（常量时间闸的行为面）。
func TestBrowseGrantLifecycle(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, _, row, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	e.browseStep(ctx) // 采纳进注册表

	sid, grant, ok := splitGrant3(e.BrowseSessionGrant(tBrowseSessionID))
	require.True(t, ok)
	assert.Equal(t, tBrowseSessionID, sid)
	assert.NotEmpty(t, grant)
	assert.True(t, e.BrowseValidateGrant(tBrowseSessionID, grant))
	assert.False(t, e.BrowseValidateGrant(tBrowseSessionID, "wrong-grant-value"))
	assert.False(t, e.BrowseValidateGrant("01JDNOSUCH000000000000000A", grant))
	_ = row
}

// splitGrant 拆 BrowseSessionGrant 的返回（测试助手）。
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
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, rt, _, clock := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	e.browseStep(ctx)
	require.NotEmpty(t, rt.calls())

	clock.Advance(BrowseHardTTL + time.Minute)
	e.browseStep(ctx)

	rows, err := e.browseRepo.List(ctx, e.db.Runner())
	require.NoError(t, err)
	assert.Empty(t, rows, "expired session row must be deleted")
	removed := rt.removedSnapshot()
	assert.Contains(t, removed, capability.NamespaceRef{Team: "default", Project: tProjectID, Browse: tBrowseSessionID})
}

// TestBrowseIdleReclaim：无接触超空闲窗 → 回收；entry/authorize 接触续活。
func TestBrowseIdleReclaim(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, _, _, clock := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	e.browseStep(ctx)

	// 半窗接触续活（grant 兑换即接触）。
	clock.Advance(browseIdleTTLForTest() / 2)
	_, _, ok := e.BrowseSessionGrant(tBrowseSessionID)
	require.True(t, ok)
	clock.Advance(browseIdleTTLForTest() / 2)
	e.browseStep(ctx)
	rows, err := e.browseRepo.List(ctx, e.db.Runner())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "touched session stays within the idle window")

	// 全窗无接触 → 回收。
	clock.Advance(browseIdleTTLForTest() + time.Minute)
	e.browseStep(ctx)
	rows, err = e.browseRepo.List(ctx, e.db.Runner())
	require.NoError(t, err)
	assert.Empty(t, rows, "idle session must be reclaimed")
}

// browseIdleTTLForTest 暴露空闲窗常量（测试同包直用；命名转一层是让
// 引用点在 grep 里显形）。
func browseIdleTTLForTest() time.Duration { return browseIdleTTL }

// TestBrowseRestartRecovery：注册表丢失（进程重启语义）→ 行恢复（grant
// 重铸——旧 cookie 失效）。
func TestBrowseRestartRecovery(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL
	e, _, _, _ := newBrowseFixture(t, "postgres", url, true)
	ctx := context.Background()
	e.browseStep(ctx)
	_, oldGrant, ok := splitGrant3(e.BrowseSessionGrant(tBrowseSessionID))
	require.True(t, ok)

	// 重启语义：注册表清空，行还在。
	e.browse.sessions = map[string]*browseSession{}
	e.browseStep(ctx)

	_, newGrant, ok := splitGrant3(e.BrowseSessionGrant(tBrowseSessionID))
	require.True(t, ok, "row must re-adopt into the registry")
	assert.NotEqual(t, oldGrant, newGrant, "grant is re-minted on recovery (old cookies invalid)")
	assert.False(t, e.BrowseValidateGrant(tBrowseSessionID, oldGrant), "stale grant must not validate")
}

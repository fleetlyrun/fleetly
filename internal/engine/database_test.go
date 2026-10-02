package engine

// Database 域收敛环测试（ADR-0029 验收锚）：投影面（模板钉版/项目网/
// 寻址/卷/材料）、gen 重启安全语义（指纹未变同号重放）、网集变化推进
// gen 一次、凭证缺失的诚实失败、收口拆载体与缓存清理。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

const (
	tDatabaseID   = "01JD0DB000000000000000000"
	tDatabaseName = "shop"
)

// newDatabaseFixture 组装单库收敛夹具：项目/网络/凭证 Secret/数据库行。
func newDatabaseFixture(t *testing.T, engine, connectURL string) (*Engine, *fakeRuntime, *dbrepo.Database) {
	t.Helper()
	db, clock := statertest.New(t)
	rt := newFakeRuntime()
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	e := New(Deps{DB: db, Runtime: rt, Cipher: cipher, Logger: discardLogger()}, Options{})
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

	row := &dbrepo.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseName,
		Engine: engine, CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
	require.NoError(t, dbrepo.New(clock).Create(ctx, db.Runner(), row))
	return e, rt, row
}

// 收敛：Ensure 到 Database 域 ns + 模板钉版投影 + 凭证材料 + 卷挂载 +
// db-<id> 寻址。
func TestDatabaseReconcileConverges(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, _ := newDatabaseFixture(t, "postgres", url)
	ctx := context.Background()

	e.databaseStep(ctx)
	e.databaseStep(ctx) // 幂等：再次收敛同号重放

	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID}, last.NS)

	w := last.Spec["postgres"]
	require.NotNil(t, w, "workload keyed by template engine name")
	assert.Equal(t, tDatabaseID, w.ID)
	assert.Equal(t, "postgres:17-bookworm", w.Image)
	assert.Equal(t, []string{"default"}, w.Networks, "database attaches the project's active networks")
	assert.Equal(t, []capability.Address{{Name: DatabaseDNSName(tDatabaseID)}}, w.Addressing)
	require.Len(t, w.Volumes, 1)
	assert.Equal(t, tDatabaseName, w.Volumes[0].VolumeID, "volume name = database name formula")
	assert.Equal(t, "/var/lib/postgresql/data", w.Volumes[0].Target)
	assert.Equal(t, "/run/secrets/"+dbPasswordFile, w.Env["POSTGRES_PASSWORD_FILE"])
	assert.Equal(t, int64(1), w.Replicas)
	require.NotNil(t, w.Healthcheck)
	// 引擎原生 exec 探针（模板单源；通用 TCP 方言的 nc 假设不成立，
	// staging 真机实证 2026-10-02）。
	assert.Equal(t, []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"}, w.Healthcheck.Exec)
	// 密码文件材料（值来自连接串回读——单真源）。
	assert.Equal(t, []byte("secretpw"), last.Materials.SecretFiles[dbPasswordFile])
	// 首挂钉住合并进 Placement（fake 集群默认一节点可用）。
	assert.Equal(t, []string{"01JD0NODE00000000000000000"}, w.Placement.NodeIDs)

	// 行状态推进：gen=1 落行（重读行——本地结构不随环推进）；观测
	// running 后状态翻 running。
	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), fresh.Generation)
	assert.NotEmpty(t, fresh.SpecFingerprint, "spec fingerprint persisted alongside the generation")
	e.obsMu.Lock()
	e.observations[tDatabaseID] = capability.WorkloadEvent{
		WorkloadID: tDatabaseID, Generation: 1, State: capability.WorkloadRunning,
	}
	e.obsMu.Unlock()
	e.databaseStep(ctx)
	fresh, err = e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, dbrepo.StatusRunning, fresh.Status)
}

// gen 重启安全语义：指纹未变多 tick 同号重放（载体不滚）；网集变化推进
// gen 一次后在新值稳定（managed_edge_test 钉死语义的用户域版）。
func TestDatabaseGenerationStableAcrossTicks(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, _ := newDatabaseFixture(t, "postgres", url)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		e.databaseStep(ctx)
	}
	calls := rt.calls()
	require.Len(t, calls, 4)
	for _, c := range calls {
		assert.Equal(t, calls[0].Gen, c.Gen, "unchanged projection must replay the same generation")
	}

	// 新项目网 → 网集变化 → gen 恰好推进一次，随后稳定。
	require.NoError(t, networkrepo.New(e.db.Clock()).Create(ctx, e.db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000002", ProjectID: tProjectID, Name: "internal",
	}))
	for i := 0; i < 3; i++ {
		e.databaseStep(ctx)
	}
	calls = rt.calls()
	require.Len(t, calls, 7)
	assert.Greater(t, calls[4].Gen, calls[3].Gen, "network-set change must advance the generation")
	assert.Equal(t, calls[4].Gen, calls[5].Gen)
	assert.Equal(t, calls[4].Gen, calls[6].Gen)
	assert.Len(t, calls[6].Spec["postgres"].Networks, 2)
}

// redis 模板：requirepass 配置文件材料 + 干净 argv（不落明文）。
func TestDatabaseRedisTemplateMaterials(t *testing.T) {
	const url = "redis://:redispw@db-01jd0db000000000000000000:6379/0"
	e, rt, _ := newDatabaseFixture(t, "redis", url)
	ctx := context.Background()

	e.databaseStep(ctx)
	calls := rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	w := last.Spec["redis"]
	assert.Equal(t, "redis:7.4", w.Image)
	assert.Equal(t, []string{"redis-server", "/run/secrets/" + dbRedisConfFile}, w.Command)
	conf := string(last.Materials.SecretFiles[dbRedisConfFile])
	assert.Contains(t, conf, "requirepass redispw")
	assert.Contains(t, conf, "appendonly yes")
	assert.Equal(t, []string{"redis-cli", "-p", "6379", "ping"}, w.Healthcheck.Exec)
}

// pgvector 模板：上游镜像 + 首启建扩展的 init 脚本命令。
func TestDatabasePgvectorTemplateRenders(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, _ := newDatabaseFixture(t, "pgvector", url)
	ctx := context.Background()

	e.databaseStep(ctx)
	calls := rt.calls()
	require.NotEmpty(t, calls)
	w := calls[len(calls)-1].Spec["pgvector"]
	assert.Equal(t, "pgvector/pgvector:0.8.6-pg17-bookworm", w.Image)
	require.Len(t, w.Command, 3)
	assert.Contains(t, w.Command[2], "CREATE EXTENSION IF NOT EXISTS vector")
	assert.Contains(t, w.Command[2], "docker-entrypoint.sh")
}

// 凭证 Secret 缺失 = 诚实失败：不 Ensure（载体保持现状），不推进 gen。
func TestDatabaseCredentialMissingBlocksEnsure(t *testing.T) {
	e, rt, row := newDatabaseFixture(t, "postgres", "postgresql://u:p@h:5432/d")
	ctx := context.Background()
	require.NoError(t, secret.New(e.db.Clock()).SoftDelete(ctx, e.db.Runner(), tProjectID, row.CredentialsRef))

	e.databaseStep(ctx)
	assert.Empty(t, rt.calls(), "credential failure must not reach Ensure")

	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), fresh.Generation)
	assert.Equal(t, dbrepo.StatusPending, fresh.Status)
}

// 收口：Runtime.Remove 到 Database 域 + 归属/期望/观测缓存清理。
func TestTeardownDatabaseRemovesCarriers(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, _ := newDatabaseFixture(t, "postgres", url)
	ctx := context.Background()
	e.databaseStep(ctx)
	require.NotEmpty(t, rt.calls())

	require.NoError(t, e.TeardownDatabase(ctx, tDatabaseID))
	ns := capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID}
	require.Contains(t, rt.removedSnapshot(), ns)

	e.obsMu.RLock()
	_, hasObs := e.observations[tDatabaseID]
	_, hasOwner := e.workloadApp[tDatabaseID]
	e.obsMu.RUnlock()
	assert.False(t, hasObs)
	assert.False(t, hasOwner)
	e.expectMu.Lock()
	_, hasExpected := e.expected[databaseDomainKeyPrefix+tDatabaseID]
	e.expectMu.Unlock()
	assert.False(t, hasExpected)

	// 不存在/已删：NotFound（对齐 TeardownApp 口径）。
	err := e.TeardownDatabase(ctx, "01JD0MISSING0000000000000X")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// 连接串铸造 ↔ 密码回读（单真源往返）。
func TestDatabaseConnectionURLRoundTrip(t *testing.T) {
	pg, err := DatabaseConnectionURL("postgres", tDatabaseID, "p: w@rd")
	require.NoError(t, err)
	assert.Equal(t,
		fmt.Sprintf("postgresql://fleetly:p%%3A%%20w%%40rd@%s:5432/fleetly", DatabaseDNSName(tDatabaseID)), pg)
	password, err := dbPasswordFromURL(pg)
	require.NoError(t, err)
	assert.Equal(t, "p: w@rd", password)

	rd, err := DatabaseConnectionURL("redis", tDatabaseID, "redispw")
	require.NoError(t, err)
	assert.Equal(t, "redis://:redispw@db-01jd0db000000000000000000:6379/0", rd)
	password, err = dbPasswordFromURL(rd)
	require.NoError(t, err)
	assert.Equal(t, "redispw", password)

	_, err = DatabaseConnectionURL("mysql", tDatabaseID, "x")
	assert.Error(t, err, "engine value domain is closed by the template registry")
}

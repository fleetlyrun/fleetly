package apitest_test

// DatabasesService e2e（F1.12，ADR-0029）：创建受理面（engine 值域/零网
// 项目/配额族之外的显性拒绝）、凭证 Secret 铸造与永不回显、收敛到
// Database 域载体、保留前缀的 PutSecret 拒绝、删除收口（载体拆除 + 卷与
// Secret 残留）、事件两拍。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// databaseFixture 组装：项目（default 网络随出生面自带，F-C），返回
// (h, ctx, projectID)。
func databaseFixture(t *testing.T) (*apitest.Harness, context.Context, string) {
	t.Helper()
	h := apitest.NewManual(t)
	return h, h.Ctx(), h.CreateProject(t, "shop").GetId()
}

func TestDatabaseLifecycle(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)

	// 值域外 engine → InvalidArgument（列合法值）。
	_, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "oracle",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "postgres",
	})
	require.NoError(t, err)
	row := created.GetDatabase()
	assert.NotEmpty(t, row.GetId())
	assert.Equal(t, "postgres", row.GetEngine())
	// B2a 起回显 = Default 版本短名（InfoFor 过渡形；B2c 改行值直读）。
	assert.Equal(t, "18", row.GetVersion())
	assert.Equal(t, "database:shop", row.GetCredentialsRef())
	assert.Equal(t, "db-"+strings.ToLower(row.GetId()), row.GetHost(), "engine-minted DNS name is lowercase")
	assert.Equal(t, int32(5432), row.GetPort())
	assert.Equal(t, "pending", row.GetStatus())

	// 凭证 Secret 已铸造（指纹面；值永不回显——响应无任何值字段）。
	secretList, err := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, secretList.GetSecrets(), 1)
	assert.Equal(t, "database:shop", secretList.GetSecrets()[0].GetName())
	assert.NotEmpty(t, secretList.GetSecrets()[0].GetFingerprint())

	// 同名活跃行 → AlreadyExists。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "redis",
	})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	// 保留前缀：PutSecret 覆写凭证 Secret → Conflict。
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{
		ProjectId: projectID, Name: "database:shop", Value: "hijack",
	})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "E_CONFLICT maps to FailedPrecondition on gRPC")

	// 收敛：Database 域载体（挂项目网 + 寻址）。
	h.Drive(ctx)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{
		Team: "default", Project: projectID, Database: row.GetId(), DatabaseName: "shop",
	}, last.NS)
	w := last.Spec["postgres"]
	require.NotNil(t, w)
	assert.Equal(t, []string{"default"}, w.Networks)

	// 观测 running → 状态列推进。
	h.Runtime.ReportRunning(row.GetId(), last.Gen)
	h.Drive(ctx)
	got, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "running", got.GetDatabase().GetStatus())

	// 卷行自愈补建（名 = 数据库名）。
	volList, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, volList.GetVolumes(), 1)
	assert.Equal(t, "shop", volList.GetVolumes()[0].GetName())

	// List 分页（单元素页面）。
	list, err := dbs.ListDatabases(ctx, &structurev1.ListDatabasesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, list.GetDatabases(), 1)
	assert.Equal(t, row.GetId(), list.GetDatabases()[0].GetId())
	after, err := dbs.ListDatabases(ctx, &structurev1.ListDatabasesRequest{
		ProjectId: projectID, AfterDatabaseSeq: row.GetSeq(),
	})
	require.NoError(t, err)
	assert.Empty(t, after.GetDatabases())

	// 删除收口：载体拆除 + tombstone；卷与凭证 Secret 残留（Project 级
	// 材料，备份保留义）。
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
		Team: "default", Project: projectID, Database: row.GetId(), DatabaseName: "shop",
	})

	_, err = dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err))
	// 再删 = 不存在（幂等口径）。
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err))

	secretList, err = secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, secretList.GetSecrets(), 1, "credential secret is retained after delete")
	volList, err = volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, volList.GetVolumes(), 1, "data volume is retained after delete")

	// tombstone 后同名可新建（ID 永不复用，新凭证覆写）。
	reborn, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "redis",
	})
	require.NoError(t, err)
	assert.NotEqual(t, row.GetId(), reborn.GetDatabase().GetId())
}

// 零网项目 → Conflict（可达性前置，fail-closed）。F-C 之后 API 创建路径
// 恒带出生 default 网络——零网只剩存量形态（出生面落地前的旧行、外部
// 写入），守卫作为 fail-closed 防线照旧有效，直插状态层构造两种形态。
func TestDatabaseCreateRequiresProjectNetwork(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	p := h.CreateProject(t, "lonely")

	createDB := func(projectID string) error {
		_, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: projectID, Name: "db", Engine: "redis",
		})
		return err
	}

	// 形态一：出生面之前的存量零网项目（直插状态层）。
	legacy := &project.Project{ID: "01JD0LEGACY000000000000000A", Name: "legacy-nonet", TeamID: "default"}
	require.NoError(t, projectrepo.New(h.DB.Clock(), h.DB.Runner()).Create(ctx, legacy))
	require.Error(t, createDB(legacy.ID), "legacy zero-network project must fail closed")
	assert.Equal(t, codes.FailedPrecondition, status.Code(createDB(legacy.ID)))

	// 形态二：出生 default 行被拆（无 API 删除面——直删复刻）。
	require.NoError(t, h.DB.Tx(ctx, func(run state.Runner) error {
		_, err := run.ExecContext(ctx, "DELETE FROM networks WHERE project_id = ?", p.GetId())
		return err
	}))
	require.Error(t, createDB(p.GetId()), "project with its default network removed must fail closed")
	assert.Equal(t, codes.FailedPrecondition, status.Code(createDB(p.GetId())))
}

// 事件两拍：database.created / database.deleted（structureEvent 载荷形态）。
func TestDatabaseEvents(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "redis",
	})
	require.NoError(t, err)
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: created.GetDatabase().GetId()})
	require.NoError(t, err)

	names := peerEventNames(t, ctx, h, "database", created.GetDatabase().GetId())
	assert.Equal(t, []string{"database.created", "database.deleted"}, names)
}

// Backup 动词面（F2.2，ADR-0039）：触发铸 pending 台账行、列表、pending
// 行 verify 拒绝、恢复受理位（挂起位落行 + 跨引擎/跨 Project 拒绝）。
// 执行链（工具容器 + ObjectStore）由 engine 环测与 dind 演练承载——
// apitest 面钉受理与回显契约。
func TestDatabaseBackupSurfaces(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "postgres",
	})
	require.NoError(t, err)
	dbID := created.GetDatabase().GetId()

	// 手动触发：pending 台账行（触发不等执行）。
	trig, err := dbs.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: dbID})
	require.NoError(t, err)
	assert.Equal(t, "pending", trig.GetBackup().GetStatus())
	assert.Equal(t, "postgres", trig.GetBackup().GetEngine())
	assert.Equal(t, "database", trig.GetBackup().GetKind(), "the ledger row carries the subject kind")
	assert.Equal(t, dbID, trig.GetBackup().GetSubjectId(), "the ledger row carries the subject id")

	// 列表：行在场（新→旧）。
	list, err := dbs.ListBackups(ctx, &structurev1.ListBackupsRequest{DatabaseId: dbID})
	require.NoError(t, err)
	require.Len(t, list.GetBackups(), 1)
	assert.Equal(t, trig.GetBackup().GetId(), list.GetBackups()[0].GetId())

	// pending 行 verify → InvalidArgument（成功行才可验）。
	_, err = dbs.VerifyBackup(ctx, &structurev1.VerifyBackupRequest{BackupId: trig.GetBackup().GetId()})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// 跨 Project 恢复源 → InvalidArgument（源 Project 内是唯一恢复面）。
	other := h.CreateProject(t, "other")
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: other.GetId(), Name: "restored", Engine: "postgres",
		RestoreFromBackup: trig.GetBackup().GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "cross-project restore source must be rejected")
}

// TestDatabaseVolumeOriginTwoStates（ADR-0097 决策 4 验收锚·apitest 两态）：
// 同名重建形态（卷行保留）下，运行侧卷携带异源实例标记 → 受理 E_CONFLICT
// （FailedPrecondition，文案指清理/换名）；无标记（预建/无标记存量卷）→
// 受理通过且收敛环铸记收编（FakeRuntime 的 claim 假面回显同源标记）。
func TestDatabaseVolumeOriginTwoStates(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	// 首代库：收敛（claim 铸记）→ 删除（卷行保留——同名重建的存量面）。
	first, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "postgres",
	})
	require.NoError(t, err)
	h.Drive(ctx)
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: first.GetDatabase().GetId()})
	require.NoError(t, err)

	// 异源态：运行侧卷携带他源标记 → 受理即拒（E_CONFLICT 面）。
	h.Runtime.SetVolumeOriginMarker([]byte(`{"database_id":"01jd0fake00000000000000000","minted_at":"2026-11-04T12:00:00Z"}`))
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "postgres",
	})
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "foreign origin maps to E_CONFLICT / FailedPrecondition")
	assert.Contains(t, err.Error(), "previous database instance")

	// 同源/无标记态：清覆写（claim 假面回显同源标记）→ 受理过 + 收敛。
	h.Runtime.SetVolumeOriginMarker(nil)
	reborn, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "postgres",
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.GetDatabase().GetId(), reborn.GetDatabase().GetId(), "ids are never reused")
	h.Drive(ctx)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	w := last.Spec["postgres"]
	require.NotNil(t, w)
	require.Len(t, w.Volumes, 1)
	volList, err := structurev1.NewVolumesServiceClient(h.Conn).ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, volList.GetVolumes(), 1)
	assert.Equal(t, volList.GetVolumes()[0].GetId(), w.Volumes[0].VolumeID,
		"carrier key = volume row id (runtime carries zero names, ADR-0097)")
}

// TestDatabaseCreateVersionSettingsResources（B2c，ADR-0094 决策 3 /
// ADR-0095 决策 1/7）：version/settings/resources 三受理面——显式版本落行、
// 未知版本拒（列合法值）、旋钮回显、白名单外旋钮拒（精确报错）、资源
// 回显与超限拒、缺省形态（无三字段 = Default + 无旋钮 + 无限制）。
func TestDatabaseCreateVersionSettingsResources(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	// 显式版本 + 旋钮 + 资源：行值直读回显（version/settings/resources）。
	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "tuned", Engine: "postgres", Version: "17",
		Settings:  map[string]string{"max_connections": "200", "work_mem": "8mb"},
		Resources: &structurev1.DatabaseResources{CpuMillis: 2000, MemoryMb: 1024},
	})
	require.NoError(t, err)
	row := created.GetDatabase()
	assert.Equal(t, "17", row.GetVersion())
	assert.Equal(t, map[string]string{"max_connections": "200", "work_mem": "8mb"}, row.GetSettings())
	assert.Equal(t, int32(2000), row.GetResources().GetCpuMillis())
	assert.Equal(t, int32(1024), row.GetResources().GetMemoryMb())

	// 资源面投影：收敛环把行值折进载体上限（nil = 无限制 = 现状）。
	h.Drive(ctx)
	calls := h.Runtime.Calls()
	var tuned *capability.Workload
	for i := range calls {
		if w, ok := calls[i].Spec["postgres"]; ok && calls[i].NS.Database == row.GetId() {
			ww := w
			tuned = &ww
		}
	}
	require.NotNil(t, tuned, "the tuned database must converge through Ensure")
	require.NotNil(t, tuned.Resources)
	assert.Equal(t, int64(2000), tuned.Resources.CPUMillis)
	assert.Equal(t, int64(1024), tuned.Resources.MemoryMB)

	// 缺省形态：无三字段 = Default 版本 + 无旋钮 + 无资源限制。
	plain, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "plain", Engine: "redis",
	})
	require.NoError(t, err)
	assert.Equal(t, "8.2", plain.GetDatabase().GetVersion())
	assert.Empty(t, plain.GetDatabase().GetSettings())
	assert.Nil(t, plain.GetDatabase().GetResources())

	// 未知版本：InvalidArgument（文案列合法值）。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "novel", Engine: "postgres", Version: "19",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "available")

	// 白名单外旋钮：ValidateSettings 精确报错（平台 owned 键不可达）。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "rogue", Engine: "postgres",
		Settings: map[string]string{"listen_addresses": "0.0.0.0"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "not a registered knob")

	// 范围外旋钮值：精确报错带合法范围。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "over", Engine: "postgres",
		Settings: map[string]string{"max_connections": "999999"},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "outside the allowed range")

	// 资源超限：自定合理界（64 vCPU / 256 GiB）。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "huge", Engine: "postgres",
		Resources: &structurev1.DatabaseResources{MemoryMb: 999999},
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "resources.memory_mb")
}

// succeedBackupRow 把 pending 台账行推到 succeeded 有产物形态（恢复源受理
// 前置；执行链不在 apitest 装配面——SQL 直改复刻既有先例）。
func succeedBackupRow(t *testing.T, h *apitest.Harness, backupID string) {
	t.Helper()
	require.NoError(t, h.DB.Tx(h.Ctx(), func(run state.Runner) error {
		_, err := run.ExecContext(h.Ctx(),
			`UPDATE backups SET status = 'succeeded', object_key = ?, digest = ?, size_bytes = 1, finished_at = '2026-11-05T00:00:00Z' WHERE id = ?`,
			"backups/test/"+backupID, "sha256:"+strings.Repeat("a", 64), backupID)
		return err
	}))
}

// TestDatabaseRestoreCompatAdmission（B2c，ADR-0094 决策 5 / design §2.4）：
// 恢复源版本轴——降级拒（18 备份进 16 库）、同版本放行、旧行空
// engine_version 同引擎放行 + 响应注记「版本未记录，降级风险自负」（红队
// A10）。
func TestDatabaseRestoreCompatAdmission(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	src, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "src", Engine: "postgres", Version: "18",
	})
	require.NoError(t, err)
	trig, err := dbs.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: src.GetDatabase().GetId()})
	require.NoError(t, err)
	// 铸账点落值：备份行携带源库行版本（RestoreCompat 锚）。
	assert.Equal(t, "18", trig.GetBackup().GetEngineVersion())
	backupID := trig.GetBackup().GetId()
	succeedBackupRow(t, h, backupID)

	// 降级拒：18 源进 16 目标。
	_, err = dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "downgraded", Engine: "postgres", Version: "16",
		RestoreFromBackup: backupID,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Contains(t, err.Error(), "downgrade")

	// 同版本放行（挂起恢复）。
	same, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "samever", Engine: "postgres", Version: "18",
		RestoreFromBackup: backupID,
	})
	require.NoError(t, err)
	assert.Equal(t, "18", same.GetDatabase().GetVersion())
	assert.Equal(t, backupID, same.GetDatabase().GetRestoreFromBackup())
	assert.Empty(t, same.GetRestoreVersionNote(), "a versioned backup source carries no unverified-version note")

	// 升向放行（16 源进 18 目标——直改台账行复刻 16 源）。
	require.NoError(t, h.DB.Tx(ctx, func(run state.Runner) error {
		_, err := run.ExecContext(ctx, `UPDATE backups SET engine_version = '16' WHERE id = ?`, backupID)
		return err
	}))
	up, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "upgraded", Engine: "postgres", Version: "18",
		RestoreFromBackup: backupID,
	})
	require.NoError(t, err, "an upgrade-direction restore is admitted (ADR-0094 decision 5)")
	assert.Empty(t, up.GetRestoreVersionNote())

	// 旧行空 engine_version：同引擎放行 + 注记（红队 A10）。
	require.NoError(t, h.DB.Tx(ctx, func(run state.Runner) error {
		_, err := run.ExecContext(ctx, `UPDATE backups SET engine_version = '' WHERE id = ?`, backupID)
		return err
	}))
	legacy, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "legacy-src", Engine: "postgres", Version: "18",
		RestoreFromBackup: backupID,
	})
	require.NoError(t, err, "an unversioned legacy backup is admitted same-engine (design 2.4)")
	assert.Contains(t, legacy.GetRestoreVersionNote(), "downgrade risk is yours",
		"the response must flag the unverified compatibility of a legacy backup")
}

// TestDataEnginesCatalog（B2c，ADR-0094 决策 4 / design §2.3）：Engine
// Catalog——服务端注册表现场装配（database 类型全员；版本矩阵含默认标记、
// 旋钮 schema、能力自描述；dialect = engine 自名；digest 不出目录）。
func TestDataEnginesCatalog(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	engines := structurev1.NewDataEnginesServiceClient(h.Conn)

	resp, err := engines.ListDataEngines(ctx, &structurev1.ListDataEnginesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEngines(), 9, "five database engines + nats/kafka/redpanda (pubsub kind, B4b/B6a/B6b) + silo (bucketstore kind, B5)")

	byName := map[string]*structurev1.EngineCatalog{}
	for _, e := range resp.GetEngines() {
		byName[e.GetEngine()] = e
	}
	pg := byName["postgres"]
	require.NotNil(t, pg)
	assert.Equal(t, "database", pg.GetKind())
	assert.Equal(t, "postgres", pg.GetDialect(), "database kind dialect is the engine's own name")
	assert.Equal(t, int32(5432), pg.GetPort())
	require.Len(t, pg.GetVersions(), 3)
	assert.Equal(t, "16", pg.GetVersions()[0].GetId())
	assert.True(t, pg.GetVersions()[2].GetDefault())
	assert.False(t, pg.GetVersions()[2].GetDeprecated())
	require.NotEmpty(t, pg.GetKnobs())
	assert.Equal(t, "max_connections", pg.GetKnobs()[1].GetKey())
	assert.Equal(t, "int", pg.GetKnobs()[1].GetKind())
	assert.Equal(t, "static", pg.GetKnobs()[1].GetClass())
	assert.True(t, pg.GetCapabilities().GetBackup())
	assert.True(t, pg.GetCapabilities().GetBrowse())
	assert.True(t, pg.GetCapabilities().GetRestoreCompat())
	assert.False(t, pg.GetCapabilities().GetMigrate(), "the cross-engine transfer contract (design 4.5) is a later batch - honest default")

	// pubsub 类型入目录（B4b，ADR-0096）：nats 自名方言、browse 拒、backup 在册。
	nats := byName["nats"]
	require.NotNil(t, nats)
	assert.Equal(t, "pubsub", nats.GetKind())
	assert.Equal(t, "nats", nats.GetDialect())
	assert.Equal(t, int32(4222), nats.GetPort())
	require.Len(t, nats.GetVersions(), 2)
	assert.Equal(t, "2.11", nats.GetVersions()[1].GetId())
	assert.True(t, nats.GetVersions()[1].GetDefault())
	assert.True(t, nats.GetCapabilities().GetBackup())
	assert.False(t, nats.GetCapabilities().GetBrowse(), "no browse tool for nats (ADR-0096 decision 4)")
	require.Len(t, nats.GetKnobs(), 2)
	assert.Equal(t, "static", nats.GetKnobs()[0].GetClass())

	// pubsub 类型 kafka 协议族第二实现（B6b，ADR-0096 决策 7）：redpanda
	// dialect=kafka 同族互示（客户端零改动切换）、能力位全 false 诚实缺省
	// （无数据备份面挂账——kafka 同族同裁）、BSL 1.1 license_note 披露。
	rp := byName["redpanda"]
	require.NotNil(t, rp)
	assert.Equal(t, "pubsub", rp.GetKind())
	assert.Equal(t, "kafka", rp.GetDialect(), "redpanda shares the kafka dialect family (dialect family mechanism)")
	assert.Equal(t, int32(9092), rp.GetPort())
	require.Len(t, rp.GetVersions(), 2)
	assert.Equal(t, "26.1", rp.GetVersions()[0].GetId())
	assert.Equal(t, "26.2", rp.GetVersions()[1].GetId())
	assert.True(t, rp.GetVersions()[1].GetDefault())
	assert.False(t, rp.GetCapabilities().GetBackup(), "redpanda backup face is deferred (kafka-family ruling)")
	assert.False(t, rp.GetCapabilities().GetMigrate(), "backup=false implies migrate=false")
	assert.False(t, rp.GetCapabilities().GetRestoreCompat())
	require.Len(t, rp.GetKnobs(), 2)
	assert.Equal(t, "log_retention_ms", rp.GetKnobs()[0].GetKey())
	assert.Equal(t, "dynamic", rp.GetKnobs()[0].GetClass())
	assert.Contains(t, rp.GetLicenseNote(), "BSL 1.1")
	assert.Contains(t, rp.GetLicenseNote(), "4 years")

	// bucketstore 类型入目录（B5，ADR-0096）：silo 单版 Default、dialect=s3
	// 自名、能力位全 false（转储挂账诚实缺省）、AGPLv3 license_note 披露。
	silo := byName["silo"]
	require.NotNil(t, silo)
	assert.Equal(t, "bucketstore", silo.GetKind())
	assert.Equal(t, "s3", silo.GetDialect(), "S3 dialect kind word (compatible implementations share it)")
	assert.Equal(t, int32(9000), silo.GetPort())
	require.Len(t, silo.GetVersions(), 1)
	assert.Equal(t, "2026-10", silo.GetVersions()[0].GetId())
	assert.True(t, silo.GetVersions()[0].GetDefault(), "the single version is the default")
	assert.False(t, silo.GetCapabilities().GetBackup(), "silo backup face is deferred (ADR-0096 decision 6)")
	assert.False(t, silo.GetCapabilities().GetMigrate(), "backup=false implies migrate=false")
	assert.False(t, silo.GetCapabilities().GetBrowse())
	assert.Empty(t, silo.GetKnobs(), "silo v1 carries no knobs")
	assert.Contains(t, silo.GetLicenseNote(), "AGPLv3")

	// pgvector 复合短名入目录（二维矩阵的用户面表达）。
	vec := byName["pgvector"]
	require.NotNil(t, vec)
	assert.Equal(t, "0.8-pg17", vec.GetVersions()[0].GetId())
	assert.True(t, vec.GetVersions()[1].GetDefault())
}

// driveDatabaseRunning 把库行推到 running（收敛首拍 + 观测回推；Update
// 受理门需要 running，红队 A6）。
func driveDatabaseRunning(t *testing.T, h *apitest.Harness, dbs structurev1.DatabasesServiceClient, dbID string) {
	t.Helper()
	h.Drive(h.Ctx())
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	var lastGen uint64
	for i := range calls {
		if _, ok := calls[i].Spec["postgres"]; ok && calls[i].NS.Database == dbID {
			lastGen = uint64(calls[i].Gen)
		}
	}
	require.NotZero(t, lastGen, "the database must have converged through Ensure")
	h.Runtime.ReportRunning(dbID, capability.Generation(lastGen))
	h.Drive(h.Ctx())
	got, err := dbs.GetDatabase(h.Ctx(), &structurev1.GetDatabaseRequest{Id: dbID})
	require.NoError(t, err)
	require.Equal(t, "running", got.GetDatabase().GetStatus(), "fixture: the row must reach running before the update face")
}

// settingsUtilityCalls 过滤 dynamic 热应用面的 utility 调用流水
// （dbsettings- 前缀——卷源标记门调用被排除）。
func settingsUtilityCalls(h *apitest.Harness) []capability.UtilityRequest {
	var out []capability.UtilityRequest
	for _, r := range h.Runtime.UtilityRequests() {
		if strings.HasPrefix(r.ID, "dbsettings-") {
			out = append(out, r)
		}
	}
	return out
}

// TestDatabaseUpdateDualLane（B2d，ADR-0095 决策 5/8/9 验收锚·apitest）：
// 预览（diff + 车道判定 + 滚动后果 + 铸票）→ 带票 Update（dynamic 同步
// 热应用先行：fake 工具容器执行序；static 落行 + Kick：Ensure 真实重发
// 且命令覆写面含新值）→ 烧票单用途 → 不可变轴/status 门/失败腿
// （E_SETTINGS_APPLY_FAILED 行不动）/database.updated 事件。
func TestDatabaseUpdateDualLane(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "tuned", Engine: "postgres", Version: "17",
		Settings: map[string]string{"work_mem": "16mb"},
	})
	require.NoError(t, err)
	dbID := created.GetDatabase().GetId()
	driveDatabaseRunning(t, h, dbs, dbID)
	ensures := len(h.Runtime.Calls())

	// —— 预览：diff + 车道判定 + 滚动后果 + 铸票（dynamic 全集 + static
	// = replace；资源面缺席 = 无限制）。——
	pv, err := dbs.PreviewUpdateDatabase(ctx, &structurev1.PreviewUpdateDatabaseRequest{Update: &structurev1.UpdateDatabaseRequest{
		DatabaseId: dbID,
		Settings:   &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "64mb", "max_connections": "300", "shared_buffers": "256mb"}},
	}})
	require.NoError(t, err)
	require.Len(t, pv.GetSettingsChanges(), 3)
	// 键序排序：max_connections < shared_buffers < work_mem。
	assert.Equal(t, "work_mem", pv.GetSettingsChanges()[2].GetKey())
	assert.Equal(t, "16mb", pv.GetSettingsChanges()[2].GetFrom())
	assert.Equal(t, "64mb", pv.GetSettingsChanges()[2].GetTo())
	assert.Equal(t, "dynamic", pv.GetSettingsChanges()[2].GetClass())
	assert.Equal(t, "max_connections", pv.GetSettingsChanges()[0].GetKey())
	assert.Equal(t, "static", pv.GetSettingsChanges()[0].GetClass())
	assert.Equal(t, []string{"work_mem"}, pv.GetDynamicKeys())
	assert.Equal(t, []string{"max_connections", "shared_buffers"}, pv.GetStaticKeys())
	assert.Equal(t, "replace", pv.GetRollout())
	assert.Nil(t, pv.GetResourcesFrom(), "no row resources = unlimited = absent message")
	assert.NotEmpty(t, pv.GetTicket())
	assert.Positive(t, pv.GetTicketExpiresIn())

	// dynamic-only 预览：zero_touch。
	pvDyn, err := dbs.PreviewUpdateDatabase(ctx, &structurev1.PreviewUpdateDatabaseRequest{Update: &structurev1.UpdateDatabaseRequest{
		DatabaseId: dbID,
		Settings:   &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "32mb"}},
	}})
	require.NoError(t, err)
	assert.Equal(t, "zero_touch", pvDyn.GetRollout())

	// —— 无票无 skip：E_CONFIRMATION_REQUIRED（拦截器强制集）。——
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{DatabaseId: dbID})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_REQUIRED", codeOf(t, err))

	// —— 不可变轴：engine/version 携带即拒（E_INVALID_ARGUMENT 指引文案）。——
	for _, bad := range []*structurev1.UpdateDatabaseRequest{
		{DatabaseId: dbID, Engine: "mysql", Confirmation: skipConfirm()},
		{DatabaseId: dbID, Version: "18", Confirmation: skipConfirm()},
	} {
		_, err = dbs.UpdateDatabase(ctx, bad)
		require.Error(t, err)
		assert.Equal(t, "E_INVALID_ARGUMENT", codeOf(t, err))
		assert.Contains(t, err.Error(), "immutable")
	}

	// —— 带票 Update：dynamic 先行（工具容器恰一次，ALTER SYSTEM 原生
	// 字面）→ static 落行 + Kick（下一拍 Ensure 真实重发、命令覆写面含
	// 新值）。——
	before := len(settingsUtilityCalls(h))
	up, err := dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Settings:     &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "64mb", "max_connections": "300", "shared_buffers": "256mb"}},
		Confirmation: &deliveryv1.Confirmation{Ticket: pv.GetTicket()},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"work_mem"}, up.GetDynamicApplied())
	assert.Equal(t, []string{"max_connections", "shared_buffers"}, up.GetStaticApplied())
	assert.Equal(t, map[string]string{"work_mem": "64mb", "max_connections": "300", "shared_buffers": "256mb"}, up.GetDatabase().GetSettings())

	applied := settingsUtilityCalls(h)[before:]
	require.Len(t, applied, 1)
	argv := strings.Join(applied[0].Argv, " ")
	assert.Contains(t, argv, "ALTER SYSTEM SET work_mem = '64MB'")
	assert.Contains(t, argv, "pg_reload_conf()")

	h.Drive(ctx)
	calls := h.Runtime.Calls()
	require.Len(t, calls, ensures+1, "the static lane must re-Ensure on the very next step (memo fourth segment)")
	cmd := calls[len(calls)-1].Spec["postgres"].Command
	assert.Contains(t, strings.Join(cmd, " "), "max_connections=300")
	assert.Contains(t, strings.Join(cmd, " "), "shared_buffers=256MB")
	assert.NotContains(t, strings.Join(cmd, " "), "work_mem", "dynamic knobs never enter the Workload command face (feed law)")

	// —— 票据单用途：同票二发 = 已确认过 MISMATCH。——
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Settings:     &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "64mb", "max_connections": "300", "shared_buffers": "256mb"}},
		Confirmation: &deliveryv1.Confirmation{Ticket: pv.GetTicket()},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_MISMATCH", codeOf(t, err))
	assert.Contains(t, err.Error(), "already redeemed")

	// —— 预览后世界漂移：旧票 MISMATCH（RequestDigest 绑合并后期望态。
	// 全量替换语义下携带 settings 的请求绑自身值集——settings 缺席的请求
	// （resources-only）才把行的当前 settings 织进票面：他方 Update 改行
	// settings 后同票即漂移）。——
	pvStale, err := dbs.PreviewUpdateDatabase(ctx, &structurev1.PreviewUpdateDatabaseRequest{Update: &structurev1.UpdateDatabaseRequest{
		DatabaseId: dbID,
		Resources:  &structurev1.DatabaseResources{CpuMillis: 1000},
	}})
	require.NoError(t, err)
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Settings:     &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "32mb"}},
		Confirmation: skipConfirm(),
	})
	require.NoError(t, err)
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Resources:    &structurev1.DatabaseResources{CpuMillis: 1000},
		Confirmation: &deliveryv1.Confirmation{Ticket: pvStale.GetTicket()},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_MISMATCH", codeOf(t, err))
	assert.Contains(t, err.Error(), "does not match the current desired state")

	// —— 失败腿：dynamic 热应用被拒 = E_SETTINGS_APPLY_FAILED，行不动。——
	h.Runtime.SetSettingsApplyError(errors.New("psql: FATAL: terminating connection due to administrator command"))
	before = len(settingsUtilityCalls(h))
	rowBefore, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: dbID})
	require.NoError(t, err)
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Settings:     &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"work_mem": "96mb"}},
		Confirmation: skipConfirm(),
	})
	require.Error(t, err)
	assert.Equal(t, "E_SETTINGS_APPLY_FAILED", codeOf(t, err))
	assert.Contains(t, err.Error(), "engine-boom-detail", "the engine's own reply tail rides the error")
	assert.Len(t, settingsUtilityCalls(h), before+1)
	rowAfter, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: dbID})
	require.NoError(t, err)
	assert.Equal(t, rowBefore.GetDatabase().GetSettings(), rowAfter.GetDatabase().GetSettings(), "a failed apply leaves the row untouched")
	h.Runtime.SetSettingsApplyError(nil) // 失败注入只作用于失败腿。

	// —— 资源面：skip 直发落行 + 回显；移除 = 无限制（消息缺席）。——
	res, err := dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   dbID,
		Resources:    &structurev1.DatabaseResources{CpuMillis: 2000, MemoryMb: 1024},
		Confirmation: skipConfirm(),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2000), res.GetDatabase().GetResources().GetCpuMillis())
	assert.Equal(t, int32(1024), res.GetDatabase().GetResources().GetMemoryMb())

	// —— 非运行行：E_CONFLICT（在滚窗/未就绪拒——红队 A6）。——
	fresh, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "pendingdb", Engine: "redis",
	})
	require.NoError(t, err)
	_, err = dbs.UpdateDatabase(ctx, &structurev1.UpdateDatabaseRequest{
		DatabaseId:   fresh.GetDatabase().GetId(),
		Settings:     &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"maxmemory": "256mb"}},
		Confirmation: skipConfirm(),
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFLICT", codeOf(t, err))
	assert.Contains(t, err.Error(), "requires a running database")

	// —— 事件面：database.updated 在册且已发射（database.* 族只增）。——
	names := peerEventNames(t, ctx, h, "database", dbID)
	assert.Contains(t, names, "database.updated")
}

// skipConfirm 是 Update 面的脚本形态确认（--yes 逃生门；governance 豁免
// 同 Deploy 族的 yesSkip）。
func skipConfirm() *deliveryv1.Confirmation { return &deliveryv1.Confirmation{Skip: true} }

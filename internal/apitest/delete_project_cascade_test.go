package apitest_test

// 项目删除级联数据库（2026-10-05 评审批台账 #4 / ADR-0029 追记）五路径：
// ①双库级联收口全断言（载体拆除 + 双 tombstone + 双 database.deleted +
// 项目 tombstone + 卷/凭证保留）；②teardown 失败注入——整体诚实失败带
// 精确错误、已拆库保持 tombstone、重试收敛成功；③空项目删除行为不变；
// ④冻结窗拒绝路径不变（级联零副作用）；⑤存量孤儿库的重跑收敛（runbook
// 记录·五 #3 换装指引的锚）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// cascadeFixture 组装：项目 + n 个（名, engine）库，收敛驱动一轮；返回
// (h, ctx, projectID, 库 ID 数组)。
func cascadeFixture(t *testing.T, nameEngine ...[2]string) (*apitest.Harness, context.Context, string, []string) {
	t.Helper()
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "cascade"})
	require.NoError(t, err)
	projectID := p.GetProject().GetId()
	ids := make([]string, 0, len(nameEngine))
	for _, ne := range nameEngine {
		created, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: projectID, Name: ne[0], Engine: ne[1],
		})
		require.NoError(t, err)
		ids = append(ids, created.GetDatabase().GetId())
	}
	h.Drive(ctx)
	return h, ctx, projectID, ids
}

// projectListed 报告 projectID 是否仍在 ListProjects 活跃面可见。
func projectListed(t *testing.T, projects structurev1.ProjectsServiceClient, ctx context.Context, projectID string) bool {
	t.Helper()
	list, err := projects.ListProjects(ctx, &structurev1.ListProjectsRequest{})
	require.NoError(t, err)
	for _, p := range list.GetProjects() {
		if p.GetId() == projectID {
			return true
		}
	}
	return false
}

// auditActions 收集指定 Action 前缀的审计行资源面（级联三链断言）。
func auditActions(t *testing.T, ctx context.Context, h *apitest.Harness, prefix string) map[string]bool {
	t.Helper()
	entries, err := identityv1.NewAuditQueryServiceClient(h.Conn).ListAudit(ctx, &identityv1.ListAuditRequest{Action: prefix, Limit: 100})
	require.NoError(t, err)
	out := map[string]bool{}
	for _, e := range entries.GetEntries() {
		out[e.GetResource()] = true
	}
	return out
}

// ①双库级联：两库载体拆除 + 两 tombstone + 两 database.deleted 事件 +
// 项目 tombstone；卷与凭证 Secret 保留（备份保留义）；审计三链齐
// （每库 database.delete + 项目 project.delete）。
func TestDeleteProjectCascadesDatabases(t *testing.T) {
	h, ctx, projectID, ids := cascadeFixture(t, [2]string{"orders", "postgres"}, [2]string{"cache", "redis"})
	require.Len(t, ids, 2)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)

	_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.NoError(t, err)

	// 载体拆除：两库的 Database 域 Remove 各一。
	removed := h.Runtime.Removed()
	for _, id := range ids {
		assert.Contains(t, removed, capability.NamespaceRef{
			Team: "default", Project: projectID, Database: id,
		}, "project delete must tear down every database carrier")
	}

	// 双 tombstone：读面统一 404；列表空。
	for _, id := range ids {
		_, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: id})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "E_NOT_FOUND")
	}
	list, err := dbs.ListDatabases(ctx, &structurev1.ListDatabasesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Empty(t, list.GetDatabases(), "no database rows may survive the project delete")

	// 事件：每库 created→deleted 两拍 + 项目 created→deleted 两拍。
	for _, id := range ids {
		assert.Equal(t, []string{"database.created", "database.deleted"},
			peerEventNames(t, ctx, h, "database", id))
	}
	assert.Equal(t, []string{"project.created", "project.deleted"},
		peerEventNames(t, ctx, h, "project", projectID))

	// 审计：每库 database.delete + 项目 project.delete（资源面精确对账）。
	dbAudits := auditActions(t, ctx, h, "database.delete")
	for _, id := range ids {
		assert.True(t, dbAudits["database/"+id], "each cascaded database must carry its own audit row")
	}
	projAudits := auditActions(t, ctx, h, "project.delete")
	assert.True(t, projAudits["project/"+projectID], "the project delete must be audited")

	// 项目 tombstone：活跃面不可见。
	assert.False(t, projectListed(t, projects, ctx, projectID), "deleted project must not be listed")

	// 卷与凭证 Secret 残留（Project 级材料，备份保留义——与单库删除同口径）。
	volList, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, volList.GetVolumes(), 2, "data volumes are retained after the cascaded delete")
	secretList, err := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	assert.Len(t, secretList.GetSecrets(), 2, "credential secrets are retained after the cascaded delete")
}

// ②失败注入：第二库（枚举序尾）teardown 失败 → 整体 E_INTERNAL 且错误带
// 库 ID；已拆的第一库保持 tombstone（事件在册）；项目存活；重试（注入
// 清除后）收敛成功。
func TestDeleteProjectCascadeFailureRetriesConverge(t *testing.T) {
	h, ctx, projectID, ids := cascadeFixture(t, [2]string{"first", "redis"}, [2]string{"second", "redis"})
	require.Len(t, ids, 2)
	// ListByProject 新→旧（ULID 创建序）：后建的 second 先枚举、先收口；
	// first 的 teardown 注入失败 → 级联在 second 之后中断。
	first, second := ids[0], ids[1]
	h.Runtime.SetRemoveErrFor(first, errors.New("injected docker api stall"))

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.Error(t, err)
	assert.Equal(t, "E_INTERNAL", appErrCode(t, err))
	require.Contains(t, err.Error(), first, "the failure must name the database that failed to tear down")
	require.Contains(t, err.Error(), "first", "the failure must carry the database name")

	// 已拆的库保持 tombstone（幂等收敛的锚）——事件与 404 在册。
	assert.Equal(t, []string{"database.created", "database.deleted"},
		peerEventNames(t, ctx, h, "database", second))
	_, err = dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: second})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_NOT_FOUND")
	// 失败库保持活跃（载体未拆——Remove 注入恒败）。
	_, err = dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: first})
	require.NoError(t, err, "the database whose teardown failed must stay operable for the retry")
	removed := h.Runtime.Removed()
	assert.NotContains(t, removed, capability.NamespaceRef{Team: "default", Project: projectID, Database: first})
	assert.Contains(t, removed, capability.NamespaceRef{Team: "default", Project: projectID, Database: second})
	// 项目存活（诚实失败——无半事务态静默）。
	assert.True(t, projectListed(t, projects, ctx, projectID), "a failed cascade must not tombstone the project")

	// 重试收敛：注入清除 → 删除成功，两库全收口、项目 tombstone。
	h.Runtime.SetRemoveErrFor(first, nil)
	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.NoError(t, err)
	for _, id := range ids {
		_, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: id})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "E_NOT_FOUND")
	}
	assert.False(t, projectListed(t, projects, ctx, projectID), "the retried delete must converge")
}

// ③空项目删除行为不变：零 Remove、无级联副作用，项目 tombstone 照旧。
func TestDeleteProjectWithoutDatabasesUnchanged(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "empty"})
	require.NoError(t, err)

	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: p.GetProject().GetId()})
	require.NoError(t, err)
	assert.Empty(t, h.Runtime.Removed(), "an empty project delete must not touch any carrier")
	assert.False(t, projectListed(t, projects, ctx, p.GetProject().GetId()))
	assert.Equal(t, []string{"project.created", "project.deleted"},
		peerEventNames(t, ctx, h, "project", p.GetProject().GetId()))
}

// ④冻结窗拒绝路径不变：DeleteProject 是冻结封禁面动词，拦截发生在
// handler 之前——级联零副作用（库不动、载体不拆），lift 后恢复。
func TestDeleteProjectCascadeFrozen(t *testing.T) {
	h, ctx, projectID, ids := cascadeFixture(t, [2]string{"frozen-db", "redis"})
	require.Len(t, ids, 1)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)

	const reason = "migration window"
	frozen, err := gov.SetChangeFreeze(ctx, &systemv1.SetChangeFreezeRequest{TeamId: "default", Reason: reason})
	require.NoError(t, err)

	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")
	assert.Contains(t, err.Error(), reason)

	// 零副作用：库存活、载体未拆、项目存活。
	_, err = dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: ids[0]})
	require.NoError(t, err)
	assert.Empty(t, h.Runtime.Removed())
	assert.True(t, projectListed(t, projects, ctx, projectID))

	// lift → 级联照常收敛。
	_, err = gov.LiftChangeFreeze(ctx, &systemv1.LiftChangeFreezeRequest{Id: frozen.GetFreeze().GetId()})
	require.NoError(t, err)
	_, err = projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.NoError(t, err)
	assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
		Team: "default", Project: projectID, Database: ids[0],
	})
}

// ⑤存量孤儿收敛（runbook 记录·五 #3 换装指引的锚）：级联落地之前已被
// tombstone 的项目（旧行为删除、库成孤儿）——新版重跑 projects delete 即
// 收敛（归属授权与 SoftDelete 幂等对已删行成立；枚举面是活跃库行）。
func TestDeleteProjectRetryConvergesOrphanDatabases(t *testing.T) {
	h, ctx, projectID, ids := cascadeFixture(t, [2]string{"orphan-a", "redis"}, [2]string{"orphan-b", "redis"})
	require.Len(t, ids, 2)

	// 复刻旧行为残局：项目已 tombstone、两库原样（直插状态层——无 API
	// 面可以绕过当前级联）。
	require.NoError(t, h.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE projects SET deleted_at = '2026-10-05T00:00:00Z' WHERE id = ?", projectID)
		return err
	}))

	projects := structurev1.NewProjectsServiceClient(h.Conn)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	_, err := projects.DeleteProject(ctx, &structurev1.DeleteProjectRequest{Id: projectID})
	require.NoError(t, err, "re-running the delete on a tombstoned project must converge its orphan databases")

	for _, id := range ids {
		_, err := dbs.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: id})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "E_NOT_FOUND")
		assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
			Team: "default", Project: projectID, Database: id,
		}, "orphan carriers must be torn down by the convergence retry")
	}
	assert.False(t, projectListed(t, projects, ctx, projectID))
}

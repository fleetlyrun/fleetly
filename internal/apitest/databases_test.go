package apitest_test

// DatabasesService e2e（F1.12，ADR-0029）：创建受理面（engine 值域/零网
// 项目/配额族之外的显性拒绝）、凭证 Secret 铸造与永不回显、收敛到
// Database 域载体、保留前缀的 PutSecret 拒绝、删除收口（载体拆除 + 卷与
// Secret 残留）、事件两拍。

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// databaseFixture 组装：项目（default 网络随出生面自带，F-C），返回
// (h, ctx, projectID)。
func databaseFixture(t *testing.T) (*apitest.Harness, context.Context, string) {
	t.Helper()
	h := apitest.NewManual(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	return h, ctx, p.GetProject().GetId()
}

func TestDatabaseLifecycle(t *testing.T) {
	h, ctx, projectID := databaseFixture(t)
	dbs := structurev1.NewDatabasesServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)

	// 值域外 engine → InvalidArgument（列合法值）。
	_, err := dbs.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
		ProjectId: projectID, Name: "shop", Engine: "mysql",
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
	assert.Equal(t, "17-bookworm", row.GetVersion())
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
		Team: "default", Project: projectID, Database: row.GetId(),
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
		ProjectId: projectID, AfterDatabaseId: row.GetId(),
	})
	require.NoError(t, err)
	assert.Empty(t, after.GetDatabases())

	// 删除收口：载体拆除 + tombstone；卷与凭证 Secret 残留（Project 级
	// 材料，备份保留义）。
	_, err = dbs.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
		Team: "default", Project: projectID, Database: row.GetId(),
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
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	p, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "lonely"})
	require.NoError(t, err)

	createDB := func(projectID string) error {
		_, err := structurev1.NewDatabasesServiceClient(h.Conn).CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: projectID, Name: "db", Engine: "redis",
		})
		return err
	}

	// 形态一：出生面之前的存量零网项目（直插状态层）。
	legacy := &project.Project{ID: "01JD0LEGACY000000000000000A", Name: "legacy-nonet", TeamID: "default"}
	require.NoError(t, project.New(h.DB.Clock()).Create(ctx, h.DB.Runner(), legacy))
	require.Error(t, createDB(legacy.ID), "legacy zero-network project must fail closed")
	assert.Equal(t, codes.FailedPrecondition, status.Code(createDB(legacy.ID)))

	// 形态二：出生 default 行被拆（无 API 删除面——直删复刻）。
	require.NoError(t, h.DB.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM networks WHERE project_id = ?", p.GetProject().GetId())
		return err
	}))
	require.Error(t, createDB(p.GetProject().GetId()), "project with its default network removed must fail closed")
	assert.Equal(t, codes.FailedPrecondition, status.Code(createDB(p.GetProject().GetId())))
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

package engine

// teamaxis_test.go 钉事件 Team 轴 fail-closed（架构评审 R14 收口）：资源轴
// Team 解析失败落 unresolvedTeam 哨兵，不落 ""（空串在 outbox 读侧 = 平台
// 级、对全部 Team 可见）。哨兵行对 Team 视角不可见、平台 owner（无过滤）
// 可取证；managed/browse 平台域仍显式留 ""。

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
)

// failingProjectRepo 只覆写 Get（注入 Project 归属解析故障）。
type failingProjectRepo struct {
	project.Repo
	err error
}

func (f failingProjectRepo) Get(context.Context, string) (*project.Project, error) {
	return nil, f.err
}

// failingAppRepo 只覆写 GetAnyByID（注入 App 归属解析故障）。
type failingAppRepo struct {
	app.Repo
	err error
}

func (f failingAppRepo) GetAnyByID(context.Context, string) (*app.App, error) {
	return nil, f.err
}

// failingDatabaseRepo 只覆写 Get（注入 Database 归属解析故障）。
type failingDatabaseRepo struct {
	database.Repo
	err error
}

func (f failingDatabaseRepo) Get(context.Context, string) (*database.Database, error) {
	return nil, f.err
}

// TestEventTeamResolutionFailsClosed：解析失败 → 哨兵（非空、非真实 Team）；
// 平台域（managed/browse）显式留 ""；未知归属域 fail-closed 落哨兵。
func TestEventTeamResolutionFailsClosed(t *testing.T) {
	e, _, _ := newTestEngineMutated(t, Options{}, discardLogger(), func(s *transact.Stores) {
		s.Projects = failingProjectRepo{Repo: s.Projects, err: errors.New("db busy")}
		s.Apps = failingAppRepo{Repo: s.Apps, err: errors.New("db busy")}
		s.Databases = failingDatabaseRepo{Repo: s.Databases, err: errors.New("db busy")}
	})
	ctx := context.Background()

	assert.NotEqual(t, "", unresolvedTeam, "the sentinel must never be the platform-visible empty team")
	assert.Equal(t, unresolvedTeam, e.eventTeamOfProject(ctx, e.repos, tProjectID))
	assert.Equal(t, unresolvedTeam, e.eventTeamOfApp(ctx, e.repos, tAppID))
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, appOwner(tAppID)))
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, databaseOwner("01JD0DB0000000000000000G")))
	// pubsub/bucketstore 是用户域资源（ADR-0096）：行缺失同样 fail-closed，
	// 不得因未建行而落到平台级可见。
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, pubsubOwner("01JD0PS0000000000000000G")))
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, bucketStoreOwner("01JD0BS0000000000000000G")))
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, workloadOwner{domain: 99, id: "unknown"}))

	// 平台域显式构造：managed/browse 无 owner Team，平台级可见是终态口径。
	assert.Equal(t, "", e.ownerTeam(ctx, systemOwner("fleetly-proxy")))
	assert.Equal(t, "", e.ownerTeam(ctx, browseOwner("01JD0BROWSE00000000000000G")))
}

// TestUnresolvedTeamRowsAreInvisibleToTeamScope：哨兵行对 Team 视角不可见
// （ListAfter/ListByAggregate/ListByAggregateIDs 三读径同锚），平台 owner
// （无过滤）全量可见。
func TestUnresolvedTeamRowsAreInvisibleToTeamScope(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	const id = "01JD0NODE000000000000000U"
	_, err := e.repos.Outbox.Append(ctx, unresolvedTeam, "node.joined", "node", id, []byte(`{}`))
	require.NoError(t, err)

	teamView, err := e.repos.Outbox.ListAfter(ctx, 0, 100, "default")
	require.NoError(t, err)
	for _, ev := range teamView {
		assert.NotEqual(t, id, ev.AggregateID, "a team-scoped list must not see unresolved-team rows")
	}

	ownerView, err := e.repos.Outbox.ListAfter(ctx, 0, 100, "")
	require.NoError(t, err)
	found := false
	for _, ev := range ownerView {
		if ev.AggregateID == id {
			found = true
		}
	}
	assert.True(t, found, "the platform owner (no filter) must see unresolved-team rows")

	byAgg, err := e.repos.Outbox.ListByAggregate(ctx, "default", "node", id, 0, 100)
	require.NoError(t, err)
	assert.Empty(t, byAgg, "the aggregate read path shares the same team filter")

	ownerByAgg, err := e.repos.Outbox.ListByAggregate(ctx, "", "node", id, 0, 100)
	require.NoError(t, err)
	require.Len(t, ownerByAgg, 1)

	byAggIDs, err := e.repos.Outbox.ListByAggregateIDs(ctx, "default", "node", []string{id}, 0, 100)
	require.NoError(t, err)
	assert.Empty(t, byAggIDs[id], "the aggregate-set read path shares the same team filter")

	ownerByAggIDs, err := e.repos.Outbox.ListByAggregateIDs(ctx, "", "node", []string{id}, 0, 100)
	require.NoError(t, err)
	require.Len(t, ownerByAggIDs[id], 1)
}

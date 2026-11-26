package engine

// teamaxis_test.go 钉事件 Team 轴 fail-closed（架构评审 R14 收口；批 1①b
// 起经登记表 + BindDomain 铸造域）：资源轴 Team 解析失败落 unresolvedTeam
// 哨兵，不落 ""（空串在 outbox 读侧 = 平台级、对全部 Team 可见）。哨兵行
// 对 Team 视角不可见、平台 owner（无过滤）可取证；managed/browse 平台域
// 仍显式留 ""（显式登记的平台闭包铸出，非失败路径）。「聚合已删但归属
// 未清」的边角逐域表驱动钉住：账本归属在场、聚合行缺席 → 哨兵。

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
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

// ownerOfHandle 读句柄固定的域身份（白盒：在册性断言要用与生产绑定同源的
// 域值铸 workloadOwner）。
func ownerOfHandle(h interface{ owner(string) workloadOwner }, id string) workloadOwner {
	return h.owner(id)
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
	// database 域经 BindDomain 铸造绑定（生产同源闭包）：行解析故障 → 哨兵。
	dbHandle := e.BindDomain(e.databaseTeamOf).(*domainObservations)
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, ownerOfHandle(dbHandle, "01JD0DB0000000000000000G")))
	// pubsub/bucketstore 是用户域资源（ADR-0096）：行缺失同样 fail-closed，
	// 不得因未建行而落到平台级可见。
	psHandle := e.BindDomain(e.pubsubTeamOf).(*domainObservations)
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, ownerOfHandle(psHandle, "01JD0PS0000000000000000G")))
	bsHandle := e.BindDomain(e.bucketStoreTeamOf).(*domainObservations)
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, ownerOfHandle(bsHandle, "01JD0BS0000000000000000G")))
	assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, workloadOwner{domain: 99, id: "unknown"}))

	// 平台域显式构造：managed/browse 无 owner Team，平台级可见是终态口径
	// （显式登记的空串闭包，区别于失败哨兵）。
	sysHandle := e.BindDomain(platformTeamOf).(*domainObservations)
	assert.Equal(t, "", e.ownerTeam(ctx, ownerOfHandle(sysHandle, "fleetly-proxy")))
	browseHandle := e.BindDomain(platformTeamOf).(*domainObservations)
	assert.Equal(t, "", e.ownerTeam(ctx, ownerOfHandle(browseHandle, "01JD0BROWSE00000000000000G")))
}

// TestStaleOwnerResolvesFailClosed：聚合已删但归属未清的边角（表驱动，
// 设计稿 §3.5 验收锚）——观测账本的归属条目在场、聚合行已删/缺席，载体
// 事件的资源轴 Team 解析必须 fail-closed 落哨兵（不降级平台级可见行）。
// 用户域三域逐域过账本全链：BindDomain → RecordOwners → ownerOf →
// ownerTeam。
func TestStaleOwnerResolvesFailClosed(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()

	t.Run("database", func(t *testing.T) {
		h := e.BindDomain(e.databaseTeamOf).(*domainObservations)
		h.RecordOwners("01JD0DBGONE00000000000000G", 1, []capability.Workload{{ID: "01JD0DBGONE00000000000000G", Generation: 1}})
		owner, ok := e.obs.ownerOf("01JD0DBGONE00000000000000G")
		require.True(t, ok, "the ledger holds the ownership entry")
		assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, owner), "a deleted aggregate must not demote the event to platform-visible")
	})

	t.Run("pubsub", func(t *testing.T) {
		h := e.BindDomain(e.pubsubTeamOf).(*domainObservations)
		h.RecordOwners("01JD0PSGONE00000000000000G", 1, []capability.Workload{{ID: "01JD0PSGONE00000000000000G", Generation: 1}})
		owner, ok := e.obs.ownerOf("01JD0PSGONE00000000000000G")
		require.True(t, ok)
		assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, owner))
	})

	t.Run("bucketstore", func(t *testing.T) {
		h := e.BindDomain(e.bucketStoreTeamOf).(*domainObservations)
		h.RecordOwners("01JD0BSGONE00000000000000G", 1, []capability.Workload{{ID: "01JD0BSGONE00000000000000G", Generation: 1}})
		owner, ok := e.obs.ownerOf("01JD0BSGONE00000000000000G")
		require.True(t, ok)
		assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, owner))
	})

	t.Run("app", func(t *testing.T) {
		// App 域：账本归属在场（经核 recordSpecs 播种）、App 行缺席。
		e.obs.recordSpecs(appOwner("01JD0APPGONE0000000000000G"), "dep-1", 1,
			[]capability.Workload{{ID: "01JD0APPGONE0000000000000G-web", Generation: 1}})
		owner, ok := e.obs.ownerOf("01JD0APPGONE0000000000000G-web")
		require.True(t, ok)
		assert.Equal(t, unresolvedTeam, e.ownerTeam(ctx, owner))
	})

	t.Run("platform domains stay visible", func(t *testing.T) {
		// 对照腿：平台域载体（受管）同等形态落空串（平台级可见是终态口径，
		// 不是失败降级）。
		h := e.BindDomain(platformTeamOf).(*domainObservations)
		h.RecordOwnersByWorkload(1, []capability.Workload{{ID: "managed-proxy", Process: "proxy", Generation: 1}})
		owner, ok := e.obs.ownerOf("managed-proxy")
		require.True(t, ok)
		assert.Equal(t, "", e.ownerTeam(ctx, owner))
	})
}

// TestBindDomainMintsDistinctDomains：每次铸造都是新域——两枚句柄铸不出
// 同一归属，账本互不可见（模块拿不到别域句柄的结构性证明）。
func TestBindDomainMintsDistinctDomains(t *testing.T) {
	e, _, _ := newTestEngine(t)
	a := e.BindDomain(platformTeamOf).(*domainObservations)
	b := e.BindDomain(platformTeamOf).(*domainObservations)
	assert.NotEqual(t, a.domain, b.domain, "each BindDomain call must mint a fresh domain identity")

	a.RecordExpected("row-a", 1)
	_, anchored := e.expect.expected[a.owner("row-a")]
	require.True(t, anchored, "the expected anchor is keyed by the minted domain")
	// b 域账本独立：a 的期望锚对 b 的清理面不可达（域判别在句柄内铸不出
	// 别域身份）。
	b.ForgetExpected("row-a")
	_, stillThere := e.expect.expected[a.owner("row-a")]
	assert.True(t, stillThere, "one handle must not reach another domain's ledger entries")
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

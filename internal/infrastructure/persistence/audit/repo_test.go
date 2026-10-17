package audit_test

// Audit 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Append 落账
// 铸造面（ID = aud-<seq>、CreatedAt 取时钟、Team 轴统一铸入点——Entry 未
// 显式携带时从 ctx 取、显式携带优先于 ctx、无标注 = '' 平台级行，
// ADR-0035）、ListFiltered 过滤矩阵（actor/source/action 前缀/resource 单
// 维与至少两维组合断言、action 前缀 LIKE 转义——% _ 字面义）、Team 轴
// 过滤口径（team 行 ∪ '' 平台行——ADR-0035 决策 6）、limit 钳位（<=0 或
// >1000 回落 1000）。只增聚合，无更新/删除面，不硬造。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

const (
	teamA = "01JD0TEAM000000000000000A1"
	teamB = "01JD0TEAM000000000000000B1"
)

// ids 摘出夹具行的 ID 序（断言新→旧次序的可读形态）。
func ids(entries []audit.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

// appendRow 逐行落夹具：teamID 为空走无标注 ctx（平台行），否则经
// WithTeam 注入（authn 拦截器统一铸入形态——本夹具不测显式优先，那条在
// AppendCasting 已断言）。
func appendRow(t *testing.T, repo audit.Repo, ctx context.Context, teamID string, e *audit.Entry) {
	t.Helper()
	if teamID != "" {
		ctx = audit.WithTeam(ctx, teamID)
	}
	require.NoError(t, repo.Append(ctx, e))
}

// TestAuditAppendCasting：落账铸造面——ID/seq/CreatedAt 由 repo 铸、Team
// 轴三形态（ctx 铸入/显式优先/无标注平台行）。
func TestAuditAppendCasting(t *testing.T) {
	db, clock := statetest.New(t)
	repo := auditrepo.New(clock, db.Runner())

	// Entry 未携带 TeamID → 从 ctx 铸入（authn 拦截器统一注入形态）。
	e1 := &audit.Entry{Actor: "alice", Source: audit.SourceAPI, Action: "project.create", Resource: "project/p1"}
	require.NoError(t, repo.Append(audit.WithTeam(context.Background(), teamA), e1))
	assert.Equal(t, "aud-1", e1.ID, "id is cast at the row assembly site (aud-<seq>, ADR-0070)")
	assert.Equal(t, int64(1), e1.Seq)
	assert.Equal(t, state.FormatTime(clock.Now()), e1.CreatedAt)
	assert.Equal(t, teamA, e1.TeamID)

	// 显式携带优先于 ctx（调用方自带归属的直注形态）。
	e2 := &audit.Entry{Actor: "bob", Source: audit.SourceCLI, Action: "volume.delete", Resource: "volume/v1", TeamID: teamB}
	require.NoError(t, repo.Append(audit.WithTeam(context.Background(), teamA), e2))
	assert.Equal(t, teamB, e2.TeamID, "explicit TeamID must win over the ctx annotation")

	// 无 ctx 标注且未携带 → '' 平台级行（system/无身份动作）。
	e3 := &audit.Entry{Actor: "", Source: audit.SourceSystem, Action: "node.reap", Resource: "node/n1"}
	require.NoError(t, repo.Append(context.Background(), e3))
	assert.Empty(t, e3.TeamID)

	// 回读铸造面。
	got, err := repo.List(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, audit.SourceSystem, got[0].Source, "source word survives the round trip")
	assert.Equal(t, "aud-3", got[0].ID)
}

// TestAuditListFilteredMatrix：过滤矩阵 + Team 轴口径 + limit 钳位。
func TestAuditListFilteredMatrix(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := auditrepo.New(clock, db.Runner())

	// 六行夹具：actor/source/action/resource/team 多轴交错（aud-1..6 落序）。
	// aud-1: alice/api/project.create/project/p1 @teamA（ctx 铸入）
	// aud-2: alice/cli/volume.delete/volume/v1   @teamB（ctx 铸入）
	// aud-3: bob/api/project.delete/project/p1   teamA（显式携带）
	// aud-4: system/node.reap/node/n1            ''（平台行）
	// aud-5: carol/manual/role.create/role/r1    teamA（显式携带）
	// aud-6: carol/manual/role_create/role/r2    teamB（ctx 铸入）
	appendRow(t, repo, ctx, teamA, &audit.Entry{Actor: "alice", Source: audit.SourceAPI, Action: "project.create", Resource: "project/p1"})
	appendRow(t, repo, ctx, teamB, &audit.Entry{Actor: "alice", Source: audit.SourceCLI, Action: "volume.delete", Resource: "volume/v1"})
	appendRow(t, repo, ctx, "", &audit.Entry{Actor: "bob", Source: audit.SourceAPI, Action: "project.delete", Resource: "project/p1", TeamID: teamA})
	appendRow(t, repo, ctx, "", &audit.Entry{Actor: "", Source: audit.SourceSystem, Action: "node.reap", Resource: "node/n1"})
	appendRow(t, repo, ctx, "", &audit.Entry{Actor: "carol", Source: audit.SourceManual, Action: "role.create", Resource: "role/r1", TeamID: teamA})
	appendRow(t, repo, ctx, teamB, &audit.Entry{Actor: "carol", Source: audit.SourceManual, Action: "role_create", Resource: "role/r2"})

	// 无过滤全量（新→旧）；limit 钳位：<=0 或 >1000 回落 1000（六行全回）。
	for _, limit := range []int{0, -3, 1001, 5000} {
		got, err := repo.List(ctx, limit)
		require.NoError(t, err)
		assert.Len(t, got, 6, "limit %d clamps into range and returns all rows", limit)
	}
	got, err := repo.List(ctx, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"aud-6", "aud-5"}, ids(got), "list is newest-first")

	// 单维：actor。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-2", "aud-1"}, ids(got))

	// 两维组合：actor + source。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice", Source: "cli"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-2"}, ids(got), "actor+source must intersect")

	// 两维组合：source + action 前缀。
	got, err = repo.ListFiltered(ctx, audit.Filter{Source: "api", ActionPrefix: "project."})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-3", "aud-1"}, ids(got))

	// 三维组合：actor + source + resource。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice", Source: "api", Resource: "project/p1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-1"}, ids(got))

	// action 前缀 LIKE 转义：前缀 "role_" 只命中字面下划线行——未转义的
	// LIKE 会把 role.create 的 "." 也当任意字符吞掉。
	got, err = repo.ListFiltered(ctx, audit.Filter{ActionPrefix: "role_"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-6"}, ids(got), "underscore in the prefix must match literally (escaped LIKE)")
	got, err = repo.ListFiltered(ctx, audit.Filter{ActionPrefix: "role."})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-5"}, ids(got), "dot in the prefix must match literally")

	// Team 轴（ADR-0035 决策 6）：team 行 ∪ '' 平台行，平台行对全部 Team 可见。
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamA})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-5", "aud-4", "aud-3", "aud-1"}, ids(got))
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamB})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-6", "aud-4", "aud-2"}, ids(got))

	// Team 轴再叠一维：team + actor。
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamA, Actor: "bob"})
	require.NoError(t, err)
	assert.Equal(t, []string{"aud-3"}, ids(got))
}

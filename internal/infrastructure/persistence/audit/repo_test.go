package audit_test

// Audit 聚合 repo hermetic 测试（G19 聚合带补齐）。覆盖面：Append 落账
// 铸造面（ID 铸 ULID（ADR-0092）、CreatedAt 取时钟、Team 轴统一铸入点——Entry 未
// 显式携带时从 ctx 取、显式携带优先于 ctx、无标注 = '' 平台级行，
// ADR-0035）、ListFiltered 过滤矩阵（actor/source/action 前缀/resource 单
// 维与至少两维组合断言、action 前缀 LIKE 转义——% _ 字面义）、Team 轴
// 过滤口径（team 行 ∪ '' 平台行——ADR-0035 决策 6）、limit 钳位（<=0 或
// >1000 回落 1000）、非事务面并发 Append（seq 唯一连续——R05-M1 复现）
// 与唯一冲突重试的成功/耗尽两径。只增聚合，无更新/删除面，不硬造。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
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
	assert.True(t, entityid.Valid(e1.ID), "id is minted at the row assembly site (ULID, ADR-0092)")
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
	assert.Equal(t, int64(2), e2.Seq)
	assert.Equal(t, int64(3), e3.Seq, "seq keeps counting per row (ADR-0070)")
	assert.NotEqual(t, e1.ID, e2.ID, "each minted row carries a distinct id")

	// 回读铸造面。
	got, err := repo.List(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, audit.SourceSystem, got[0].Source, "source word survives the round trip")
	assert.Equal(t, e3.ID, got[0].ID)
}

// TestAuditListFilteredMatrix：过滤矩阵 + Team 轴口径 + limit 钳位。
func TestAuditListFilteredMatrix(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	repo := auditrepo.New(clock, db.Runner())

	// 六行夹具：actor/source/action/resource/team 多轴交错（seq 1..6 落序）。
	// seq1: alice/api/project.create/project/p1 @teamA（ctx 铸入）
	// seq2: alice/cli/volume.delete/volume/v1   @teamB（ctx 铸入）
	// seq3: bob/api/project.delete/project/p1   teamA（显式携带）
	// seq4: system/node.reap/node/n1            ''（平台行）
	// seq5: carol/manual/role.create/role/r1    teamA（显式携带）
	// seq6: carol/manual/role_create/role/r2    teamB（ctx 铸入）
	e1 := &audit.Entry{Actor: "alice", Source: audit.SourceAPI, Action: "project.create", Resource: "project/p1"}
	appendRow(t, repo, ctx, teamA, e1)
	e2 := &audit.Entry{Actor: "alice", Source: audit.SourceCLI, Action: "volume.delete", Resource: "volume/v1"}
	appendRow(t, repo, ctx, teamB, e2)
	e3 := &audit.Entry{Actor: "bob", Source: audit.SourceAPI, Action: "project.delete", Resource: "project/p1", TeamID: teamA}
	appendRow(t, repo, ctx, "", e3)
	e4 := &audit.Entry{Actor: "", Source: audit.SourceSystem, Action: "node.reap", Resource: "node/n1"}
	appendRow(t, repo, ctx, "", e4)
	e5 := &audit.Entry{Actor: "carol", Source: audit.SourceManual, Action: "role.create", Resource: "role/r1", TeamID: teamA}
	appendRow(t, repo, ctx, "", e5)
	e6 := &audit.Entry{Actor: "carol", Source: audit.SourceManual, Action: "role_create", Resource: "role/r2"}
	appendRow(t, repo, ctx, teamB, e6)

	// 无过滤全量（新→旧）；limit 钳位：<=0 或 >1000 回落 1000（六行全回）。
	for _, limit := range []int{0, -3, 1001, 5000} {
		got, err := repo.List(ctx, limit)
		require.NoError(t, err)
		assert.Len(t, got, 6, "limit %d clamps into range and returns all rows", limit)
	}
	got, err := repo.List(ctx, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{e6.ID, e5.ID}, ids(got), "list is newest-first")

	// 单维：actor。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice"})
	require.NoError(t, err)
	assert.Equal(t, []string{e2.ID, e1.ID}, ids(got))

	// 两维组合：actor + source。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice", Source: "cli"})
	require.NoError(t, err)
	assert.Equal(t, []string{e2.ID}, ids(got), "actor+source must intersect")

	// 两维组合：source + action 前缀。
	got, err = repo.ListFiltered(ctx, audit.Filter{Source: "api", ActionPrefix: "project."})
	require.NoError(t, err)
	assert.Equal(t, []string{e3.ID, e1.ID}, ids(got))

	// 三维组合：actor + source + resource。
	got, err = repo.ListFiltered(ctx, audit.Filter{Actor: "alice", Source: "api", Resource: "project/p1"})
	require.NoError(t, err)
	assert.Equal(t, []string{e1.ID}, ids(got))

	// action 前缀 LIKE 转义：前缀 "role_" 只命中字面下划线行——未转义的
	// LIKE 会把 role.create 的 "." 也当任意字符吞掉。
	got, err = repo.ListFiltered(ctx, audit.Filter{ActionPrefix: "role_"})
	require.NoError(t, err)
	assert.Equal(t, []string{e6.ID}, ids(got), "underscore in the prefix must match literally (escaped LIKE)")
	got, err = repo.ListFiltered(ctx, audit.Filter{ActionPrefix: "role."})
	require.NoError(t, err)
	assert.Equal(t, []string{e5.ID}, ids(got), "dot in the prefix must match literally")

	// Team 轴（ADR-0035 决策 6）：team 行 ∪ '' 平台行，平台行对全部 Team 可见。
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamA})
	require.NoError(t, err)
	assert.Equal(t, []string{e5.ID, e4.ID, e3.ID, e1.ID}, ids(got))
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamB})
	require.NoError(t, err)
	assert.Equal(t, []string{e6.ID, e4.ID, e2.ID}, ids(got))

	// Team 轴再叠一维：team + actor。
	got, err = repo.ListFiltered(ctx, audit.Filter{Team: teamA, Actor: "bob"})
	require.NoError(t, err)
	assert.Equal(t, []string{e3.ID}, ids(got))
}

// TestAuditAppendConcurrentNonTx：R05-M1 复现形态——32 goroutine × 8 次
// 非事务 Append 全部成功，seq 唯一且连续（1..256），铸造 id 全量唯一。
// 非事务面的 NextSeq+INSERT 临界区由 repo 串行化；唯一冲突重试是兜底。
func TestAuditAppendConcurrentNonTx(t *testing.T) {
	db, clock := statetest.New(t)
	repo := auditrepo.New(clock, db.Runner())
	const (
		goroutines = 32
		perWorker  = 8
		total      = goroutines * perWorker
	)
	errs := make(chan error, total)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				e := &audit.Entry{
					Actor:    "racer",
					Source:   audit.SourceSystem,
					Action:   "audit.concurrent",
					Resource: fmt.Sprintf("audit/%d-%d", g, i),
				}
				if err := repo.Append(context.Background(), e); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent append must not lose rows: %v", err)
	}

	rows, err := repo.List(context.Background(), total)
	require.NoError(t, err)
	require.Len(t, rows, total, "every append must leave exactly one audit row")
	seen := make(map[int64]bool, total)
	mintedIDs := make(map[string]bool, total)
	for i, row := range rows { // 新→旧：seq 应连续递减
		want := int64(total - i)
		assert.Equal(t, want, row.Seq, "seq must be contiguous with no gaps")
		assert.True(t, entityid.Valid(row.ID), "every concurrent append mints a valid ULID id")
		mintedIDs[row.ID] = true
		seen[row.Seq] = true
	}
	assert.Len(t, seen, total, "seq must be unique")
	assert.Len(t, mintedIDs, total, "minted ids must be unique")
}

// collidingRunner 模拟"另一写者刚铸走同一 seq"：第一次 INSERT 先真实落库，
// 再以同参数重放一次取回真实 UNIQUE 错误——repo 应重取 MAX+1 后成功。
type collidingRunner struct {
	state.Runner
	collideOnce bool
}

func (r *collidingRunner) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if !r.collideOnce {
		return r.Runner.ExecContext(ctx, query, args...)
	}
	r.collideOnce = false
	if _, err := r.Runner.ExecContext(ctx, query, args...); err != nil {
		return nil, err
	}
	return r.Runner.ExecContext(ctx, query, args...) // 同 seq 二次插入 → 真实唯一冲突
}

// TestAuditAppendRetriesUniqueCollision：唯一冲突后重取 seq 重铸 ID。
func TestAuditAppendRetriesUniqueCollision(t *testing.T) {
	db, clock := statetest.New(t)
	repo := auditrepo.New(clock, &collidingRunner{Runner: db.Runner(), collideOnce: true})

	e := &audit.Entry{Actor: "alice", Source: audit.SourceSystem, Action: "audit.retry", Resource: "audit/r1"}
	require.NoError(t, repo.Append(context.Background(), e))
	assert.Equal(t, int64(2), e.Seq, "retry must re-cast seq from MAX+1")
	assert.True(t, entityid.Valid(e.ID), "retry re-mints a fresh ULID id (ADR-0092)")

	rows, err := repo.List(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, int64(2), rows[0].Seq)
	assert.Equal(t, int64(1), rows[1].Seq)
	assert.Equal(t, e.ID, rows[0].ID, "the persisted winner carries the re-minted id")
	assert.NotEqual(t, rows[0].ID, rows[1].ID, "the collided first row keeps its own minted id")
}

// uniqueViolationRunner 每次 INSERT 都报唯一冲突（模拟持续外部竞争），用
// 于断言重试有界、错误不再被吞。
type uniqueViolationRunner struct {
	state.Runner
	calls int
}

func (r *uniqueViolationRunner) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	r.calls++
	return nil, errors.New("constraint failed: UNIQUE constraint failed: audit.seq (2067)")
}

// TestAuditAppendRetryExhausted：有界尝试耗尽后返回最后一次唯一冲突。
func TestAuditAppendRetryExhausted(t *testing.T) {
	db, clock := statetest.New(t)
	run := &uniqueViolationRunner{Runner: db.Runner()}
	repo := auditrepo.New(clock, run)

	err := repo.Append(context.Background(), &audit.Entry{Actor: "alice", Source: audit.SourceSystem, Action: "audit.retry", Resource: "audit/r1"})
	require.ErrorContains(t, err, "UNIQUE")
	assert.Equal(t, 3, run.calls, "bounded attempts: initial + 2 retries")
	rows, lerr := repo.List(context.Background(), 10)
	require.NoError(t, lerr)
	assert.Empty(t, rows, "failed appends must not leave partial rows")
}

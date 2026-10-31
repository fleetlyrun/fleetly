package outbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func TestOutboxMonotonicSeq(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	events := outboxrepo.New(clock, db.Runner())

	var last int64
	for i := 0; i < 3; i++ {
		seq, err := events.Append(ctx, "team-a", "deployment.queued", "deployment",
			"01JD0DEPLOY0000000000000000"+string(rune('0'+i)), []byte(`{}`))
		require.NoError(t, err)
		require.Greater(t, seq, last, "seq must be monotonically increasing")
		last = seq
	}

	// 未注册事件名落库即拒绝（注册表三链咬合的运行时面）。
	_, err := events.Append(ctx, "team-a", "deployment.nope", "deployment", "x", nil)
	require.ErrorContains(t, err, "not registered")

	after, err := events.ListAfter(ctx, 1, 10, "")
	require.NoError(t, err)
	assert.Len(t, after, 2)
	assert.Equal(t, int64(2), after[0].Seq)

	seq, err := events.LastSeq(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), seq)

	// Team 行级过滤（P0-2）：本队 + 平台级（''）行可见，他队行不可见。
	_, err = events.Append(ctx, "", "node.joined", "node", "01JD0NODE0000000000000000N", []byte(`{}`))
	require.NoError(t, err)
	_, err = events.Append(ctx, "team-b", "deployment.queued", "deployment", "01JD0DEPLOY0000000000000009", []byte(`{}`))
	require.NoError(t, err)
	teamA, err := events.ListAfter(ctx, 0, 10, "team-a")
	require.NoError(t, err)
	assert.Len(t, teamA, 4, "team-a sees its own rows plus the platform-level ('') row")
	for _, ev := range teamA {
		assert.NotEqual(t, "01JD0DEPLOY0000000000000009", ev.AggregateID, "team-b rows must not leak")
	}
	unfiltered, err := events.ListAfter(ctx, 0, 10, "")
	require.NoError(t, err)
	assert.Len(t, unfiltered, 5, "empty team = no row-level filter (platform owner / internal consumers)")

	// 保留窗（ADR-0026）：earliest_seq 划界 + 窗外回收（"只增"修订为
	// "窗内只增"）；窗内行不受 trim 影响。
	earliest, err := events.EarliestSeq(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), earliest)

	clock.Advance(8 * 24 * time.Hour)
	n, err := events.TrimBefore(ctx, clock.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(5), n, "all five rows are outside the 7d window")
	earliest, err = events.EarliestSeq(ctx)
	require.NoError(t, err)
	assert.Zero(t, earliest, "empty table reports earliest 0 (not a gap)")
}

// TestOutboxTrimKeepsSeqPrefix：R05-M4——按 created_at 直删会在墙钟跳变
// 下制造中间洞（删掉 seq2/3 的旧行、留下 seq1 的未来行，earliest 仍为 1，
// 订阅方不触发 410 而静默丢事件）。裁剪必须是 seq 前缀：先取窗外行中的
// 最大 seq，再删所有 seq <= 它的行；时间乱序行直插构造（Append 走时钟，
// 无法回拨造乱序）。
func TestOutboxTrimKeepsSeqPrefix(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	events := outboxrepo.New(clock, db.Runner())
	insert := func(createdAt, aggregateID string) {
		t.Helper()
		_, err := db.Runner().ExecContext(ctx,
			`INSERT INTO outbox (name, aggregate, aggregate_id, payload, created_at, team_id) VALUES (?, ?, ?, ?, ?, '')`,
			"deployment.queued", "deployment", aggregateID, `{}`, createdAt)
		require.NoError(t, err)
	}
	insert("2026-02-01T00:00:00Z", "d1") // seq1：墙钟"未来"
	insert("2026-01-01T00:00:00Z", "d2") // seq2：窗外（旧）
	insert("2026-01-02T00:00:00Z", "d3") // seq3：窗外（旧）
	insert("2026-02-02T00:00:00Z", "d4") // seq4：墙钟"未来"

	n, err := events.TrimBefore(ctx, statetest.MustParse("2026-01-04T00:00:00Z"))
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "only the seq prefix (1..3) may be trimmed: max old seq is 3")

	earliest, err := events.EarliestSeq(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), earliest, "the surviving table is a seq suffix (no middle hole)")
	last, err := events.LastSeq(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), last)
	assert.Less(t, int64(1), earliest, "a cursor at the trimmed edge satisfies the 410 condition (after_seq < earliest)")

	after, err := events.ListAfter(ctx, 3, 10, "")
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, int64(4), after[0].Seq)
}

// TestOutboxListByAggregate：按聚合过滤的前向扫描（B2a W1b facts 地基：
// 诊断「近窗事件」查询面）——聚合等值 + seq 升序 + team 行级过滤
// （本队 + 平台级 ”）+ afterSeq 游标 + limit 钳制。
func TestOutboxListByAggregate(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	events := outboxrepo.New(clock, db.Runner())

	// 三聚合混布：workload 聚合（终态事实族）+ deployment 聚合 + 平台级行。
	appendRow := func(team, name, aggregate, id string) {
		t.Helper()
		_, err := events.Append(ctx, team, name, aggregate, id, []byte(`{}`))
		require.NoError(t, err)
	}
	wid := "01JD0WORK000000000000000W"
	appendRow("team-a", "workload.container_exited", "workload", wid)
	appendRow("team-a", "workload.drift_detected", "workload", wid)
	appendRow("team-a", "deployment.queued", "deployment", "01JD0DEPLOY000000000000001")     // 异聚合
	appendRow("team-b", "workload.container_exited", "workload", "01JD0WORK00000000000000X") // 异队同聚合异 ID
	appendRow("", "workload.rollout_stalled", "workload", wid)                               // 平台级行

	// team-a 视角：workload/wid 的两行 + 平台级一行（升序）。
	rows, err := events.ListByAggregate(ctx, "team-a", "workload", wid, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "workload.container_exited", rows[0].Name)
	assert.Equal(t, "workload.drift_detected", rows[1].Name)
	assert.Equal(t, "workload.rollout_stalled", rows[2].Name, "platform-level ('') rows are visible to every team")

	// afterSeq 游标：跳过前两行。
	rows, err = events.ListByAggregate(ctx, "team-a", "workload", wid, rows[1].Seq, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "workload.rollout_stalled", rows[0].Name)

	// team-b 视角：只见平台级行（team-a 的行不泄露）。
	rows, err = events.ListByAggregate(ctx, "team-b", "workload", wid, 0, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "workload.rollout_stalled", rows[0].Name)

	// team 空 = 不过滤（平台 owner/内部消费面）。
	rows, err = events.ListByAggregate(ctx, "", "workload", wid, 0, 100)
	require.NoError(t, err)
	assert.Len(t, rows, 3, "empty team skips the row-level filter")

	// limit 钳制。
	rows, err = events.ListByAggregate(ctx, "team-a", "workload", wid, 0, 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// 无命中聚合：空结果。
	rows, err = events.ListByAggregate(ctx, "team-a", "task", "missing", 0, 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestOutboxListByAggregateKeepsNewest：超限聚合的截断方向取**最新**行
// （修复批 F3）：升序前缀截断会让持续崩溃风暴下最新 container_exited 被
// 挤出扫描窗——终态计数对越热的循环越看不见。1005 行聚合 → 返回最新
// 1000 行（seq 6..1005）且仍升序。
func TestOutboxListByAggregateKeepsNewest(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	events := outboxrepo.New(clock, db.Runner())
	wid := "01JD0STORM00000000000000W"
	for i := 0; i < 1005; i++ {
		_, err := events.Append(ctx, "team-a", "workload.container_exited", "workload", wid, []byte(`{}`))
		require.NoError(t, err)
	}
	rows, err := events.ListByAggregate(ctx, "team-a", "workload", wid, 0, 1000)
	require.NoError(t, err)
	require.Len(t, rows, 1000)
	assert.Equal(t, int64(6), rows[0].Seq, "truncation must keep the newest 1000 rows (seq 6..1005)")
	assert.Equal(t, int64(1005), rows[len(rows)-1].Seq)
	for i := 1; i < len(rows); i++ {
		require.Greater(t, rows[i].Seq, rows[i-1].Seq, "returned rows must stay ascending (caller contract)")
	}
}

// TestOutboxListByAggregateIDs：App 级集合查询（修复批 F4 诊断扇出收口）
// ——快照 workload 集一次 IN 查询分组返回：异聚合/异键剔除、组内升序、
// 每键独立最新 limit 截断（与逐键 ListByAggregate 同截断方向）、team 行级
// 过滤、afterSeq 游标、空键集零 SQL 空映射。
func TestOutboxListByAggregateIDs(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	events := outboxrepo.New(clock, db.Runner())
	appendRow := func(team, aggregate, id string) {
		t.Helper()
		_, err := events.Append(ctx, team, "workload.container_exited", aggregate, id, []byte(`{}`))
		require.NoError(t, err)
	}
	w1 := "01JD0WORK0000000000000001"
	w2 := "01JD0WORK0000000000000002"
	appendRow("team-a", "workload", w1) // w1：3 行
	appendRow("team-a", "workload", w1)
	appendRow("team-a", "workload", w1)
	appendRow("team-a", "workload", w2)                            // w2：1 行
	appendRow("team-a", "deployment", "01JD0DEPLOY00000000000001") // 异聚合：不入集
	appendRow("team-b", "workload", w1)                            // 异队行

	// team-a 视角：两键分组、组内升序、异聚合/异队剔除。
	got, err := events.ListByAggregateIDs(ctx, "team-a", "workload", []string{w1, w2}, 0, 100)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Len(t, got[w1], 3)
	require.Len(t, got[w2], 1)
	for i := 1; i < len(got[w1]); i++ {
		require.Greater(t, got[w1][i].Seq, got[w1][i-1].Seq, "per-key rows must stay ascending (caller contract)")
	}

	// 每键独立最新 limit：limit=2 只作用于 w1（w2 不受热键挤占——窗口函数
	// PARTITION BY 的语义面）。
	got, err = events.ListByAggregateIDs(ctx, "team-a", "workload", []string{w1, w2}, 0, 2)
	require.NoError(t, err)
	require.Len(t, got[w1], 2)
	assert.Equal(t, int64(2), got[w1][0].Seq, "per-key truncation keeps the newest rows")
	assert.Equal(t, int64(3), got[w1][1].Seq)
	require.Len(t, got[w2], 1, "a cold key keeps its rows regardless of a hot key's budget")

	// afterSeq 游标 + 键缺席（快照外 ID）：空组不入映射。
	got, err = events.ListByAggregateIDs(ctx, "team-a", "workload", []string{w1, "01JD0MISSING00000000000M"}, got[w1][0].Seq, 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Len(t, got[w1], 1)

	// team-b 视角：异队行仅自己可见（w2 整组不泄露）。
	got, err = events.ListByAggregateIDs(ctx, "team-b", "workload", []string{w1, w2}, 0, 100)
	require.NoError(t, err)
	assert.NotContains(t, got, w2, "another team's rows must not leak through the set query")

	// 空键集：零 SQL 空映射（不产 malformed IN ()）。
	got, err = events.ListByAggregateIDs(ctx, "team-a", "workload", nil, 0, 100)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAuditAppendList(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	audits := auditrepo.New(clock, db.Runner())

	require.NoError(t, audits.Append(ctx, &audit.Entry{
		ID: "01JD0AUDIT00000000000000000", Actor: "ops", Source: audit.SourceCLI,
		Action: "project.create", Resource: "project/01JD0PROJ00000000000000000",
		AfterFP: "sha256:aa",
	}))
	list, err := audits.List(ctx, 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "cli", string(list[0].Source))
	assert.Equal(t, "2026-01-01T00:00:00Z", list[0].CreatedAt)
}

func TestNodeObservationCache(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	nodes := noderepo.New(clock, db.Runner())

	n := &node.Node{PlatformID: "01JD0NODE00000000000000000", CarrierID: "swarmabc", Hostname: "node-1", Role: "manager", Available: true}
	require.NoError(t, nodes.Upsert(ctx, n))

	// 观测刷新：available 翻转、last_seen_at 推进，first_seen_at 不变。
	clock.Advance(5 * time.Second)
	n.Available = false
	require.NoError(t, nodes.Upsert(ctx, n))
	got, err := nodes.Get(ctx, n.PlatformID)
	require.NoError(t, err)
	assert.False(t, got.Available)
	assert.Equal(t, "2026-01-01T00:00:00Z", got.FirstSeenAt)
	assert.Equal(t, "2026-01-01T00:00:05Z", got.LastSeenAt)

	list, err := nodes.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

package writes_test

// 事实发射 kernel 的扇出语义（P2 刀 1 验收锚）：默认双真（一条 FactRow
// → outbox 流面 + audit 取证面两行同源）、两 flag 偏离、两轴分立
//（outbox 资源轴 = FactRow.TeamID；audit 操作者轴 = ctx 铸入——同一
// 事实两行各自正确的轴）、惰性求值与失败隔离（任一行失败，后续事实
// 零求值零执行）、Emit 非事务 best-effort 面（错误返回由调用方自记）。
// 真库面经 statetest + 真实 repo 驱动；失败注入经 fake repo（接口在
// domain，kernel 只消费 Append 面）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

var errBoom = errors.New("boom")

// kernelRepos 装配真库 repo 集（FakeClock 钉时间戳）。
func kernelRepos() writes.Repos {
	clock := statetest.NewFakeClock()
	return writes.Repos{Outbox: outboxrepo.New(clock), Audits: auditrepo.New(clock)}
}

// outboxTeamAxis 直读 outbox 行的资源轴 team_id（Event 读面不带 team——
// proto 契约冻结，P0-2；team 是读面过滤的内部列）。
func outboxTeamAxis(t *testing.T, db *persistence.DB, seq int64) string {
	t.Helper()
	var team string
	require.NoError(t, db.Runner().QueryRowContext(context.Background(),
		"SELECT team_id FROM outbox WHERE seq = ?", seq).Scan(&team))
	return team
}

// TestSequenceFansOutBothRows：默认双真——同一条 FactRow 扇出 outbox 行
// （资源轴 = TeamID）与 audit 行（操作者轴 = ctx 铸入，与资源轴分立），
// 缺省铸型（Source "" → system；Resource "" → Aggregate/ID）。
func TestSequenceFansOutBothRows(t *testing.T) {
	db, _ := statetest.New(t)
	repos := kernelRepos()
	ctx := audit.WithTeam(context.Background(), "team-operator")

	err := db.Tx(ctx, func(tx *sql.Tx) error {
		return writes.Sequence(ctx, tx, repos, func() writes.FactRow {
			return writes.FactRow{
				Name: "task.created", Aggregate: "task", ID: "id1",
				Payload: []byte(`{"a":1}`), TeamID: "team-resource",
				Action: "task.create", Actor: "user:alice",
				BeforeFP: "", AfterFP: "oneshot",
			}
		})
	})
	require.NoError(t, err)

	events, err := repos.Outbox.ListAfter(ctx, db.Runner(), 0, 10, "")
	require.NoError(t, err)
	require.Len(t, events, 1, "流面一行")
	assert.Equal(t, "task.created", events[0].Name)
	assert.Equal(t, "task", events[0].Aggregate)
	assert.Equal(t, "id1", events[0].AggregateID)
	assert.Equal(t, []byte(`{"a":1}`), events[0].Payload)
	assert.Equal(t, "team-resource", outboxTeamAxis(t, db, events[0].Seq),
		"outbox 行 team_id 是资源轴（FactRow.TeamID）")

	audits, err := repos.Audits.List(ctx, db.Runner(), 10)
	require.NoError(t, err)
	require.Len(t, audits, 1, "取证面一行")
	a := audits[0]
	assert.Equal(t, "task.create", a.Action)
	assert.Equal(t, "task/id1", a.Resource)
	assert.Equal(t, "user:alice", a.Actor)
	assert.Equal(t, "oneshot", a.AfterFP)
	assert.Equal(t, "team-operator", a.TeamID,
		"audit 行 team_id 是操作者轴（ctx 铸入）——与资源轴分立，互不代填")
}

// TestSequenceDefaults：Source 空 → system；Resource 空 → Aggregate/ID
// （Audit 指纹词面不受影响——审计动词与事件名分立是 FactRow 显式双词面）。
func TestSequenceDefaults(t *testing.T) {
	db, _ := statetest.New(t)
	repos := kernelRepos()
	ctx := context.Background()

	require.NoError(t, db.Tx(ctx, func(tx *sql.Tx) error {
		return writes.Sequence(ctx, tx, repos, func() writes.FactRow {
			return writes.FactRow{Name: "task.created", Aggregate: "task", ID: "id1",
				Payload: []byte(`{}`), Action: "task.create"}
		})
	}))

	audits, err := repos.Audits.List(ctx, db.Runner(), 10)
	require.NoError(t, err)
	require.Len(t, audits, 1)
	assert.Equal(t, audit.SourceSystem, audits[0].Source, "空 Source 缺省 system")
	assert.Equal(t, "task/id1", audits[0].Resource, "空 Resource 缺省 Aggregate/ID")
}

// TestSequenceFlags：LedgerOnly 只落取证面；StreamOnly 只落流面。
func TestSequenceFlags(t *testing.T) {
	db, _ := statetest.New(t)
	repos := kernelRepos()
	ctx := context.Background()

	require.NoError(t, db.Tx(ctx, func(tx *sql.Tx) error {
		return writes.Sequence(ctx, tx, repos,
			func() writes.FactRow {
				return writes.FactRow{Aggregate: "task", ID: "l1",
					Action: "task.audit", LedgerOnly: true} // 测试面：flag 语义探针
			},
			func() writes.FactRow {
				return writes.FactRow{Name: "task.created", Aggregate: "task", ID: "s1",
					Payload:    []byte(`{}`),
					StreamOnly: true} // 测试面：flag 语义探针
			},
		)
	}))

	events, err := repos.Outbox.ListAfter(ctx, db.Runner(), 0, 10, "")
	require.NoError(t, err)
	require.Len(t, events, 1, "LedgerOnly 不落流面")
	assert.Equal(t, "s1", events[0].AggregateID, "StreamOnly 落流面")

	audits, err := repos.Audits.List(ctx, db.Runner(), 10)
	require.NoError(t, err)
	require.Len(t, audits, 1, "StreamOnly 不落取证面")
	assert.Equal(t, "task/l1", audits[0].Resource, "LedgerOnly 落取证面")
}

// TestSequenceRejectsBothFlags：双旗同立拒绝（什么都不发射 = 静默丢事实）。
func TestSequenceRejectsBothFlags(t *testing.T) {
	db, _ := statetest.New(t)
	repos := kernelRepos()

	err := writes.Sequence(context.Background(), db.Runner(), repos, func() writes.FactRow {
		// 测试面：双旗同立拒绝路径探针（非法形态本身无单侧偏离理由可注）。
		return writes.FactRow{LedgerOnly: true, StreamOnly: true}
	})
	require.Error(t, err)
}

// fakeOutbox 是失败注入用的 outbox.Repo fake（kernel 只消费 Append）。
type fakeOutbox struct {
	appends int
	failOn  int // 第 failOn 次 Append 失败（1 起；0 = 永不）
}

func (f *fakeOutbox) Append(context.Context, state.Runner, string, string, string, string, []byte) (int64, error) {
	f.appends++
	if f.appends == f.failOn {
		return 0, errBoom
	}
	return int64(f.appends), nil
}

func (f *fakeOutbox) ListAfter(context.Context, state.Runner, int64, int, string) ([]outbox.Event, error) {
	return nil, nil
}
func (f *fakeOutbox) LastSeq(context.Context, state.Runner) (int64, error)     { return 0, nil }
func (f *fakeOutbox) EarliestSeq(context.Context, state.Runner) (int64, error) { return 0, nil }
func (f *fakeOutbox) TrimBefore(context.Context, state.Runner, time.Time) (int64, error) {
	return 0, nil
}

// fakeAudits 是失败注入用的 audit.Repo fake（记录到达行）。
type fakeAudits struct {
	entries []audit.Entry
}

func (f *fakeAudits) Append(_ context.Context, _ state.Runner, e *audit.Entry) error {
	f.entries = append(f.entries, *e)
	return nil
}

func (f *fakeAudits) List(context.Context, state.Runner, int) ([]audit.Entry, error) {
	return nil, nil
}
func (f *fakeAudits) ListFiltered(context.Context, state.Runner, audit.Filter) ([]audit.Entry, error) {
	return nil, nil
}

// TestSequenceLazyOrderAndFailureIsolation：事实按声明序惰性求值（每条
// 在其落库时点求值）；任一行失败即返回，后续事实零求值零执行。
func TestSequenceLazyOrderAndFailureIsolation(t *testing.T) {
	var evaluated []string
	fact := func(name string) func() writes.FactRow {
		return func() writes.FactRow {
			evaluated = append(evaluated, name)
			return writes.FactRow{Aggregate: "task", ID: name, Action: "task.probe",
				LedgerOnly: true} // 测试面：探针走取证面，失败注入在流面
		}
	}
	ob := &fakeOutbox{failOn: 1}
	au := &fakeAudits{}

	// 第 1 条走取证面（不触碰流面）；第 2 条 StreamOnly 触碰 fakeOutbox
	// 的失败注入位（首个 Append 即 boom）：失败即返回，第 3 条零求值。
	err := writes.Sequence(context.Background(), nil, writes.Repos{Outbox: ob, Audits: au},
		fact("f1"),
		func() writes.FactRow {
			evaluated = append(evaluated, "f2")
			return writes.FactRow{Name: "task.created", Aggregate: "task", ID: "f2",
				Payload:    []byte(`{}`),
				StreamOnly: true} // 测试面：触碰失败注入位
		},
		fact("f3"),
	)
	require.ErrorIs(t, err, errBoom)
	assert.Equal(t, []string{"f1", "f2"}, evaluated, "失败后后续事实零求值")
	assert.Len(t, au.entries, 1, "失败事实的取证面不落（流面先行失败）——前序事实的取证面已在")
}

// TestEmitNonTransactional：Emit 是非事务 best-effort 面——错误返回由
// 调用方自记；LedgerOnly 只触碰 audit 面（Outbox 零值不装配）。
func TestEmitNonTransactional(t *testing.T) {
	db, _ := statetest.New(t)
	clock := statetest.NewFakeClock()
	audits := auditrepo.New(clock)
	ctx := audit.WithTeam(context.Background(), "team-op")

	// LedgerOnly：登录/认证类事实——不装配 Outbox（未触碰的 repo 可为零值）。
	require.NoError(t, writes.Emit(ctx, db.Runner(), writes.Repos{Audits: audits}, writes.FactRow{
		Aggregate: "token", ID: "t1",
		Actor: "token:tok", Action: "token.narrowed_denied", AfterFP: "reason",
		LedgerOnly: true, // 认证拒绝：仅取证面（无事件孪生）
	}))

	rows, err := audits.List(ctx, db.Runner(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "token/t1", rows[0].Resource, "空 Resource 缺省 Aggregate/ID")
	assert.Equal(t, "team-op", rows[0].TeamID)

	// 错误面：fake 注入失败，Emit 原样返回（调用方自记日志）。
	require.ErrorIs(t, writes.Emit(ctx, db.Runner(),
		writes.Repos{Outbox: &fakeOutbox{failOn: 1}, Audits: audits},
		writes.FactRow{Name: "task.created", Aggregate: "task", ID: "x",
			StreamOnly: true}), errBoom) // 测试面：失败注入探针（仅流面触碰 fakeOutbox）
}

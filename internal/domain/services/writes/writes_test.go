package writes_test

// 写序列 kernel 的段序与失败语义（ADR-0064 验收锚）：以 outbox LastSeq 为
// 探针记录各段求值时点——write 段的 探针事件使 seq 前进一位，检查所见
// = write 前、事件所见 = write 后（探针一位）、审计所见 = 事件后
//（探针 + 事实事件两位）。失败隔离经求值记录器钉死（任一段失败，后续
// 段零执行），并以失败后 LastSeq 不变实锤整单回滚。

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

var errBoom = errors.New("boom")

// probes 收各段求值时点（LastSeq 探针）与执行事实记录器。
type probes struct {
	seqAtCheck, seqAtEvent, seqAtAudit int64
	ranWrite, ranEvent, ranAudit       bool
}

// runSequence 跑一次探针 Fact：write 段落 探针事件使 LastSeq 前进一位；
// fail 指定在哪一段注入失败（"" = 全绿路径）。
func runSequence(t *testing.T, db *persistence.DB, ctx context.Context, fail string) (*probes, error) {
	t.Helper()
	outboxRepo, auditRepo := outboxrepo.New(statetest.NewFakeClock()), auditrepo.New(statetest.NewFakeClock())
	p := &probes{}

	err := db.Tx(ctx, func(tx *sql.Tx) error {
		return writes.Sequence(ctx, tx, outboxRepo, auditRepo, writes.Fact{
			Checks: []func(context.Context, *sql.Tx) error{
				func(context.Context, *sql.Tx) error {
					if fail == "check" {
						return errBoom
					}
					seq, lerr := outboxRepo.LastSeq(ctx, tx)
					p.seqAtCheck = seq
					return lerr
				},
			},
			Write: func(context.Context, *sql.Tx) error {
				if fail == "write" {
					return errBoom
				}
				p.ranWrite = true
				_, aerr := outboxRepo.Append(ctx, tx, "", "task.created", "task", "id1", []byte("{}"))
				return aerr
			},
			Events: []func() writes.Event{
				func() writes.Event {
					p.ranEvent = true
					p.seqAtEvent, _ = outboxRepo.LastSeq(ctx, tx)
					return writes.Event{Name: "task.updated", Aggregate: "task", ID: "id1", Payload: []byte(`{}`), Team: "t1"}
				},
			},
			Audits: []func(context.Context) []*audit.Entry{
				func(context.Context) []*audit.Entry {
					p.ranAudit = true
					p.seqAtAudit, _ = outboxRepo.LastSeq(ctx, tx)
					return []*audit.Entry{{ID: "a1", Action: "test.do", Resource: "test/id1"}}
				},
			},
		})
	})
	if fail != "" {
		return p, err
	}
	require.NoError(t, err)
	return p, nil
}

func TestSequenceOrder(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()

	p, err := runSequence(t, db, ctx, "")
	require.NoError(t, err)
	assert.True(t, p.ranWrite, "聚合写执行")
	assert.True(t, p.ranEvent && p.ranAudit, "事件与审计段全执行")

	after, err := outboxrepo.New(statetest.NewFakeClock()).LastSeq(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, int64(2), after, "探针 + 事实事件两条落账")
	assert.Equal(t, int64(0), p.seqAtCheck, "检查先于聚合写（拒绝零副作用——检查时 探针未落）")
	assert.Equal(t, int64(1), p.seqAtEvent, "事件晚于聚合写（负载可依赖写后行——求值时 探针已落）")
	assert.Equal(t, int64(2), p.seqAtAudit, "审计晚于事件段（事实事件已落账）")
}

func TestSequenceFailureIsolation(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()
	outboxRepo := outboxrepo.New(statetest.NewFakeClock())
	before, err := outboxRepo.LastSeq(ctx, db.Runner())
	require.NoError(t, err)

	t.Run("check 拒绝零副作用", func(t *testing.T) {
		p, err := runSequence(t, db, ctx, "check")
		require.ErrorIs(t, err, errBoom)
		assert.False(t, p.ranWrite, "检查失败：聚合写零执行")
		assert.False(t, p.ranEvent && p.ranAudit, "检查失败：事件与审计段零执行")
	})

	t.Run("write 失败零后续段", func(t *testing.T) {
		p, err := runSequence(t, db, ctx, "write")
		require.ErrorIs(t, err, errBoom)
		assert.False(t, p.ranEvent && p.ranAudit, "写失败：事件与审计段零执行")
	})

	after, err := outboxRepo.LastSeq(ctx, db.Runner())
	require.NoError(t, err)
	assert.Equal(t, before, after, "失败事务整单回滚——outbox 零残留")
}

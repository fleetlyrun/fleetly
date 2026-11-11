package persistence_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 四件一拍组合（ADR-0005）：状态 CAS + Outbox 事件 + 审计（本例无
// tombstone——部署记录永不删）在同一个 Tx 内落库；任一件失败全回滚。
func TestFourWriteSameTransaction(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	deployments := deploymentrepo.New(clock, db.Runner())

	d := &deployment.Deployment{
		ID: "01JD0DEPLOY0000000000000000", AppID: "01JD0APP000000000000000000",
		ToRevision: "01JD0REV000000000000000000", State: deployment.StateQueued,
	}

	var seq int64
	err := db.Tx(ctx, func(run state.Runner) error {
		deployments := deploymentrepo.New(clock, run)
		events := outboxrepo.New(clock, run)
		audits := auditrepo.New(clock, run)
		if err := deployments.Create(ctx, d); err != nil {
			return err
		}
		var err error
		seq, err = events.Append(ctx, "default", "deployment.queued", "deployment", d.ID, []byte(`{}`))
		if err != nil {
			return err
		}
		return audits.Append(ctx, &audit.Entry{
			ID: "01JD0AUDIT00000000000000000", Source: audit.SourceCLI,
			Action: "deployment.create", Resource: "deployment/" + d.ID,
		})
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)

	// 回滚路径：Outbox 未注册事件名使事务失败 → Deployment 行不得残留。
	bad := &deployment.Deployment{
		ID: "01JD0DEPLOY0000000000000001", AppID: d.AppID,
		ToRevision: d.ToRevision, State: deployment.StateQueued,
	}
	err = db.Tx(ctx, func(run state.Runner) error {
		deployments := deploymentrepo.New(clock, run)
		events := outboxrepo.New(clock, run)
		if err := deployments.Create(ctx, bad); err != nil {
			return err
		}
		_, err := events.Append(ctx, "", "not.registered", "deployment", bad.ID, nil)
		return err
	})
	require.Error(t, err)
	_, err = deployments.Get(ctx, bad.ID)
	assert.ErrorIs(t, err, state.ErrNotFound, "rolled-back deployment must not persist")
}

// WAL 生效与重复 Open 幂等（goose 迁移只前滚，二次打开不重复执行）。
func TestOpenIdempotentAndWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fleetly.db")
	db, err := persistence.Open(ctx, path, nil)
	require.NoError(t, err)

	var mode string
	require.NoError(t, db.Runner().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode))
	assert.Equal(t, "wal", mode, "WAL must be enabled")
	require.NoError(t, db.Close())

	db2, err := persistence.Open(ctx, path, nil)
	require.NoError(t, err)
	require.NoError(t, db2.Close())
}

// 读柄是 query_only 池：Reader 上的写必须大声失败（SQLITE_READONLY），
// 写柄与 Tx 正常；读柄仍能读到写柄已提交的行（不落后）。
func TestReaderRejectsWrites(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()

	_, err := db.Reader().ExecContext(ctx,
		`INSERT INTO seq_counters (name, next) VALUES ('reader-probe', 2)`)
	require.Error(t, err, "a write on the read handle must fail loudly (query_only)")
	assert.Contains(t, strings.ToLower(err.Error()), "readonly",
		"the failure must be the query_only/SQLITE_READONLY face, got: %v", err)

	_, err = db.Runner().ExecContext(ctx,
		`INSERT INTO seq_counters (name, next) VALUES ('writer-probe', 2)`)
	require.NoError(t, err, "the write handle stays writable")

	require.NoError(t, db.Tx(ctx, func(run state.Runner) error {
		_, err := run.ExecContext(ctx,
			`INSERT INTO seq_counters (name, next) VALUES ('tx-probe', 2)`)
		return err
	}), "Tx stays on the write handle")

	var n int
	require.NoError(t, db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM seq_counters WHERE name IN ('writer-probe', 'tx-probe')`).Scan(&n))
	assert.Equal(t, 2, n, "the read handle observes committed writes")
}

// VacuumInto 走专用 backup 连接：快照在写事务占用唯一写连接期间仍可
// 完成（若复用写柄会阻塞至事务结束），且长读在飞时写柄继续可写（WAL
// 读写互不阻塞）。快照本身是可打开的完整库。
func TestVacuumIntoUsesDedicatedBackupConnection(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()
	_, err := db.Runner().ExecContext(ctx,
		`INSERT INTO seq_counters (name, next) VALUES ('snapshot-anchor', 7)`)
	require.NoError(t, err)

	// 长读在飞：读池的一条连接被 rows 占用。
	rows, err := db.Reader().QueryContext(ctx, `SELECT name FROM seq_counters`)
	require.NoError(t, err)
	require.True(t, rows.Next())
	defer func() { _ = rows.Close() }()

	// 写事务在飞：写池的唯一连接被事务占用。
	started := make(chan struct{})
	release := make(chan struct{})
	txDone := make(chan error, 1)
	go func() {
		txDone <- db.Tx(ctx, func(run state.Runner) error {
			if _, err := run.ExecContext(ctx,
				`INSERT INTO seq_counters (name, next) VALUES ('vacuum-hold', 2)`); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	vctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	target := filepath.Join(t.TempDir(), "snapshot.db")
	require.NoError(t, db.VacuumInto(vctx, target),
		"VACUUM INTO must use the dedicated backup connection, not the busy write handle")

	// 快照是完整可打开的库（事务前快照：锚行在场）。
	snap, err := sql.Open("sqlite", "file:"+filepath.ToSlash(target))
	require.NoError(t, err)
	var n int
	require.NoError(t, snap.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM seq_counters WHERE name = 'snapshot-anchor'`).Scan(&n))
	require.NoError(t, snap.Close())
	assert.Equal(t, 1, n, "the snapshot is a consistent pre-transaction database")

	close(release)
	require.NoError(t, <-txDone)

	// 长读仍在飞时写柄继续可写：读池分离不把写串行化在读后面。
	_, err = db.Runner().ExecContext(ctx,
		`INSERT INTO seq_counters (name, next) VALUES ('post-vacuum-write', 2)`)
	require.NoError(t, err)
}

// FakeClock 推进语义（hermetic 时间戳确定性）。
func TestFakeClockAdvances(t *testing.T) {
	_, clock := statetest.New(t)
	first := clock.Now()
	clock.Advance(61 * time.Second)
	assert.True(t, clock.Now().After(first))
	assert.Equal(t, "2026-01-01T00:01:01Z", state.FormatTime(clock.Now()))
}

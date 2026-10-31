// Package alertrule 是阈值告警规则 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/telemetry/alertrule（ADR-0075 Repo 接口层），本包只持
// 不导出实现与构造。
package alertrule

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain alertrule.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain alertrule.Repo）。
func New(clock state.Clock, run state.Runner) alertrule.Repo { return &repo{clock: clock, run: run} }

const selectCols = `id, seq, app_id, metric, threshold, for_secs, enabled, state, state_since,
	last_value, last_observed_at, pending_since, created_at, updated_at`

// Create 落一条规则（初始态 ok）。
func (r *repo) Create(ctx context.Context, rule *alertrule.Rule) error {
	seq, err := persistence.NextSeq(ctx, r.run, "alert_rules")
	if err != nil {
		return err
	}
	rule.Seq = seq
	if rule.ID == "" {
		rule.ID = entityid.Join(rule.AppID, seq)
	}
	rule.CreatedAt = state.FormatTime(r.clock.Now())
	rule.UpdatedAt = rule.CreatedAt
	rule.State, rule.StateSince = alertrule.StateOK, rule.CreatedAt
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO alert_rules (id, seq, app_id, metric, threshold, for_secs, enabled, state, state_since,
			last_value, last_observed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
		rule.ID, rule.Seq, rule.AppID, rule.Metric, rule.Threshold, rule.ForSecs, boolInt(rule.Enabled),
		rule.State, rule.StateSince, rule.CreatedAt, rule.UpdatedAt)
	return err
}

// Get 按 ID 读行。
func (r *repo) Get(ctx context.Context, id string) (*alertrule.Rule, error) {
	row := r.run.QueryRowContext(ctx, "SELECT "+selectCols+" FROM alert_rules WHERE id = ?", id)
	return scanRule(row)
}

// ListEnabled 列全部启用规则（app 升序）。
func (r *repo) ListEnabled(ctx context.Context) ([]alertrule.Rule, error) {
	return r.listWhere(ctx, "WHERE enabled = 1 ORDER BY app_id, id")
}

// ListByApp 列某 App 的规则（含禁用行；id 升序）。
func (r *repo) ListByApp(ctx context.Context, appID string) ([]alertrule.Rule, error) {
	return r.listWhere(ctx, "WHERE app_id = ? ORDER BY seq", appID)
}

// ListAll 列全部规则（alerts list 面）。
func (r *repo) ListAll(ctx context.Context) ([]alertrule.Rule, error) {
	return r.listWhere(ctx, "ORDER BY app_id, id")
}

func (r *repo) listWhere(ctx context.Context, where string, args ...any) ([]alertrule.Rule, error) {
	rows, err := r.run.QueryContext(ctx, "SELECT "+selectCols+" FROM alert_rules "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []alertrule.Rule
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rule)
	}
	return out, rows.Err()
}

// Observe 落最近评估观测（值 + 时刻；不迁态）。
func (r *repo) Observe(ctx context.Context, id string, value float64, at string) error {
	_, err := r.run.ExecContext(ctx,
		"UPDATE alert_rules SET last_value = ?, last_observed_at = ?, updated_at = ? WHERE id = ?",
		value, at, state.FormatTime(r.clock.Now()), id)
	return err
}

// BeginPending 落越限首见的起窗时刻（pending_since 列直写；B2a W1b 直接
// 改列形态）。
func (r *repo) BeginPending(ctx context.Context, id, at string) error {
	_, err := r.run.ExecContext(ctx,
		"UPDATE alert_rules SET pending_since = ?, updated_at = ? WHERE id = ?",
		at, state.FormatTime(r.clock.Now()), id)
	return err
}

// ClearPending 清 pending 窗（回落归位；幂等）。
func (r *repo) ClearPending(ctx context.Context, id string) error {
	_, err := r.run.ExecContext(ctx,
		"UPDATE alert_rules SET pending_since = '', updated_at = ? WHERE id = ?",
		state.FormatTime(r.clock.Now()), id)
	return err
}

// Transition 迁移评估态（fired/resolved 通知沿的持久锚）；迁移即离窗——
// pending_since 随迁移清列（firing 与 ok 都是 pending 窗的终点）。
func (r *repo) Transition(ctx context.Context, id, to, at string) error {
	_, err := r.run.ExecContext(ctx,
		"UPDATE alert_rules SET state = ?, state_since = ?, pending_since = '', updated_at = ? WHERE id = ?",
		to, at, state.FormatTime(r.clock.Now()), id)
	return err
}

// Delete 删行（幂等：不存在 ErrNotFound 由调用面映射 404）。
func (r *repo) Delete(ctx context.Context, id string) error {
	res, err := r.run.ExecContext(ctx, "DELETE FROM alert_rules WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// scanRule 是行扫描（*sql.Row 与 *sql.Rows 同构）。
func scanRule(row interface{ Scan(dest ...any) error }) (*alertrule.Rule, error) {
	var rule alertrule.Rule
	var enabled int
	err := row.Scan(&rule.ID, &rule.Seq, &rule.AppID, &rule.Metric, &rule.Threshold, &rule.ForSecs, &enabled,
		&rule.State, &rule.StateSince, &rule.LastValue, &rule.LastObservedAt, &rule.PendingSince,
		&rule.CreatedAt, &rule.UpdatedAt)
	if persistence.MapScanErr(err) == state.ErrNotFound {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rule.Enabled = enabled != 0
	return &rule, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

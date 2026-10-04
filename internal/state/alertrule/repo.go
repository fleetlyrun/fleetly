// Package alertrule 是阈值告警规则的行仓储（F2.5，ADR-0041 决策 3）：
// per-App 规则 + 评估状态机落行（ok|firing；pending 持续窗在 engine 内存，
// 重启重置——采样近似语义的一部分）。评估写面（Observe/Transition）与
// 配置写面（Create/Delete）分立。
package alertrule

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 指标值域（冻结词汇，ADR-0041）。
const (
	MetricCPUPercent       = "cpu_percent"
	MetricMemoryWorkingSet = "memory_working_set_bytes"
)

// 状态值域。
const (
	StateOK     = "ok"
	StateFiring = "firing"
)

// Rule 是一行阈值规则（含评估状态面）。
type Rule struct {
	ID             string
	AppID          string
	Metric         string
	Threshold      float64
	ForSecs        int64
	Enabled        bool
	State          string
	StateSince     string
	LastValue      float64
	LastObservedAt string
	CreatedAt      string
	UpdatedAt      string
}

// Repo 是 alert_rules 表的仓储。
type Repo struct {
	clock state.Clock
}

// New 构造仓储。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

const selectCols = `id, app_id, metric, threshold, for_secs, enabled, state, state_since,
	last_value, last_observed_at, created_at, updated_at`

// Create 落一条规则（初始态 ok）。
func (r *Repo) Create(ctx context.Context, run state.Runner, rule *Rule) error {
	rule.CreatedAt = state.FormatTime(r.clock.Now())
	rule.UpdatedAt = rule.CreatedAt
	rule.State, rule.StateSince = StateOK, rule.CreatedAt
	_, err := run.ExecContext(ctx, `
		INSERT INTO alert_rules (id, app_id, metric, threshold, for_secs, enabled, state, state_since,
			last_value, last_observed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, '', ?, ?)`,
		rule.ID, rule.AppID, rule.Metric, rule.Threshold, rule.ForSecs, boolInt(rule.Enabled),
		rule.State, rule.StateSince, rule.CreatedAt, rule.UpdatedAt)
	return err
}

// Get 按 ID 读行。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Rule, error) {
	row := run.QueryRowContext(ctx, "SELECT "+selectCols+" FROM alert_rules WHERE id = ?", id)
	return scanRule(row)
}

// ListEnabled 列全部启用规则（app 升序）。
func (r *Repo) ListEnabled(ctx context.Context, run state.Runner) ([]Rule, error) {
	return r.listWhere(ctx, run, "WHERE enabled = 1 ORDER BY app_id, id")
}

// ListByApp 列某 App 的规则（含禁用行；id 升序）。
func (r *Repo) ListByApp(ctx context.Context, run state.Runner, appID string) ([]Rule, error) {
	return r.listWhere(ctx, run, "WHERE app_id = ? ORDER BY id", appID)
}

// ListAll 列全部规则（alerts list 面）。
func (r *Repo) ListAll(ctx context.Context, run state.Runner) ([]Rule, error) {
	return r.listWhere(ctx, run, "ORDER BY app_id, id")
}

func (r *Repo) listWhere(ctx context.Context, run state.Runner, where string, args ...any) ([]Rule, error) {
	rows, err := run.QueryContext(ctx, "SELECT "+selectCols+" FROM alert_rules "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Rule
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
func (r *Repo) Observe(ctx context.Context, run state.Runner, id string, value float64, at string) error {
	_, err := run.ExecContext(ctx,
		"UPDATE alert_rules SET last_value = ?, last_observed_at = ?, updated_at = ? WHERE id = ?",
		value, at, state.FormatTime(r.clock.Now()), id)
	return err
}

// Transition 迁移评估态（fired/resolved 通知沿的持久锚）。
func (r *Repo) Transition(ctx context.Context, run state.Runner, id, to, at string) error {
	_, err := run.ExecContext(ctx,
		"UPDATE alert_rules SET state = ?, state_since = ?, updated_at = ? WHERE id = ?",
		to, at, state.FormatTime(r.clock.Now()), id)
	return err
}

// Delete 删行（幂等：不存在 ErrNotFound 由调用面映射 404）。
func (r *Repo) Delete(ctx context.Context, run state.Runner, id string) error {
	res, err := run.ExecContext(ctx, "DELETE FROM alert_rules WHERE id = ?", id)
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
func scanRule(row interface{ Scan(dest ...any) error }) (*Rule, error) {
	var rule Rule
	var enabled int
	err := row.Scan(&rule.ID, &rule.AppID, &rule.Metric, &rule.Threshold, &rule.ForSecs, &enabled,
		&rule.State, &rule.StateSince, &rule.LastValue, &rule.LastObservedAt,
		&rule.CreatedAt, &rule.UpdatedAt)
	if state.MapScanErr(err) == state.ErrNotFound {
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

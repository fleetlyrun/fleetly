// Package deployment 是 Deployment 聚合 repo（CONTEXT.md Deployment 词条：
// 从旧 Revision 到新 Revision 的受监督迁移）。行本身即 admission 持久
// 真源（state=queued，2026-09-30 裁决：库为真源 + 进程内索引，ADR-0016）；
// 状态迁移一律经 Transit CAS（四件一拍的"状态"件，Outbox/审计由调用方
// 同事务组合）。
package deployment

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// State 是 Deployment 状态机值（领域模型 §4；含 admission 排队态）。
type State string

const (
	StateQueued      State = "queued"       // admission 已受理，等待单写者拾取
	StatePreparing   State = "preparing"    // Spec 归一化/材料装配/前置 Job
	StateBuilding    State = "building"     // 构建中（镜像引用可跳过）
	StateReleasing   State = "releasing"    // 投影 Workload 下发 Runtime（L1 门）
	StateObserving   State = "observing"    // L3 观察窗 + L2 看门狗常驻
	StateSucceeded   State = "succeeded"    // 终态
	StateFailed      State = "failed"       // 终态（自动触发回滚）
	StateRollingBack State = "rolling-back" // Replay 上一成功 Revision
	StateSuperseded  State = "superseded"   // 被显式 supersede 抢占的终态
	StateCancelled   State = "cancelled"    // 排队中/在途被取消的终态
)

// Terminal 报告是否终态（终态行不再参与 admission 与单写者驱动）。
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateSuperseded, StateCancelled:
		return true
	}
	return false
}

// Active 报告是否活跃态（admission 去重窗口：排队 + 在途 + 回滚中）。
func (s State) Active() bool {
	return s != "" && !s.Terminal()
}

// ActiveStates 是全部活跃态。
func ActiveStates() []State {
	return []State{
		StateQueued, StatePreparing, StateBuilding,
		StateReleasing, StateObserving, StateRollingBack,
	}
}

// ActiveStateStrings 是活跃态的 SQL IN 参数形态。
func ActiveStateStrings() []string {
	out := make([]string, len(ActiveStates()))
	for i, s := range ActiveStates() {
		out[i] = string(s)
	}
	return out
}

// ActiveStatesNoQueued 是在途态（admission 显式 supersede 的抢占面：
// preparing..rolling-back；queued 由 latest-wins 合并处理）。
func ActiveStatesNoQueued() []State {
	return []State{
		StatePreparing, StateBuilding, StateReleasing, StateObserving, StateRollingBack,
	}
}

// Deployment 是聚合行（部署记录永不删——审计单位）。
type Deployment struct {
	ID                string
	AppID             string
	FromRevision      string // 上一 Revision ID（首次部署为空）
	ToRevision        string // 目标 Revision ID
	State             State
	Generation        uint64 // 本 Deployment 拟下发的 Generation（单调）
	IdempotencyKey    string
	CommitSHA         string
	SupersededBy      string // 被哪个 Deployment 抢占（终态 superseded 时非空）
	Error             string // 失败原因（英文，用户可见）
	ObserveDeadline   string // 当前阶段绝对截止（job 等待 / L1 就绪等待 / L3 观察窗；RFC3339，空 = 不在限时阶段；消歧真源 = FirstBoot 游标，ADR-0030）
	FirstBoot         string // firstBootJobs 游标（'' 未开始 | '<idx>:<taskID>' 等待中 | 'done' 全部完成；ADR-0030）
	RollbackAttempted bool   // failed 自动回滚已尝试（=1 的 failed 是终态，不再自动重试）
	CreatedAt         string
	UpdatedAt         string
	FinishedAt        string
}

// firstBootDone 是 first_boot 游标的完成哨兵（区分"未开始"空值）。
const FirstBootDone = "done"

// FirstBootAnchor 解析游标：返回 (job 下标, 锚定 Task ID, 是否等待中)。
// 'done' 与非法形态返回 ok=false（等待外形态由调用方按序处理）。
func (d *Deployment) FirstBootAnchor() (idx int, taskID string, ok bool) {
	if d.FirstBoot == "" || d.FirstBoot == FirstBootDone {
		return 0, "", false
	}
	head, tail, found := strings.Cut(d.FirstBoot, ":")
	if !found || tail == "" {
		return 0, "", false
	}
	n, err := strconv.Atoi(head)
	if err != nil || n < 0 {
		return 0, "", false
	}
	return n, tail, true
}

// Repo 是 Deployment 聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（活跃态幂等键唯一索引防并发重复入队 → ErrAlreadyExists）。
func (r *Repo) Create(ctx context.Context, run state.Runner, d *Deployment) error {
	now := state.FormatTime(r.clock.Now())
	d.CreatedAt, d.UpdatedAt = now, now
	// first_boot 游标随行落库（提交方直落形态：回滚部署带 done 游标——
	// ADR-0030 决策 5；常规部署为空串 = 未开始）。
	_, err := run.ExecContext(ctx, `
		INSERT INTO deployments
			(id, app_id, from_revision, to_revision, state, generation,
			 idempotency_key, commit_sha, superseded_by, error, observe_deadline,
			 first_boot, created_at, updated_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, ?, '')`,
		d.ID, d.AppID, d.FromRevision, d.ToRevision, string(d.State), d.Generation,
		d.IdempotencyKey, d.CommitSHA, d.FirstBoot, d.CreatedAt, d.UpdatedAt)
	if state.IsUniqueViolation(err) {
		// 唯一索引作用域是 App 内（B10）：冲突 = 同 App 同键已有活跃行。
		return fmt.Errorf("%w: an active deployment in app %s already holds idempotency key %q",
			state.ErrAlreadyExists, d.AppID, d.IdempotencyKey)
	}
	return err
}

// Get 按 ID 直读。
func (r *Repo) Get(ctx context.Context, run state.Runner, id string) (*Deployment, error) {
	row := run.QueryRowContext(ctx, selectCols+" WHERE id = ?", id)
	return scanDeployment(row.Scan)
}

// ListByApp 返回 App 全部 Deployment（新→旧 + after 游标；ADR-0026
// after_* + limit 惯例——游标 = ULID 创建序）。limit<=0 或 >200 回落/钳制
// 缺省 50。
func (r *Repo) ListByApp(ctx context.Context, run state.Runner, appID, afterID string, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > maxListLimit {
		limit = defaultListLimit
	}
	q := selectCols + " WHERE app_id = ?"
	args := []any{appID}
	if afterID != "" {
		q += " AND id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ActiveByApp 返回 App 全部活跃态 Deployment（应至多一条在途 + 任意排队）。
func (r *Repo) ActiveByApp(ctx context.Context, run state.Runner, appID string) ([]Deployment, error) {
	states := ActiveStateStrings()
	args := append([]any{appID}, toAny(states)...)
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE app_id = ? AND state IN ("+placeholders(len(states))+") ORDER BY id",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// FindActiveByIdempotencyKey 返回该 App 内持该键的活跃 Deployment（无 →
// ErrNotFound）。键的作用域是 App（B10）：异 App 同键不命中——作用域
// 收紧后的正确语义，异 App 的键不在本 App 作用域内；存量迁移窗口若出现
// 跨 App 同键活跃行，对本 App 亦视同无键。
func (r *Repo) FindActiveByIdempotencyKey(ctx context.Context, run state.Runner, appID, key string) (*Deployment, error) {
	states := ActiveStateStrings()
	args := append([]any{appID, key}, toAny(states)...)
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE app_id = ? AND idempotency_key = ? AND state IN ("+placeholders(len(states))+") ORDER BY id LIMIT 1",
		args...)
	return scanDeployment(row.Scan)
}

// ListDriving 返回全部待驱动行：活跃态 + 待自动回滚的 failed（领域模型
// §4：failed → (自动) rolling-back；rollback_attempted=1 或无 from_revision
// 的 failed 是终态，不拾取）。
func (r *Repo) ListDriving(ctx context.Context, run state.Runner) ([]Deployment, error) {
	states := ActiveStateStrings()
	args := toAny(states)
	args = append(args, false)
	rows, err := run.QueryContext(ctx,
		selectCols+" WHERE state IN ("+placeholders(len(states))+")"+
			" OR (state = 'failed' AND rollback_attempted = ? AND from_revision != '')"+
			" ORDER BY id",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// NextGeneration 返回 App 下一 Generation（单调；Drift 判定与幂等重放的锚）。
func (r *Repo) NextGeneration(ctx context.Context, run state.Runner, appID string) (uint64, error) {
	var gen uint64
	err := run.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(generation), 0) FROM deployments WHERE app_id = ?`, appID).Scan(&gen)
	if err != nil {
		return 0, err
	}
	return gen + 1, nil
}

// LatestSucceeded 返回 App 最近一次 succeeded 的 Deployment（ADR-0022
// 启动基线重放的锚；无成功基线 → ErrNotFound）。
func (r *Repo) LatestSucceeded(ctx context.Context, run state.Runner, appID string) (*Deployment, error) {
	row := run.QueryRowContext(ctx,
		selectCols+" WHERE app_id = ? AND state = 'succeeded' ORDER BY id DESC LIMIT 1", appID)
	return scanDeployment(row.Scan)
}

// Transit 是状态 CAS：仅当当前状态 ∈ from 时迁移到 to（mut 可补充落
// error/finished_at/generation/superseded_by/observe_deadline 等字段）。
// 前置不符 → ErrConflict；行不存在 → ErrNotFound（四件一拍的"状态"件；
// UPDATE 带 state 前置条件作并发防御纵深，即使未来放开连接池也安全）。
func (r *Repo) Transit(ctx context.Context, run state.Runner, id string, from []State, to State, mut func(*Deployment)) error {
	cur, err := r.Get(ctx, run, id)
	if err != nil {
		return err
	}
	if !stateIn(from, cur.State) {
		return fmt.Errorf("%w: deployment %s is %s, want one of %s", state.ErrConflict, id, cur.State, joinStates(from))
	}
	origState := cur.State
	if mut != nil {
		mut(cur)
	}
	cur.State = to
	cur.UpdatedAt = state.FormatTime(r.clock.Now())
	if to.Terminal() && cur.FinishedAt == "" {
		cur.FinishedAt = cur.UpdatedAt
	}
	res, err := run.ExecContext(ctx, `
		UPDATE deployments SET
			state = ?, generation = ?, error = ?, superseded_by = ?,
			observe_deadline = ?, first_boot = ?, rollback_attempted = ?,
			to_revision = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND state = ?`,
		string(cur.State), cur.Generation, cur.Error, cur.SupersededBy,
		cur.ObserveDeadline, cur.FirstBoot, cur.RollbackAttempted, cur.ToRevision,
		cur.UpdatedAt, cur.FinishedAt,
		id, string(origState))
	if err != nil {
		return err
	}
	// CAS 纵深防御（Q-5）：前置 Get 与 UPDATE 之间状态被并发迁移时
	// RowsAffected=0——必须显式归一 ErrConflict，不得静默当成功（对照
	// secret/hook repo 的 RowsAffected 正例；单连接串行下不可达，放开
	// 连接池后是最后防线）。
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: deployment %s state changed concurrently, want one of %s",
			state.ErrConflict, id, joinStates(from))
	}
	return nil
}

const selectCols = `
	SELECT id, app_id, from_revision, to_revision, state, generation,
	       idempotency_key, commit_sha, superseded_by, error, observe_deadline,
	       first_boot, rollback_attempted, created_at, updated_at, finished_at
	FROM deployments`

func scanDeployment(scan func(dest ...any) error) (*Deployment, error) {
	var d Deployment
	var stateStr string
	var rollback int
	err := scan(&d.ID, &d.AppID, &d.FromRevision, &d.ToRevision, &stateStr, &d.Generation,
		&d.IdempotencyKey, &d.CommitSHA, &d.SupersededBy, &d.Error, &d.ObserveDeadline,
		&d.FirstBoot, &rollback, &d.CreatedAt, &d.UpdatedAt, &d.FinishedAt)
	if err != nil {
		return nil, state.MapScanErr(err)
	}
	d.State = State(stateStr)
	d.RollbackAttempted = rollback != 0
	return &d, nil
}

func stateIn(set []State, s State) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

func joinStates(set []State) string {
	parts := make([]string, len(set))
	for i, s := range set {
		parts[i] = string(s)
	}
	return strings.Join(parts, "|")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

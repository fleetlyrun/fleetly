package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// deployments 表读写（release-semantics §2.3 发布状态机的行驱动）：一行 =
// 一次部署的状态机载体。status 列（00001 建、无 CHECK）承载主状态词表
// queued/preparing/building/releasing/observing/succeeded/failed/cancelled；
// 00004 加法列承载子状态（phase=blocked_waiting）、失败分流判据
// （first_healthy_at）、同记录恢复（recovery）、判定（verdict）、停机账
// （downtime_ms）、看门狗/观察窗时间锚与期望态快照（desired_spec 密文）。
//
// 迁移全部走 from→to 谓词 + RowsAffected 校验（与 builds 同纪律）：终态
// 不可逆、并发扫描下恰好一个推进者胜出。事件与审计由调用方与业务写同事务
// 组合（fail-closed，state-model §2.9）；本层只提供行原语。

// DeploymentStatus 是发布状态机主状态（release-semantics §2.3）。
type DeploymentStatus string

const (
	// DeployQueued 已入队（同 app 互斥：等待在途部署终态）。
	DeployQueued DeploymentStatus = "queued"
	// DeployPreparing 准备中（compose 重载/放置解析/env 合并/镜像 preflight）。
	DeployPreparing DeploymentStatus = "preparing"
	// DeployBuilding 构建核对（build 层已完成则直通）。
	DeployBuilding DeploymentStatus = "building"
	// DeployReleasing 发布中（Swarm service 对账 + 健康门；含
	// blocked_waiting 子状态 = phase 列）。
	DeployReleasing DeploymentStatus = "releasing"
	// DeployObserving 观察窗（默认 60s，只告警）。
	DeployObserving DeploymentStatus = "observing"
	// DeploySucceeded 成功终态（观察窗通过；active revision 已前移）。
	DeploySucceeded DeploymentStatus = "succeeded"
	// DeployFailed 失败终态（error_code 必非空；归位/分流结果在同记录）。
	DeployFailed DeploymentStatus = "failed"
	// DeployCancelled 取消终态（先归位再落 cancelled）。
	DeployCancelled DeploymentStatus = "cancelled"
)

// Valid 报告状态是否为已定义状态位。
func (s DeploymentStatus) Valid() bool {
	switch s {
	case DeployQueued, DeployPreparing, DeployBuilding, DeployReleasing,
		DeployObserving, DeploySucceeded, DeployFailed, DeployCancelled:
		return true
	}
	return false
}

// Terminal 报告是否终态（succeeded/failed/cancelled）。
func (s DeploymentStatus) Terminal() bool {
	switch s {
	case DeploySucceeded, DeployFailed, DeployCancelled:
		return true
	}
	return false
}

// 非终态集合（重启恢复扫描与互斥检查的候选谓词）。
const nonTerminalStatuses = `('queued','preparing','building','releasing','observing')`

// deployment 子状态（phase 列词表；空 = 无子状态）。
const (
	// PhaseBlockedWaiting 发布中绑定节点 DOWN：看门狗暂停计时、可 cancel
	// （release-semantics §2.3，场景 15）。
	PhaseBlockedWaiting = "blocked_waiting"
)

// deployment verdict 词表（verdict 列；unstable 仅为 deployment 判定）。
const (
	// VerdictUnstable 已切流观察窗失败（app=degraded 的来源之一）。
	VerdictUnstable = "unstable"
)

// deployment recovery 词表（recovery 列；同记录恢复记录，D-REL-7）。
const (
	// RecoveryRestore 已按最后有效 revision 归位重放（未切流失败/cancel）。
	RecoveryRestore = "restore"
	// RecoveryBlocked 恢复被阻塞（引擎不可达等；退避重试可续跑）。
	RecoveryBlocked = "blocked"
)

// DeployRecord 是一次部署的状态机行。
type DeployRecord struct {
	ID string
	// AppID / AppName：app 外键与 compose 名（冗余快照便于跨进程渲染）。
	AppID   string
	AppName string
	// Kind ∈ deploy|rollback（00001 CHECK）：仅已切流回滚建新记录（D-REL-7）。
	Kind string
	// Status 是状态机主状态；Phase 是子状态（blocked_waiting 或空）。
	Status DeploymentStatus
	Phase  string
	// RevisionID 是成功后固化的版本行（成功时回填；可空）。
	RevisionID string
	// RecoveryOf 是 rollback 记录指向的原 deployment（可空）。
	RecoveryOf string
	// SubstrateHalted：首发失败 scale=0 保留现场（场景 14）。
	SubstrateHalted bool
	// FirstHealthyAt 是切流判据（零值 = 未切流，D-REL-4）。
	FirstHealthyAt time.Time
	// Recovery / Verdict：同记录恢复记录与终态判定（可空）。
	Recovery string
	Verdict  string
	// ErrorCode 是失败终态的注册表错误码（可空）。
	ErrorCode string
	// 停机账（stop-first 如实累计）。
	DowntimeMS        int64
	DowntimeStartedAt time.Time
	DowntimeEndedAt   time.Time
	// 看门狗/观察窗时间锚（零值 = 未进入对应阶段）。
	ReleaseStartedAt   time.Time
	WatchdogDeadlineAt time.Time
	ObserveStartedAt   time.Time
	// 期望态快照（spec_hash/env_snapshot_hash 明文哈希；desired_spec 为
	// box envelope 密文，本层不解释）。
	SpecHash        string
	EnvSnapshotHash string
	DesiredHash     string
	DesiredSpec     string
	ComposePath     string
	// CancelRequested：CLI 置位、引擎消费（曾健康拒绝并清位）。
	CancelRequested bool
	// Flags 是部署级告警/标志位（bitmask；PostWindowAlerted /
	// InstabilityWarning——L4 只告警一次与观察窗警告通过）。
	Flags     int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// 部署级标志位（deployments.flags，只增位不回收）。
const (
	// DeployFlagPostWindowAlerted：观察窗后不稳定已告警（L4 一次）。
	DeployFlagPostWindowAlerted int64 = 1 << 0
	// DeployFlagInstabilityWarning：观察窗警告通过（W_DEPLOY_INSTABILITY）。
	DeployFlagInstabilityWarning int64 = 1 << 1
)

// DeploymentNotFound 哨兵与状态迁移哨兵。
var (
	// ErrDeploymentNotFound 表示部署记录不存在。
	ErrDeploymentNotFound = errors.New("deployment not found")
	// ErrDeploymentStateTransition 表示部署状态迁移非法（终态不可逆、
	// 竞争落败等——调用方重读状态后裁决）。
	ErrDeploymentStateTransition = errors.New("invalid deployment state transition")
)

// DeploymentPatch 是一次行更新承载的可选字段（零值 = 不写；时间字段用
// 指针区分「不写」与「清空」——NULL 语义字段才允许置回 NULL）。
type DeploymentPatch struct {
	Status             *DeploymentStatus
	PrevStatus         *DeploymentStatus // 非 nil 时作 from 谓词（CAS 推进）
	Phase              *string
	RevisionID         *string
	SubstrateHalted    *bool
	FirstHealthyAt     *time.Time
	Recovery           *string
	Verdict            *string
	ErrorCode          *string
	DowntimeMS         *int64
	DowntimeStartedAt  *time.Time
	DowntimeEndedAt    *time.Time
	ReleaseStartedAt   *time.Time
	WatchdogDeadlineAt *time.Time
	ObserveStartedAt   *time.Time
	SpecHash           *string
	EnvSnapshotHash    *string
	DesiredHash        *string
	DesiredSpec        *string
	ComposePath        *string
	CancelRequested    *bool
	Flags              *int64
}

// CreateDeployment 创建 queued 部署行（发布入队）。kind 取 deploy|rollback；
// 事件/审计由调用方同事务组合（actor 语义在调用方：CLI=human、引擎=system）。
func (s *Store) CreateDeployment(ctx context.Context, rec DeployRecord) (DeployRecord, error) {
	if rec.Kind != "deploy" && rec.Kind != "rollback" {
		return DeployRecord{}, fmt.Errorf("state: create deployment: invalid kind %q", rec.Kind)
	}
	if rec.ID == "" {
		rec.ID = ulid.Make().String()
	}
	var recoveryOf any
	if rec.RecoveryOf != "" {
		recoveryOf = rec.RecoveryOf // FK 列：空串不落库（NULL = 无关联）
	}
	now := nowNano()
	const q = `INSERT INTO deployments
		(id, app_id, kind, status, revision_id, recovery_of, substrate_halted,
		 spec_hash, env_snapshot_hash, desired_hash, desired_spec, compose_path,
		 created_at, updated_at)
		VALUES (?, ?, ?, 'queued', NULL, ?, 0, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q,
		rec.ID, rec.AppID, rec.Kind, recoveryOf,
		rec.SpecHash, rec.EnvSnapshotHash, rec.DesiredHash, rec.DesiredSpec, rec.ComposePath,
		now, now)
	if err != nil {
		return DeployRecord{}, fmt.Errorf("state: insert deployment %s: %w", rec.ID, err)
	}
	rec.Status = DeployQueued
	rec.CreatedAt = time.Unix(0, now).UTC()
	rec.UpdatedAt = rec.CreatedAt
	return rec, nil
}

const deploymentScanCols = `d.id, d.app_id, a.name, d.kind, d.status, d.phase,
	d.revision_id, d.recovery_of, d.substrate_halted, d.first_healthy_at,
	d.recovery, d.verdict, d.error_code, d.downtime_ms, d.downtime_started_at,
	d.downtime_ended_at, d.release_started_at, d.watchdog_deadline_at,
	d.observe_started_at, d.spec_hash, d.env_snapshot_hash, d.desired_hash,
	d.desired_spec, d.compose_path, d.cancel_requested, d.flags,
	d.created_at, d.updated_at`

const deploymentScanFrom = ` FROM deployments d JOIN apps a ON a.id = d.app_id `

// GetDeployment 按ID取部署行；不存在返回 ErrDeploymentNotFound。
func (s *Store) GetDeployment(ctx context.Context, id string) (DeployRecord, error) {
	q := `SELECT ` + deploymentScanCols + deploymentScanFrom + `WHERE d.id = ?`
	return scanDeployment(s.db.QueryRowContext(ctx, q, id))
}

// ListAppDeployments 按应用返回部署记录（created_at 倒序，至多 limit 条）。
func (s *Store) ListAppDeployments(ctx context.Context, appID string, limit int) ([]DeployRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT ` + deploymentScanCols + deploymentScanFrom +
		`WHERE d.app_id = ? ORDER BY d.created_at DESC, d.id DESC LIMIT ?`
	return queryDeployments(ctx, s.db, q, appID, limit)
}

// ListNonTerminalDeployments 返回全部非终态部署（重启恢复扫描）。
func (s *Store) ListNonTerminalDeployments(ctx context.Context) ([]DeployRecord, error) {
	q := `SELECT ` + deploymentScanCols + deploymentScanFrom +
		`WHERE d.status IN ` + nonTerminalStatuses + ` ORDER BY d.created_at ASC, d.id ASC`
	return queryDeployments(ctx, s.db, q)
}

// NextQueuedDeployments 返回最早入队的至多 limit 条 queued 记录（FIFO）。
func (s *Store) NextQueuedDeployments(ctx context.Context, limit int) ([]DeployRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `SELECT ` + deploymentScanCols + deploymentScanFrom +
		`WHERE d.status = 'queued' ORDER BY d.created_at ASC, d.id ASC LIMIT ?`
	return queryDeployments(ctx, s.db, q, limit)
}

// AppHasNonTerminalDeployment 报告应用是否存在在途部署（同 app 互斥）。
func (s *Store) AppHasNonTerminalDeployment(ctx context.Context, appID string) (bool, error) {
	q := `SELECT COUNT(1) FROM deployments WHERE app_id = ? AND status IN ` + nonTerminalStatuses
	var n int64
	if err := s.db.QueryRowContext(ctx, q, appID).Scan(&n); err != nil {
		return false, fmt.Errorf("state: count non-terminal deployments: %w", err)
	}
	return n > 0, nil
}

// UpdateDeployment 按补丁更新部署行。PrevStatus 非 nil 时为 CAS 迁移
// （当前状态 ≠ PrevStatus → ErrDeploymentStateTransition，RowsAffected=0）；
// 为 nil 时不带状态谓词（子状态/时间锚/告警位等就地更新）。updated_at 恒
// 重盖。同一补丁内 Status 与字段一并原子生效。
func (s *Store) UpdateDeployment(ctx context.Context, id string, p DeploymentPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{nowNano()}
	if p.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, string(*p.Status))
	}
	if p.Phase != nil {
		sets = append(sets, "phase = ?")
		args = append(args, *p.Phase)
	}
	if p.RevisionID != nil {
		sets = append(sets, "revision_id = ?")
		args = append(args, *p.RevisionID)
	}
	if p.SubstrateHalted != nil {
		sets = append(sets, "substrate_halted = ?")
		args = append(args, boolToInt(*p.SubstrateHalted))
	}
	if p.FirstHealthyAt != nil {
		sets = append(sets, "first_healthy_at = ?")
		args = append(args, p.FirstHealthyAt.UnixNano())
	}
	if p.Recovery != nil {
		sets = append(sets, "recovery = ?")
		args = append(args, *p.Recovery)
	}
	if p.Verdict != nil {
		sets = append(sets, "verdict = ?")
		args = append(args, *p.Verdict)
	}
	if p.ErrorCode != nil {
		sets = append(sets, "error_code = ?")
		args = append(args, *p.ErrorCode)
	}
	if p.DowntimeMS != nil {
		sets = append(sets, "downtime_ms = ?")
		args = append(args, *p.DowntimeMS)
	}
	if p.DowntimeStartedAt != nil {
		sets = append(sets, "downtime_started_at = ?")
		args = append(args, p.DowntimeStartedAt.UnixNano())
	}
	if p.DowntimeEndedAt != nil {
		sets = append(sets, "downtime_ended_at = ?")
		args = append(args, p.DowntimeEndedAt.UnixNano())
	}
	if p.ReleaseStartedAt != nil {
		sets = append(sets, "release_started_at = ?")
		args = append(args, p.ReleaseStartedAt.UnixNano())
	}
	if p.WatchdogDeadlineAt != nil {
		sets = append(sets, "watchdog_deadline_at = ?")
		args = append(args, p.WatchdogDeadlineAt.UnixNano())
	}
	if p.ObserveStartedAt != nil {
		sets = append(sets, "observe_started_at = ?")
		args = append(args, p.ObserveStartedAt.UnixNano())
	}
	if p.SpecHash != nil {
		sets = append(sets, "spec_hash = ?")
		args = append(args, *p.SpecHash)
	}
	if p.EnvSnapshotHash != nil {
		sets = append(sets, "env_snapshot_hash = ?")
		args = append(args, *p.EnvSnapshotHash)
	}
	if p.DesiredHash != nil {
		sets = append(sets, "desired_hash = ?")
		args = append(args, *p.DesiredHash)
	}
	if p.DesiredSpec != nil {
		sets = append(sets, "desired_spec = ?")
		args = append(args, *p.DesiredSpec)
	}
	if p.ComposePath != nil {
		sets = append(sets, "compose_path = ?")
		args = append(args, *p.ComposePath)
	}
	if p.CancelRequested != nil {
		sets = append(sets, "cancel_requested = ?")
		args = append(args, boolToInt(*p.CancelRequested))
	}
	if p.Flags != nil {
		sets = append(sets, "flags = ?")
		args = append(args, *p.Flags)
	}
	q := `UPDATE deployments SET ` + sets[0]
	for _, s := range sets[1:] {
		q += ", " + s //nolint:gosec // G202：sets 全为编译期字面量白名单，值经 ? 参数绑定
	}
	q += ` WHERE id = ?`
	args = append(args, id)
	if p.PrevStatus != nil {
		q += ` AND status = ?`
		args = append(args, string(*p.PrevStatus))
	}
	if p.Status != nil {
		// 终态不可逆是结构性保证：任何状态改写都不得作用于终态行
		//（succeeded/failed/cancelled 无出边，release-semantics §2.3）。
		q += ` AND status NOT IN ('succeeded','failed','cancelled')`
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("state: update deployment %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read deployment update count: %w", err)
	}
	if n == 0 {
		if p.PrevStatus != nil {
			return ErrDeploymentStateTransition
		}
		return ErrDeploymentNotFound
	}
	return nil
}

// scanDeployment 从单行构造 DeployRecord（app 名经 JOIN 带出）。
func scanDeployment(row interface{ Scan(dest ...any) error }) (DeployRecord, error) {
	var r DeployRecord
	var kind, status string
	var revisionID, recoveryOf, recovery, verdict, errorCode sql.NullString
	var substrateHalted, cancelRequested int64
	var flags int64
	var downtimeMS int64
	var firstHealthy, downtimeStarted, downtimeEnded, releaseStarted,
		watchdogDeadline, observeStarted sql.NullInt64
	var specHash, envSnapshotHash, desiredHash, desiredSpec, composePath string
	var phase string
	var created, updated int64
	err := row.Scan(&r.ID, &r.AppID, &r.AppName, &kind, &status, &phase,
		&revisionID, &recoveryOf, &substrateHalted, &firstHealthy,
		&recovery, &verdict, &errorCode, &downtimeMS, &downtimeStarted,
		&downtimeEnded, &releaseStarted, &watchdogDeadline, &observeStarted,
		&specHash, &envSnapshotHash, &desiredHash, &desiredSpec, &composePath,
		&cancelRequested, &flags, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeployRecord{}, ErrDeploymentNotFound
		}
		return DeployRecord{}, fmt.Errorf("state: scan deployment: %w", err)
	}
	r.Kind = kind
	r.Status = DeploymentStatus(status)
	r.Phase = phase
	r.RevisionID = revisionID.String
	r.RecoveryOf = recoveryOf.String
	r.SubstrateHalted = substrateHalted != 0
	r.Recovery = recovery.String
	r.Verdict = verdict.String
	r.ErrorCode = errorCode.String
	r.DowntimeMS = downtimeMS
	r.FirstHealthyAt = nullTime(firstHealthy)
	r.DowntimeStartedAt = nullTime(downtimeStarted)
	r.DowntimeEndedAt = nullTime(downtimeEnded)
	r.ReleaseStartedAt = nullTime(releaseStarted)
	r.WatchdogDeadlineAt = nullTime(watchdogDeadline)
	r.ObserveStartedAt = nullTime(observeStarted)
	r.SpecHash = specHash
	r.EnvSnapshotHash = envSnapshotHash
	r.DesiredHash = desiredHash
	r.DesiredSpec = desiredSpec
	r.ComposePath = composePath
	r.CancelRequested = cancelRequested != 0
	r.Flags = flags
	r.CreatedAt = time.Unix(0, created).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	return r, nil
}

// nullTime 把可空整数列转为 time.Time（NULL = 零值）。
func nullTime(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return time.Unix(0, v.Int64).UTC()
}

// queryDeployments 执行多行部署查询并按序返回。
func queryDeployments(ctx context.Context, db *sql.DB, query string, args ...any) ([]DeployRecord, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: query deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DeployRecord
	for rows.Next() {
		rec, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate deployments: %w", err)
	}
	return out, nil
}

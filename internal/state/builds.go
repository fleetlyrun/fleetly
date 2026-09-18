package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// 构建记录（task-breakdown T2.8/T2.9、architecture §2.2 构建行）：状态机
// queued → building → succeeded | failed，全部迁移与审计同事务（fail-closed，
// 同 app.env_set 纪律）。构建阶段不广播事件——eventcode 注册表（35 项）无
// build.* 事件名且只增不发明；可观测性经本表读取通道（fleetly builds
// list）与 audit_log（build.create / build.start / build.finish）承载。
// 镜像身份：image_ref ↔ image_digest 即 T2.9 的本机 digest→ref 登记
// （部署引用取 digest，D9）。

// BuildStatus 是构建状态机的状态位。
type BuildStatus string

const (
	// BuildQueued 已入队待执行。
	BuildQueued BuildStatus = "queued"
	// BuildBuilding 执行中。
	BuildBuilding BuildStatus = "building"
	// BuildSucceeded 成功终态（image_digest 必非空）。
	BuildSucceeded BuildStatus = "succeeded"
	// BuildFailed 失败终态（error_code 必非空）。
	BuildFailed BuildStatus = "failed"
)

// Valid 报告状态是否为已定义状态位。
func (s BuildStatus) Valid() bool {
	switch s {
	case BuildQueued, BuildBuilding, BuildSucceeded, BuildFailed:
		return true
	}
	return false
}

// Driver 是构建驱动（与 compose 服务 build/image 两模式对应）。
type Driver string

const (
	// DriverRailpack 无 build.dockerfile → Railpack 自动检出（钉版 v0.39.0，
	// internal/build）。
	DriverRailpack Driver = "railpack"
	// DriverDockerfile 有 build.dockerfile → Dockerfile 前端（一等路径）。
	DriverDockerfile Driver = "dockerfile"
	// DriverPassthrough 仅 image 模式直通（无构建；v0.1 不建行，枚举位预留）。
	DriverPassthrough Driver = "passthrough"
)

// Valid 报告驱动是否为已定义值。
func (d Driver) Valid() bool {
	switch d {
	case DriverRailpack, DriverDockerfile, DriverPassthrough:
		return true
	}
	return false
}

// BuildRecord 是一次构建的权威记录行。
type BuildRecord struct {
	ID       string
	AppID    string
	Service  string
	Driver   Driver
	Status   BuildStatus
	ImageRef string
	// ImageDigest 是不可变镜像 ID（`sha256:<hex>`）；成功终态必非空（D9）。
	ImageDigest string
	// Request 是构建输入 JSON（构建队列的跨进程执行输入）。
	Request string
	// PlanPath / LogPath 是产物归档路径（railpack plan JSON / 构建日志）。
	PlanPath string
	LogPath  string
	// ErrorCode 是失败终态的注册表错误码。
	ErrorCode string
	CreatedAt time.Time
	// StartedAt / FinishedAt：零值 = 未开始 / 未到终态。
	StartedAt  time.Time
	FinishedAt time.Time
}

// 构建记录哨兵错误。
var (
	// ErrBuildNotFound 表示构建记录不存在。
	ErrBuildNotFound = errors.New("build not found")
	// ErrBuildStateTransition 表示构建状态迁移非法（终态不可逆、claim
	// 竞争失败等——调用方应重读状态后裁决）。
	ErrBuildStateTransition = errors.New("invalid build state transition")
)

// CreateBuild 创建 queued 构建记录（构建入队；id 留空自动生成 ULID）。
// 审计 build.create 与建行同事务（fail-closed）。
func (s *Store) CreateBuild(ctx context.Context, rec BuildRecord) (BuildRecord, error) {
	var created BuildRecord
	err := s.InTx(ctx, func(tx *Tx) error {
		b, err := tx.CreateBuild(ctx, rec)
		if err != nil {
			return err
		}
		created = b
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "build.create",
			Target:      "app:" + rec.AppID,
			Result:      "ok",
			DiffSummary: `{"build":"` + b.ID + `","service":"` + rec.Service + `","driver":"` + string(rec.Driver) + `"}`,
		})
	})
	if err != nil {
		return BuildRecord{}, err
	}
	return created, nil
}

// CreateBuild 是事务内创建 queued 构建记录。
func (t *Tx) CreateBuild(ctx context.Context, rec BuildRecord) (BuildRecord, error) {
	if rec.ID == "" {
		rec.ID = ulid.Make().String()
	}
	if rec.Status != "" && rec.Status != BuildQueued {
		return BuildRecord{}, fmt.Errorf("state: create build %s: initial status must be queued, got %s", rec.ID, rec.Status)
	}
	if !rec.Driver.Valid() {
		return BuildRecord{}, fmt.Errorf("state: create build %s: invalid driver %q", rec.ID, rec.Driver)
	}
	request := rec.Request
	if request == "" {
		request = "{}"
	}
	now := nowNano()
	const q = `INSERT INTO builds
		(id, app_id, service, driver, status, image_ref, image_digest, request, plan_path, log_path, error_code, created_at)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, NULL, ?)`
	if _, err := t.ExecContext(ctx, q,
		rec.ID, rec.AppID, rec.Service, string(rec.Driver),
		rec.ImageRef, rec.ImageDigest, request, rec.PlanPath, rec.LogPath, now); err != nil {
		return BuildRecord{}, fmt.Errorf("state: insert build %s: %w", rec.ID, err)
	}
	return BuildRecord{
		ID:        rec.ID,
		AppID:     rec.AppID,
		Service:   rec.Service,
		Driver:    rec.Driver,
		Status:    BuildQueued,
		Request:   request,
		CreatedAt: time.Unix(0, now).UTC(),
	}, nil
}

// GetBuild 按ID取构建记录；不存在返回 ErrBuildNotFound。
func (s *Store) GetBuild(ctx context.Context, id string) (BuildRecord, error) {
	const q = `SELECT id, app_id, service, driver, status, image_ref, image_digest,
		request, plan_path, log_path, error_code, created_at, started_at, finished_at
		FROM builds WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanBuild(row)
}

// ListAppBuilds 按应用返回构建记录（created_at 倒序，至多 limit 条；
// limit ≤ 0 取默认 50）。
func (s *Store) ListAppBuilds(ctx context.Context, appID string, limit int) ([]BuildRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `SELECT id, app_id, service, driver, status, image_ref, image_digest,
		request, plan_path, log_path, error_code, created_at, started_at, finished_at
		FROM builds WHERE app_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, appID, limit)
	if err != nil {
		return nil, fmt.Errorf("state: query builds for app %s: %w", appID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []BuildRecord
	for rows.Next() {
		rec, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate builds: %w", err)
	}
	return out, nil
}

// NextQueuedBuilds 返回最早入队的至多 limit 条 queued 记录（FIFO 排队语义，
// 队列 worker 的候选扫描；claim 由 ClaimBuild 原子完成）。
func (s *Store) NextQueuedBuilds(ctx context.Context, limit int) ([]BuildRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `SELECT id, app_id, service, driver, status, image_ref, image_digest,
		request, plan_path, log_path, error_code, created_at, started_at, finished_at
		FROM builds WHERE status = 'queued' ORDER BY created_at ASC, id ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("state: query queued builds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BuildRecord
	for rows.Next() {
		rec, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate queued builds: %w", err)
	}
	return out, nil
}

// ClaimBuild 原子认领：queued → building（started_at 盖章），审计
// build.start 同事务。ErrBuildStateTransition 表示已被认领/终态（多 worker/
// 重复扫描竞争的安全路径——行级谓词保证恰好一个 claimer 胜出）。
func (s *Store) ClaimBuild(ctx context.Context, id string) error {
	return s.transitionBuild(ctx, id, BuildQueued, BuildBuilding, "started_at", "build.start",
		`{"build":"`+id+`"}`)
}

// FinishBuildSucceeded 推进 building → succeeded 并落镜像身份（ref + 不可变
// digest，D9）。终态谓词防重复收敛；审计 build.finish 同事务。
func (s *Store) FinishBuildSucceeded(ctx context.Context, id, imageRef, imageDigest, planPath, logPath string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		res, err := tx.ExecContext(ctx,
			`UPDATE builds SET status = 'succeeded', image_ref = ?, image_digest = ?,
				plan_path = ?, log_path = ?, finished_at = ?
			WHERE id = ? AND status = 'building'`,
			imageRef, imageDigest, planPath, logPath, now, id)
		if err != nil {
			return fmt.Errorf("state: finish build %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read finish count: %w", err)
		}
		if n == 0 {
			return ErrBuildStateTransition
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "build.finish",
			Target:      "build:" + id,
			Result:      "ok",
			DiffSummary: `{"ref":"` + imageRef + `","digest":"` + imageDigest + `"}`,
		})
	})
	if err != nil {
		return fmt.Errorf("state: finish build %s succeeded: %w", id, err)
	}
	return nil
}

// FinishBuildFailed 推进 building → failed 并落注册表错误码；审计
// build.finish（result=error）同事务。
func (s *Store) FinishBuildFailed(ctx context.Context, id, errorCode string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		now := nowNano()
		res, err := tx.ExecContext(ctx,
			`UPDATE builds SET status = 'failed', error_code = ?, finished_at = ?
			WHERE id = ? AND status = 'building'`,
			errorCode, now, id)
		if err != nil {
			return fmt.Errorf("state: fail build %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read fail count: %w", err)
		}
		if n == 0 {
			return ErrBuildStateTransition
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "build.finish",
			Target:      "build:" + id,
			Result:      "error",
			ErrorCode:   errorCode,
			DiffSummary: `{"build":"` + id + `"}`,
		})
	})
	if err != nil {
		return fmt.Errorf("state: finish build %s failed: %w", id, err)
	}
	return nil
}

// transitionBuild 执行 from → to 的构建状态迁移并同事务写审计。
func (s *Store) transitionBuild(ctx context.Context, id string, from, to BuildStatus, stampCol, action, diffSummary string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE builds SET status = ?, `+stampCol+` = ?
			WHERE id = ? AND status = ?`,
			string(to), nowNano(), id, string(from))
		if err != nil {
			return fmt.Errorf("state: update build status: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read build status update count: %w", err)
		}
		if n == 0 {
			return ErrBuildStateTransition
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      action,
			Target:      "build:" + id,
			Result:      "ok",
			DiffSummary: diffSummary,
		})
	})
	if err != nil {
		return fmt.Errorf("state: transition build %s %s→%s: %w", id, from, to, err)
	}
	return nil
}

// FindBuildsByDigest 按不可变镜像 ID 反查登记（digest→ref 映射读通道，
// T2.9 镜像身份；返回含该 digest 的全部成功构建行）。
func (s *Store) FindBuildsByDigest(ctx context.Context, digest string) ([]BuildRecord, error) {
	const q = `SELECT id, app_id, service, driver, status, image_ref, image_digest,
		request, plan_path, log_path, error_code, created_at, started_at, finished_at
		FROM builds WHERE image_digest = ? ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, digest)
	if err != nil {
		return nil, fmt.Errorf("state: query builds by digest: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BuildRecord
	for rows.Next() {
		rec, err := scanBuild(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate builds by digest: %w", err)
	}
	return out, nil
}

// scanBuild 从单行构造 BuildRecord（row 接口同时覆盖 *sql.Row 与 *sql.Rows）。
func scanBuild(row interface{ Scan(dest ...any) error }) (BuildRecord, error) {
	var r BuildRecord
	var driver, status string
	var created int64
	var started, finished sql.NullInt64
	var errorCode sql.NullString
	err := row.Scan(&r.ID, &r.AppID, &r.Service, &driver, &status, &r.ImageRef, &r.ImageDigest,
		&r.Request, &r.PlanPath, &r.LogPath, &errorCode, &created, &started, &finished)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BuildRecord{}, ErrBuildNotFound
		}
		return BuildRecord{}, fmt.Errorf("state: scan build: %w", err)
	}
	r.Driver = Driver(driver)
	r.Status = BuildStatus(status)
	if errorCode.Valid {
		r.ErrorCode = errorCode.String
	}
	r.CreatedAt = time.Unix(0, created).UTC()
	if started.Valid {
		r.StartedAt = time.Unix(0, started.Int64).UTC()
	}
	if finished.Valid {
		r.FinishedAt = time.Unix(0, finished.Int64).UTC()
	}
	return r, nil
}

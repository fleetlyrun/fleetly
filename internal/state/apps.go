package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// 应用权威态与 tombstone-first 删除（state-model §2.6）：删除走
// deleting → deleted 状态位 + 保留期；恢复（DB 回填/备份重放）不得把
// deleted 行翻回 active（「恢复不复活」，apps_test.go 钉死）。state 层
// 用哨兵错误表达生命周期冲突，HTTP 码映射随 API 面票落地。

// AppLifecycle 是应用生命周期状态位。
type AppLifecycle string

const (
	// LifecycleActive 正常在册。
	LifecycleActive AppLifecycle = "active"
	// LifecycleDeleting 删除进行中（deleting，等待清理与保留期）。
	LifecycleDeleting AppLifecycle = "deleting"
	// LifecycleDeleted 已删除（tombstone，保留期内名字仍占用）。
	LifecycleDeleted AppLifecycle = "deleted"
)

// 应用生命周期哨兵错误。
var (
	// ErrAppExists 表示同名应用已在册（任何生命周期态名字均占用）。
	ErrAppExists = errors.New("app name already registered")
	// ErrAppTombstoned 表示目标应用处于删除状态位（deleting/deleted）：
	// 不可复活、不可在其上继续业务写。
	ErrAppTombstoned = errors.New("app is tombstoned")
	// ErrAppNotFound 表示应用不存在。
	ErrAppNotFound = errors.New("app not found")
	// ErrInvalidLifecycleTransition 表示生命周期状态位迁移非法
	// （状态机：active → deleting → deleted，不可跳越、不可回退）。
	ErrInvalidLifecycleTransition = errors.New("invalid app lifecycle transition")
)

// App 是应用权威态行（v0.1 最小面：期望态根 + tombstone 状态位）。
type App struct {
	ID         string
	Name       string
	Lifecycle  AppLifecycle
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletingAt time.Time // 零值 = 未进入删除
	DeletedAt  time.Time // 零值 = 未完成删除
}

// CreateApp 创建应用行（active）。同名（任意生命周期态）返回
// ErrAppExists——deleted 名字在保留期内不释放，不复活、也不顶替。
// appID 留空自动生成 ULID。
func (s *Store) CreateApp(ctx context.Context, appID, name string) (App, error) {
	var created App
	err := s.InTx(ctx, func(tx *Tx) error {
		app, err := tx.CreateApp(ctx, appID, name)
		if err != nil {
			return err
		}
		created = app
		return nil
	})
	if err != nil {
		return App{}, err
	}
	return created, nil
}

// CreateApp 是事务内创建应用。
func (t *Tx) CreateApp(ctx context.Context, appID, name string) (App, error) {
	if appID == "" {
		appID = ulid.Make().String()
	}
	now := nowNano()
	const q = `INSERT INTO apps (id, name, lifecycle, created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?)`
	if _, err := t.ExecContext(ctx, q, appID, name, now, now); err != nil {
		if isUniqueViolation(err) {
			return App{}, fmt.Errorf("%w: %s", ErrAppExists, name)
		}
		return App{}, fmt.Errorf("state: insert app %s: %w", name, err)
	}
	return App{
		ID:        appID,
		Name:      name,
		Lifecycle: LifecycleActive,
		CreatedAt: time.Unix(0, now).UTC(),
		UpdatedAt: time.Unix(0, now).UTC(),
	}, nil
}

// GetAppByName 按名取应用行；不存在返回 ErrAppNotFound。
func (s *Store) GetAppByName(ctx context.Context, name string) (App, error) {
	const q = `SELECT id, name, lifecycle, created_at, updated_at, deleting_at, deleted_at
		FROM apps WHERE name = ?`
	row := s.db.QueryRowContext(ctx, q, name)
	return scanApp(row)
}

// GetAppByID 按平台 ID 取应用行；不存在返回 ErrAppNotFound（入口路由
// 合成时的 app 名反查消费）。
func (s *Store) GetAppByID(ctx context.Context, id string) (App, error) {
	const q = `SELECT id, name, lifecycle, created_at, updated_at, deleting_at, deleted_at
		FROM apps WHERE id = ?`
	row := s.db.QueryRowContext(ctx, q, id)
	return scanApp(row)
}

// scanApp 从单行构造 App（row 接口同时覆盖 *sql.Row 与 *sql.Rows）。
func scanApp(row interface{ Scan(dest ...any) error }) (App, error) {
	var a App
	var lifecycle string
	var created, updated int64
	var deleting, deleted sql.NullInt64
	if err := row.Scan(&a.ID, &a.Name, &lifecycle, &created, &updated, &deleting, &deleted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return App{}, ErrAppNotFound
		}
		return App{}, fmt.Errorf("state: scan app: %w", err)
	}
	a.Lifecycle = AppLifecycle(lifecycle)
	a.CreatedAt = time.Unix(0, created).UTC()
	a.UpdatedAt = time.Unix(0, updated).UTC()
	if deleting.Valid {
		a.DeletingAt = time.Unix(0, deleting.Int64).UTC()
	}
	if deleted.Valid {
		a.DeletedAt = time.Unix(0, deleted.Int64).UTC()
	}
	return a, nil
}

// MarkAppDeleting 推进 active → deleting（tombstone 第一拍）。仅允许从
// active 出发；deleting/deleted 重复进入返回 ErrInvalidLifecycleTransition。
func (s *Store) MarkAppDeleting(ctx context.Context, appID string) error {
	return s.transitionApp(ctx, appID, LifecycleActive, LifecycleDeleting, "deleting_at")
}

// MarkAppDeleting 是事务内 tombstone 第一拍（供与审计/事件同事务组合——
// T2.17 API 删除走 fail-closed 审计）。
func (t *Tx) MarkAppDeleting(ctx context.Context, appID string) error {
	res, err := t.ExecContext(ctx,
		`UPDATE apps SET lifecycle = ?, updated_at = ?, deleting_at = ?
		WHERE id = ? AND lifecycle = ?`,
		string(LifecycleDeleting), nowNano(), nowNano(), appID, string(LifecycleActive))
	if err != nil {
		return fmt.Errorf("state: update app lifecycle: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read lifecycle update count: %w", err)
	}
	if n == 0 {
		return ErrInvalidLifecycleTransition
	}
	return nil
}

// MarkAppDeleted 推进 deleting → deleted（tombstone 第二拍）。仅允许从
// deleting 出发——active 直达 deleted 被拒绝（状态机纪律，防跳过清理）。
func (s *Store) MarkAppDeleted(ctx context.Context, appID string) error {
	return s.transitionApp(ctx, appID, LifecycleDeleting, LifecycleDeleted, "deleted_at")
}

// MarkAppDeleted 是事务内 tombstone 第二拍（H10/MG-3：与终局事件/审计
// 同事务组合的形态——引擎 deleting 回收 duty 在受管服务全部移除后原子
// 落终态，进程在「迁移已落、事件未发」之间崩溃的披露缺口不存在）。
func (t *Tx) MarkAppDeleted(ctx context.Context, appID string) error {
	res, err := t.ExecContext(ctx,
		`UPDATE apps SET lifecycle = ?, updated_at = ?, deleted_at = ?
		WHERE id = ? AND lifecycle = ?`,
		string(LifecycleDeleted), nowNano(), nowNano(), appID, string(LifecycleDeleting))
	if err != nil {
		return fmt.Errorf("state: update app lifecycle: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("state: read lifecycle update count: %w", err)
	}
	if n == 0 {
		return ErrInvalidLifecycleTransition
	}
	return nil
}

// transitionApp 执行 from → to 的生命周期迁移并盖对应时间位列。
func (s *Store) transitionApp(ctx context.Context, appID string, from, to AppLifecycle, stampCol string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET lifecycle = ?, updated_at = ?, `+stampCol+` = ?
			WHERE id = ? AND lifecycle = ?`,
			string(to), nowNano(), nowNano(), appID, string(from))
		if err != nil {
			return fmt.Errorf("state: update app lifecycle: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read lifecycle update count: %w", err)
		}
		if n == 0 {
			return ErrInvalidLifecycleTransition
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("state: transition app %s %s→%s: %w", appID, from, to, err)
	}
	return nil
}

// isUniqueViolation 报告 err 是否为 SQLite 唯一约束冲突（modernc 驱动
// 暂无公开错误码类型，按错误串归类；只用于 ErrAppExists 归因，不作为
// 安全判定）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

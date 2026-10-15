// Package catalog 是 App 模板目录快照聚合 repo 的 SQLite 实现：行类型、
// ErrNoSnapshot 哨兵与 Repo 接口住 domain/delivery/catalog（ADR-0075 Repo
// 接口层），本包只持不导出实现与构造。
package catalog

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/catalog"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain catalog.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain catalog.Repo）。
func New(clock state.Clock) catalog.Repo { return &repo{clock: clock} }

// Put 原子覆盖快照（整体替换；空 body 是编程错误由约束/调用侧承载）。
func (r *repo) Put(ctx context.Context, run state.Runner, s *catalog.Snapshot) error {
	s.FetchedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO template_catalog (id, digest, body, fetched_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET digest = excluded.digest, body = excluded.body, fetched_at = excluded.fetched_at`,
		s.Digest, s.Body, s.FetchedAt)
	return err
}

// Get 读快照（缺席 → ErrNoSnapshot；Scan 归一哨兵由 persistence.MapScanErr）。
func (r *repo) Get(ctx context.Context, run state.Runner) (*catalog.Snapshot, error) {
	row := run.QueryRowContext(ctx, `SELECT digest, body, fetched_at FROM template_catalog WHERE id = 1`)
	s := &catalog.Snapshot{}
	if err := row.Scan(&s.Digest, &s.Body, &s.FetchedAt); err != nil {
		if errors.Is(persistence.MapScanErr(err), state.ErrNotFound) {
			return nil, catalog.ErrNoSnapshot
		}
		return nil, err
	}
	return s, nil
}

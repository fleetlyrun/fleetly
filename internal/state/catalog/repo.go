// Package catalog 是 App 模板目录快照聚合（F3.3，ADR-0050 决策 4）：
// 单行表（id=1），RefreshTemplates 成功后的整体替换语义。解析序 =
// 快照在场优先、内嵌目录兜底（内嵌在 internal/apptemplate，不落库）。
package catalog

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// ErrNoSnapshot 是快照缺席的哨兵（回退内嵌目录的正常态，非错误面）。
var ErrNoSnapshot = errors.New("catalog: no refreshed snapshot stored")

// Snapshot 是快照行（body 是清单 JSON 原文；条目级 digest 已在写入前核对）。
type Snapshot struct {
	Digest    string
	Body      string
	FetchedAt string
}

// Repo 是快照存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Put 原子覆盖快照（整体替换；空 body 是编程错误由约束/调用侧承载）。
func (r *Repo) Put(ctx context.Context, run state.Runner, s *Snapshot) error {
	s.FetchedAt = state.FormatTime(r.clock.Now())
	_, err := run.ExecContext(ctx, `
		INSERT INTO template_catalog (id, digest, body, fetched_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET digest = excluded.digest, body = excluded.body, fetched_at = excluded.fetched_at`,
		s.Digest, s.Body, s.FetchedAt)
	return err
}

// Get 读快照（缺席 → ErrNoSnapshot；Scan 归一哨兵由 state.MapScanErr）。
func (r *Repo) Get(ctx context.Context, run state.Runner) (*Snapshot, error) {
	row := run.QueryRowContext(ctx, `SELECT digest, body, fetched_at FROM template_catalog WHERE id = 1`)
	s := &Snapshot{}
	if err := row.Scan(&s.Digest, &s.Body, &s.FetchedAt); err != nil {
		if errors.Is(state.MapScanErr(err), state.ErrNotFound) {
			return nil, ErrNoSnapshot
		}
		return nil, err
	}
	return s, nil
}

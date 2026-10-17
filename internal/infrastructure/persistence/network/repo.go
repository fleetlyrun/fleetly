// Package networkrepo 是 Network 聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/structure/network（ADR-0075 Repo 接口层），本包只持不
// 导出实现与构造。实现侧包名 networkrepo 避开与内部网络实现的混淆
// （域侧即包名 network）。
package networkrepo

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain network.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain network.Repo）。
func New(clock state.Clock, run state.Runner) network.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 Project 同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, n *network.Network) error {
	seq, err := persistence.NextSeq(ctx, r.run, "networks")
	if err != nil {
		return err
	}
	n.Seq = seq
	if n.ID == "" {
		n.ID = entityid.Join(n.Name, seq)
	}
	now := state.FormatTime(r.clock.Now())
	n.CreatedAt = now
	egress := 0
	if n.EgressNone {
		egress = 1
	}
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO networks (id, seq, project_id, name, egress_none, created_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, '')`,
		n.ID, n.Seq, n.ProjectID, n.Name, egress, n.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: network name %q already exists in project", state.ErrAlreadyExists, n.Name)
	}
	return err
}

// GetByID 按 ID 直读活跃行（peer 声明解析面：声明行只挂活跃网络）。
func (r *repo) GetByID(ctx context.Context, id string) (*network.Network, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE id = ? AND deleted_at = ''`, id)
	return scanNetwork(row.Scan)
}

// GetByName 读活跃行。
func (r *repo) GetByName(ctx context.Context, projectID, name string) (*network.Network, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE project_id = ? AND name = ? AND deleted_at = ''`, projectID, name)
	return scanNetwork(row.Scan)
}

// ListByProject 返回 Project 全部活跃网络（全量面——配额检查与引擎挂网
// 装配消费；API 分页读面走 ListByProjectPage）。
func (r *repo) ListByProject(ctx context.Context, projectID string) ([]network.Network, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE project_id = ? AND deleted_at = '' ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []network.Network
	for rows.Next() {
		n, err := scanNetwork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// ListByProjectPage 是 ListByProject 的分页读面（ADR-0026 after_* + limit；
// 游标轴 = 既有排序轴 name 字典序升序——既有响应序不变）。limit<=0 或
// >200 回落/钳制缺省 50。
func (r *repo) ListByProjectPage(ctx context.Context, projectID, afterName string, limit int) ([]network.Network, error) {
	limit = persistence.ClampLimit(limit, defaultListLimit, maxListLimit)
	q := `
		SELECT id, seq, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE project_id = ? AND deleted_at = ''`
	args := []any{projectID}
	if afterName != "" {
		q += ` AND name > ?`
		args = append(args, afterName)
	}
	q += ` ORDER BY name ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []network.Network
	for rows.Next() {
		n, err := scanNetwork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// List 返回全部活跃网络（跨 Project；受管 Proxy 挂网的全量真源，N0 修复
// 批 B1）。
func (r *repo) List(ctx context.Context) ([]network.Network, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, project_id, name, egress_none, created_at, deleted_at
		FROM networks WHERE deleted_at = '' ORDER BY project_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []network.Network
	for rows.Next() {
		n, err := scanNetwork(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func scanNetwork(scan func(dest ...any) error) (*network.Network, error) {
	var n network.Network
	var egress int
	err := scan(&n.ID, &n.Seq, &n.ProjectID, &n.Name, &egress, &n.CreatedAt, &n.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	n.EgressNone = egress != 0
	return &n, nil
}

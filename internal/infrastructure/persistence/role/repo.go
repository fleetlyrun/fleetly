// Package role 是 Role 聚合 repo 的 SQLite 实现：行类型与 Repo 接口住
// domain/identity/role（ADR-0075 Repo 接口层），本包只持不导出实现与
// 构造。
package role

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain role.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
	run   state.Runner
}

// New 构造 repo（直接返回接口——消费面只见 domain role.Repo）。
func New(clock state.Clock, run state.Runner) role.Repo { return &repo{clock: clock, run: run} }

// Create 落一行；同 Team 内同名冲突返回 state.ErrAlreadyExists。
func (r *repo) Create(ctx context.Context, ro *role.Role) error {
	seq, err := persistence.NextSeq(ctx, r.run, "roles")
	if err != nil {
		return err
	}
	ro.Seq = seq
	if ro.ID == "" {
		ro.ID = entityid.Join(ro.Name, seq)
	}
	ro.CreatedAt = state.FormatTime(r.clock.Now())
	scopes, err := json.Marshal(ro.Scopes)
	if err != nil {
		return fmt.Errorf("role: scopes json: %w", err)
	}
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ro.ID, ro.Seq, ro.TeamID, ro.Name, boolInt(ro.Builtin), string(scopes), ro.CreatedAt)
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: role name %q already exists", state.ErrAlreadyExists, ro.Name)
	}
	return err
}

// UpsertBuiltin 落/刷内置角色（固定 slug ID；scopes 由代码单一源刷新——
// 词表演进时启动即同步，不待人工迁移）。幂等刷新不耗 seq 号：先 UPDATE，
// 零行 = 首播 → 铸 seq 落行（ADR-0070）。
func (r *repo) UpsertBuiltin(ctx context.Context, ro *role.Role) error {
	ro.CreatedAt = state.FormatTime(r.clock.Now())
	scopes, err := json.Marshal(ro.Scopes)
	if err != nil {
		return fmt.Errorf("role: scopes json: %w", err)
	}
	res, err := r.run.ExecContext(ctx,
		`UPDATE roles SET scopes = ? WHERE id = ?`, string(scopes), ro.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	seq, err := persistence.NextSeq(ctx, r.run, "roles")
	if err != nil {
		return err
	}
	ro.Seq = seq
	_, err = r.run.ExecContext(ctx, `
		INSERT INTO roles (id, seq, team_id, name, builtin, scopes, created_at)
		VALUES (?, ?, '', ?, 1, ?, ?)`,
		ro.ID, ro.Seq, ro.Name, string(scopes), ro.CreatedAt)
	return err
}

// Get 按 ID 直读。
func (r *repo) Get(ctx context.Context, id string) (*role.Role, error) {
	row := r.run.QueryRowContext(ctx, `
		SELECT id, seq, team_id, name, builtin, scopes, created_at
		FROM roles WHERE id = ?`, id)
	return scanRole(row.Scan)
}

// List 返回全部 Role（内置在前、ID 序稳定）。
func (r *repo) List(ctx context.Context) ([]role.Role, error) {
	rows, err := r.run.QueryContext(ctx, `
		SELECT id, seq, team_id, name, builtin, scopes, created_at
		FROM roles ORDER BY builtin DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []role.Role
	for rows.Next() {
		ro, err := scanRole(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *ro)
	}
	return out, rows.Err()
}

// Delete 按 ID 删行。内置角色拒绝删（代码单一源）；仍被 Token/membership
// 引用时 FK RESTRICT 归一为 state.ErrConflict。
func (r *repo) Delete(ctx context.Context, id string) error {
	ro, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if ro.Builtin {
		return fmt.Errorf("%w: builtin role %q cannot be deleted", state.ErrConflict, ro.Name)
	}
	res, err := r.run.ExecContext(ctx, `DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		if persistence.IsUniqueViolation(err) || persistence.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: role %s is still referenced by tokens or users", state.ErrConflict, id)
		}
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

func scanRole(scan func(dest ...any) error) (*role.Role, error) {
	var ro role.Role
	var builtin int
	var scopes string
	if err := scan(&ro.ID, &ro.Seq, &ro.TeamID, &ro.Name, &builtin, &scopes, &ro.CreatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	ro.Builtin = builtin != 0
	if scopes != "" {
		if err := json.Unmarshal([]byte(scopes), &ro.Scopes); err != nil {
			return nil, fmt.Errorf("role: scopes json: %w", err)
		}
	}
	return &ro, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Package githubapp 是 GitHub App 接入材料聚合 repo 的 SQLite 实现：行
// 类型与 Repo 接口住 domain/delivery/githubapp（ADR-0075 Repo 接口层），
// 本包只持不导出实现与构造（密文列投影/rewrap 机械在此）。
package githubapp

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// repo 是 domain githubapp.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain githubapp.Repo）。
func New(clock state.Clock) githubapp.Repo { return &repo{clock: clock} }

// Upsert 写入（受理即验证后调用：调用方持封装材料与已解析属主）。同作
// 用域同属主的活跃行覆盖更新（私钥轮换 = 重放 put，fingerprint 变更即
// 新钥）；异属主落新行。
func (r *repo) Upsert(ctx context.Context, run state.Runner, a *githubapp.App) error {
	now := state.FormatTime(r.clock.Now())
	if existing, err := r.getActiveByOwner(ctx, run, a.ProjectID, a.AccountLogin); err == nil {
		_, err := run.ExecContext(ctx, `
			UPDATE github_apps SET app_id = ?, installation_id = ?, api_base = ?,
				private_key_ciphertext = ?, private_key_fingerprint = ?, updated_at = ?, deleted_at = ''
			WHERE id = ?`,
			a.AppID, a.InstallationID, a.APIBase, a.PrivateKeyCiphertext, a.PrivateKeyFingerprint,
			now, existing.ID)
		a.ID, a.Seq, a.CreatedAt = existing.ID, existing.Seq, existing.CreatedAt
		a.UpdatedAt = now
		return err
	} else if err != state.ErrNotFound {
		return err
	}
	a.CreatedAt, a.UpdatedAt = now, now
	seq, err := persistence.NextSeq(ctx, run, "github_apps")
	if err != nil {
		return err
	}
	a.Seq = seq
	if a.ID == "" {
		a.ID = entityid.Join("ghapp", seq)
	}
	if _, err := run.ExecContext(ctx, `
		INSERT INTO github_apps (id, seq, project_id, app_id, installation_id, account_login,
			api_base, private_key_ciphertext, private_key_fingerprint, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		a.ID, a.Seq, a.ProjectID, a.AppID, a.InstallationID, a.AccountLogin,
		a.APIBase, a.PrivateKeyCiphertext, a.PrivateKeyFingerprint, a.CreatedAt, a.UpdatedAt); err != nil {
		if persistence.IsUniqueViolation(err) {
			return state.ErrAlreadyExists
		}
		return err
	}
	return nil
}

// Get 按 id 直读（含密文——轮换/维护路径；回显面走 List 族）。
func (r *repo) Get(ctx context.Context, run state.Runner, id string) (*githubapp.App, error) {
	row := run.QueryRowContext(ctx, selectColumns+` WHERE id = ?`, id)
	return scanOne(row.Scan)
}

// GetByProjectAndOwner 构建期解析（Project 级，遮蔽 Global）：含密文——
// 仅 clone 材料路径调用；回显面走 List 族。
func (r *repo) GetByProjectAndOwner(ctx context.Context, run state.Runner, projectID, login string) (*githubapp.App, error) {
	row := run.QueryRowContext(ctx, selectColumns+`
		WHERE project_id = ? AND account_login = ? AND deleted_at = ''`, projectID, login)
	return scanOne(row.Scan)
}

// GetGlobalByOwner 构建期解析（平台级回落）：含密文，同上仅材料路径。
func (r *repo) GetGlobalByOwner(ctx context.Context, run state.Runner, login string) (*githubapp.App, error) {
	row := run.QueryRowContext(ctx, selectColumns+`
		WHERE project_id = '' AND account_login = ? AND deleted_at = ''`, login)
	return scanOne(row.Scan)
}

// ListByProject 返回 Project 活跃行的指纹面（回显契约：密文 SQL 层 NULL
// 投影，值永不出现）。account_login 字典序升序。
func (r *repo) ListByProject(ctx context.Context, run state.Runner, projectID string) ([]githubapp.App, error) {
	return r.list(ctx, run, `
		SELECT `+fingerprintColumns+` FROM github_apps
		WHERE project_id = ? AND deleted_at = '' ORDER BY account_login`, projectID)
}

// ListGlobal 返回平台级活跃行的指纹面（同上回显契约）。
func (r *repo) ListGlobal(ctx context.Context, run state.Runner) ([]githubapp.App, error) {
	return r.list(ctx, run, `
		SELECT `+fingerprintColumns+` FROM github_apps
		WHERE project_id = '' AND deleted_at = '' ORDER BY account_login`)
}

// ListAll 读全表行（含 tombstone）：离线维护面（KEK 重封）专用——墓碑
// 行的密文同样要可解，含密文列故禁止回显面消费。
func (r *repo) ListAll(ctx context.Context, run state.Runner) ([]githubapp.App, error) {
	rows, err := run.QueryContext(ctx, selectColumns)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []githubapp.App
	for rows.Next() {
		a, err := scanOne(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdatePrivateKeyCiphertext 重写密文（KEK 重封专用：fingerprint 是明文
// 的函数，重封不变）。行不存在不报错——调用方（rewrap）的行列表与本写
// 同窗口，无并发删除面。
func (r *repo) UpdatePrivateKeyCiphertext(ctx context.Context, run state.Runner, id string, ciphertext []byte) error {
	_, err := run.ExecContext(ctx, `
		UPDATE github_apps SET private_key_ciphertext = ?, updated_at = ? WHERE id = ?`,
		ciphertext, state.FormatTime(r.clock.Now()), id)
	return err
}

// SoftDelete 落 tombstone（幂等：已删行归 nil；行不存在 = ErrNotFound）。
func (r *repo) SoftDelete(ctx context.Context, run state.Runner, id string) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE github_apps SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at = ''`,
		now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := r.Get(ctx, run, id)
		return err
	}
	return nil
}

// getActiveByOwner 活跃行查找（Upsert 的更新臂判定）。
func (r *repo) getActiveByOwner(ctx context.Context, run state.Runner, projectID, login string) (*githubapp.App, error) {
	row := run.QueryRowContext(ctx, selectColumns+`
		WHERE project_id = ? AND account_login = ? AND deleted_at = ''`, projectID, login)
	return scanOne(row.Scan)
}

func (r *repo) list(ctx context.Context, run state.Runner, q string, args ...any) ([]githubapp.App, error) {
	rows, err := run.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []githubapp.App
	for rows.Next() {
		a, err := scanOne(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// selectColumns 是全列投影（含密文——仅材料/维护路径消费）。
const selectColumns = `
	SELECT id, seq, project_id, app_id, installation_id, account_login,
		api_base, private_key_ciphertext, private_key_fingerprint, created_at, updated_at, deleted_at
	FROM github_apps`

// fingerprintColumns 是回显面投影：密文列 SQL 层 NULL（secret.ListFinger
// prints 同款执法——读面在构造上就摸不到密文）。
const fingerprintColumns = `
	id, seq, project_id, app_id, installation_id, account_login,
	api_base, NULL, private_key_fingerprint, created_at, updated_at, deleted_at`

func scanOne(scan func(dest ...any) error) (*githubapp.App, error) {
	var a githubapp.App
	err := scan(&a.ID, &a.Seq, &a.ProjectID, &a.AppID, &a.InstallationID, &a.AccountLogin,
		&a.APIBase, &a.PrivateKeyCiphertext, &a.PrivateKeyFingerprint, &a.CreatedAt, &a.UpdatedAt, &a.DeletedAt)
	if err != nil {
		return nil, persistence.MapScanErr(err)
	}
	return &a, nil
}

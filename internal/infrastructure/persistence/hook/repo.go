// Package hook 是 Git 触发配置聚合 repo 的 SQLite 实现：行类型与 Repo
// 接口住 domain/delivery/hook（ADR-0075 Repo 接口层），本包只持不导出
// 实现与构造。
package hook

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

// deliveryRetention 是重投去重行的保留窗（收侧顺带清理，防无界增长）。
const deliveryRetention = 7 * 24 * 3600 // 秒

// repo 是 domain hook.Repo 的 SQLite 实现。
type repo struct {
	clock state.Clock
}

// New 构造 repo（直接返回接口——消费面只见 domain hook.Repo）。
func New(clock state.Clock) hook.Repo { return &repo{clock: clock} }

// Create 落一行（首配铸造路径：Token 材料随行落）。
func (r *repo) Create(ctx context.Context, run state.Runner, h *hook.Hook) error {
	now := state.FormatTime(r.clock.Now())
	h.CreatedAt, h.UpdatedAt = now, now
	paths, err := marshalPaths(h.WatchPaths)
	if err != nil {
		return err
	}
	_, err = run.ExecContext(ctx, `
		INSERT INTO app_hooks (app_id, repo, branch, dockerfile, watch_paths, token_sha256, token_prefix, secret_ciphertext, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.AppID, h.Repo, h.Branch, h.Dockerfile, paths, h.TokenSHA256, h.TokenPrefix, h.SecretCiphertext, h.CreatedAt, h.UpdatedAt)
	// 双唯一键（app_id 主键 / token_sha256）命中归一为 ErrAlreadyExists：
	// SetGitHook 的 Get 先行是读面判定，并发交错（首配竞态）落此处——
	// 调用方重试即走 UpdateConfig 臂。githubapp 同款形态。
	if persistence.IsUniqueViolation(err) {
		return fmt.Errorf("%w: hook for app %q already exists", state.ErrAlreadyExists, h.AppID)
	}
	return err
}

// UpdateConfig 只改配置字段（已配置过的 Set 路径：不换 Token 材料）。
func (r *repo) UpdateConfig(ctx context.Context, run state.Runner, h *hook.Hook) error {
	h.UpdatedAt = state.FormatTime(r.clock.Now())
	paths, err := marshalPaths(h.WatchPaths)
	if err != nil {
		return err
	}
	res, err := run.ExecContext(ctx, `
		UPDATE app_hooks SET repo = ?, branch = ?, dockerfile = ?, watch_paths = ?, updated_at = ?
		WHERE app_id = ?`,
		h.Repo, h.Branch, h.Dockerfile, paths, h.UpdatedAt, h.AppID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// Get 按 App 直读。
func (r *repo) Get(ctx context.Context, run state.Runner, appID string) (*hook.Hook, error) {
	row := run.QueryRowContext(ctx, selectColumns+` WHERE app_id = ?`, appID)
	return scanOne(row.Scan)
}

// GetByTokenSHA256 按 URL token 查找（接收面的入口查询键）。
func (r *repo) GetByTokenSHA256(ctx context.Context, run state.Runner, sha string) (*hook.Hook, error) {
	row := run.QueryRowContext(ctx, selectColumns+` WHERE token_sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// RotateToken 双换 Token 材料（URL token 与 webhook secret 同源；旧行即
// 刻失效——查询键已换）。
func (r *repo) RotateToken(ctx context.Context, run state.Runner, appID, sha, prefix string, ciphertext []byte) error {
	now := state.FormatTime(r.clock.Now())
	res, err := run.ExecContext(ctx, `
		UPDATE app_hooks SET token_sha256 = ?, token_prefix = ?, secret_ciphertext = ?, updated_at = ?
		WHERE app_id = ?`, sha, prefix, ciphertext, now, appID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return state.ErrNotFound
	}
	return nil
}

// DeleteByApp 随 App tombstone 级联移除整个聚合（与 tombstone 同事务）：
// 去重行先清（FK hook_deliveries.app_id REFERENCES app_hooks，留着会让
// 删除本身撞 FOREIGN KEY constraint failed——FB-1），hook 行后清。孤儿
// hook 会让配置面对已删 App 继续成功、接收面却按活跃行拒绝——两面语义
// 分裂（ADR-0072 批真机咬出：删除 App 后 rotate 仍成功、webhook 401）。
func (r *repo) DeleteByApp(ctx context.Context, run state.Runner, appID string) error {
	if _, err := run.ExecContext(ctx, `DELETE FROM hook_deliveries WHERE app_id = ?`, appID); err != nil {
		return err
	}
	_, err := run.ExecContext(ctx, `DELETE FROM app_hooks WHERE app_id = ?`, appID)
	return err
}

// ListAll 读全表行：离线维护面（KEK 重封）专用（含 webhook secret 信封
// 列，禁止回显面消费）。
func (r *repo) ListAll(ctx context.Context, run state.Runner) ([]hook.Hook, error) {
	rows, err := run.QueryContext(ctx, selectColumns)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []hook.Hook
	for rows.Next() {
		h, err := scanOne(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// UpdateSecretCiphertext 重封 webhook secret 信封（KEK 重封专用；URL
// token 材料与信封无关，不动）。
func (r *repo) UpdateSecretCiphertext(ctx context.Context, run state.Runner, appID string, ciphertext []byte) error {
	_, err := run.ExecContext(ctx, `
		UPDATE app_hooks SET secret_ciphertext = ?, updated_at = ? WHERE app_id = ?`,
		ciphertext, state.FormatTime(r.clock.Now()), appID)
	return err
}

// RecordDelivery 落一行重投去重锚；返回 true = 该 delivery 已出现过
// （重投）。顺带清理保留窗外的旧行。
func (r *repo) RecordDelivery(ctx context.Context, run state.Runner, appID, delivery string) (bool, error) {
	now := r.clock.Now()
	cutoff := state.FormatTime(now.Add(-deliveryRetention * 1e9))
	if _, err := run.ExecContext(ctx,
		`DELETE FROM hook_deliveries WHERE received_at < ?`, cutoff); err != nil {
		return false, err
	}
	res, err := run.ExecContext(ctx, `
		INSERT OR IGNORE INTO hook_deliveries (app_id, delivery, received_at) VALUES (?, ?, ?)`,
		appID, delivery, state.FormatTime(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 0, nil
}

const selectColumns = `
	SELECT app_id, repo, branch, dockerfile, watch_paths, token_sha256, token_prefix, secret_ciphertext, created_at, updated_at
	FROM app_hooks`

func scanOne(scan func(dest ...any) error) (*hook.Hook, error) {
	var h hook.Hook
	var paths string
	if err := scan(&h.AppID, &h.Repo, &h.Branch, &h.Dockerfile, &paths,
		&h.TokenSHA256, &h.TokenPrefix, &h.SecretCiphertext, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, persistence.MapScanErr(err)
	}
	if err := unmarshalPaths(paths, &h.WatchPaths); err != nil {
		return nil, err
	}
	return &h, nil
}

func marshalPaths(paths []string) (string, error) {
	if paths == nil {
		paths = []string{}
	}
	b, err := json.Marshal(paths)
	if err != nil {
		return "", fmt.Errorf("hook: watch paths: %w", err)
	}
	return string(b), nil
}

func unmarshalPaths(s string, out *[]string) error {
	if s == "" {
		*out = nil
		return nil
	}
	return json.Unmarshal([]byte(s), out)
}

// Package hook 是 per-App Git 触发配置聚合 repo（F0.13）：URL token 与
// GitHub webhook secret 同源——token_sha256 是 URL 查找键，secret_
// ciphertext 是 age 信封（HMAC 验签需原串，纯摘要不可逆）；明文只在
// 铸造响应出现一次。
package hook

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// deliveryRetention 是重投去重行的保留窗（收侧顺带清理，防无界增长）。
const deliveryRetention = 7 * 24 * 3600 // 秒

// Hook 是聚合行（WatchPaths 以 JSON 数组落库；SecretCiphertext 是 age 信封）。
type Hook struct {
	AppID            string
	Repo             string
	Branch           string
	Dockerfile       string
	WatchPaths       []string
	TokenSHA256      string
	TokenPrefix      string
	SecretCiphertext []byte
	CreatedAt        string
	UpdatedAt        string
}

// Repo 是 Git 触发配置聚合存取。
type Repo struct {
	clock state.Clock
}

// New 构造 repo。
func New(clock state.Clock) *Repo { return &Repo{clock: clock} }

// Create 落一行（首配铸造路径：Token 材料随行落）。
func (r *Repo) Create(ctx context.Context, run state.Runner, h *Hook) error {
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
	return err
}

// UpdateConfig 只改配置字段（已配置过的 Set 路径：不换 Token 材料）。
func (r *Repo) UpdateConfig(ctx context.Context, run state.Runner, h *Hook) error {
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
func (r *Repo) Get(ctx context.Context, run state.Runner, appID string) (*Hook, error) {
	row := run.QueryRowContext(ctx, selectColumns+` WHERE app_id = ?`, appID)
	return scanOne(row.Scan)
}

// GetByTokenSHA256 按 URL token 查找（接收面的入口查询键）。
func (r *Repo) GetByTokenSHA256(ctx context.Context, run state.Runner, sha string) (*Hook, error) {
	row := run.QueryRowContext(ctx, selectColumns+` WHERE token_sha256 = ?`, sha)
	return scanOne(row.Scan)
}

// RotateToken 双换 Token 材料（URL token 与 webhook secret 同源；旧行即
// 刻失效——查询键已换）。
func (r *Repo) RotateToken(ctx context.Context, run state.Runner, appID, sha, prefix string, ciphertext []byte) error {
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

// ListAll 读全表行：离线维护面（KEK 重封）专用（含 webhook secret 信封
// 列，禁止回显面消费）。
func (r *Repo) ListAll(ctx context.Context, run state.Runner) ([]Hook, error) {
	rows, err := run.QueryContext(ctx, selectColumns)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // 只读列表，关闭错误无处置面
	var out []Hook
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
func (r *Repo) UpdateSecretCiphertext(ctx context.Context, run state.Runner, appID string, ciphertext []byte) error {
	_, err := run.ExecContext(ctx, `
		UPDATE app_hooks SET secret_ciphertext = ?, updated_at = ? WHERE app_id = ?`,
		ciphertext, state.FormatTime(r.clock.Now()), appID)
	return err
}

// RecordDelivery 落一行重投去重锚；返回 true = 该 delivery 已出现过
// （重投）。顺带清理保留窗外的旧行。
func (r *Repo) RecordDelivery(ctx context.Context, run state.Runner, appID, delivery string) (bool, error) {
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

func scanOne(scan func(dest ...any) error) (*Hook, error) {
	var h Hook
	var paths string
	if err := scan(&h.AppID, &h.Repo, &h.Branch, &h.Dockerfile, &paths,
		&h.TokenSHA256, &h.TokenPrefix, &h.SecretCiphertext, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, state.MapScanErr(err)
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

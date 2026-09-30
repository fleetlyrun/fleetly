package authn

// Bootstrap Token 首启流（F0.6 含吊销 + F0.2 前半）：无用户且无在册
// bootstrap Token 时生成一次——owner 全权、明文只落数据根文件（0600）；
// journal 追加去重锚，防止"文件被删 + 重启"再生成第二个全域 Token
// （bootstrap 数量恒 ≤1，可审计）。完整 `fleetly init` 收口在安装引导批。

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/state/token"
	"github.com/fleetlyrun/fleetly/internal/state/user"
)

const (
	// bootstrapTokenFile 是明文分发面（数据根下；取走后可删）。
	bootstrapTokenFile = "bootstrap-token"
	// journalFile 是 append-only 去重锚（数据根下；与库内 tokens 行双锚）。
	journalFile = "journal"
	// journalMarker 是 journal 中的生成事实行标记。
	journalMarker = "bootstrap-token generated"
)

// BootstrapResult 是一次首启检查的结果（夹具与日志消费）。
type BootstrapResult struct {
	Generated bool   // 本次是否新生成
	Secret    string // 新生成时的明文（仅内存驻留；落文件后由调用方处置）
}

// EnsureBootstrapToken 幂等执行首启流：种子 → 在册检查 → journal 检查 →
// 生成（库 + 文件 + journal 同批落）。resources 是 scope 词表（assembly
// 单一源注入——本包不复制词表）。
func EnsureBootstrapToken(ctx context.Context, db *state.DB, dataRoot string, resources []string, log *slog.Logger) (BootstrapResult, error) {
	if err := EnsureSeed(ctx, db, resources); err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: seed: %w", err)
	}
	tokens := tokenrepo.New(db.Clock())
	users := user.New(db.Clock())

	alive, err := tokens.HasAliveBootstrap(ctx, db.Runner())
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: bootstrap check: %w", err)
	}
	if alive {
		// 在册即不再生成；无用户且文件已失时提示恢复路径（明文 sha256
		// 存储不可找回，诚实暴露）。
		count, err := users.Count(ctx, db.Runner())
		if err == nil && count == 0 {
			if _, statErr := os.Stat(filepath.Join(dataRoot, bootstrapTokenFile)); os.IsNotExist(statErr) {
				log.Warn("bootstrap token exists but its file is gone and no users exist; " +
					"recovery needs fleetlyd data access (revoke the stale token after regaining access)")
			}
		}
		return BootstrapResult{}, nil
	}

	journalPath := filepath.Join(dataRoot, journalFile)
	if journalHasMarker(journalPath) {
		log.Warn("bootstrap token was generated before and is not alive (revoked); " +
			"first-run bootstrap will not regenerate it")
		return BootstrapResult{}, nil
	}

	material, err := identity.NewToken()
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: token material: %w", err)
	}
	row := &tokenrepo.Token{
		ID: ulid.Make().String(), Name: identity.BootstrapTokenName,
		TeamID: identity.DefaultTeamID, RoleID: identity.RoleOwnerID,
		SHA256: material.SHA256, Prefix: material.Prefix,
	}
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		return tokens.Create(ctx, tx, row)
	}); err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: persist bootstrap token: %w", err)
	}

	tokenPath := filepath.Join(dataRoot, bootstrapTokenFile)
	if err := os.MkdirAll(dataRoot, 0o750); err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: create data root: %w", err)
	}
	if err := os.WriteFile(tokenPath, []byte(material.Secret+"\n"), 0o600); err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: write bootstrap token file: %w", err)
	}
	if err := appendJournal(journalPath, db.Clock(), material.SHA256); err != nil {
		return BootstrapResult{}, fmt.Errorf("authn: append journal: %w", err)
	}
	log.Info("bootstrap token generated",
		"file", tokenPath, "sha256_prefix", material.SHA256[:8],
		"hint", "log in with it, create an admin user, then revoke it")
	return BootstrapResult{Generated: true, Secret: material.Secret}, nil
}

// journalHasMarker 报告 journal 是否已有生成事实行（文件缺失视为无）。
func journalHasMarker(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // 数据根内自有 journal
	if err != nil {
		return false
	}
	return strings.Contains(string(data), journalMarker)
}

// appendJournal 追加一行生成事实（RFC3339 + sha256 锚）。
func appendJournal(path string, clock state.Clock, sha string) error {
	line := state.FormatTime(clock.Now()) + " " + journalMarker + " sha256=" + sha + "\n"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // 数据根内自有 journal
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // 追加打开的关闭错误无处置面
	_, err = f.WriteString(line)
	return err
}

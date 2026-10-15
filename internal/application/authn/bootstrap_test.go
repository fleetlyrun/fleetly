package authn

// 首启流 hermetic 测试：生成一次、幂等、journal 去重（防"删文件重启再
// 生成"）、吊销后不再生成。

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	membershiprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/membership"
	rolerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/role"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	teamrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/team"
	tokenrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/token"
	userrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/user"
)

var testVocab = []string{"projects", "apps", "deployments", "users", "tokens", "audit"}

// testSeedRepos 构造首启面真 repo 集（hermetic 夹具可引 persistence——
// 生产构造面在组合根，P1b C 批）。
func testSeedRepos(clock state.Clock) SeedRepos {
	return SeedRepos{
		Teams: teamrepo.New(clock), Roles: rolerepo.New(clock),
		Tokens: tokenrepo.New(clock), Users: userrepo.New(clock),
	}
}

// testAuthnRepos 构造身份执法面真 repo 集（同上）。
func testAuthnRepos(clock state.Clock) Repos {
	return Repos{
		Roles: rolerepo.New(clock), Users: userrepo.New(clock), Tokens: tokenrepo.New(clock),
		Memberships: membershiprepo.New(clock), Audits: auditrepo.New(clock),
	}
}

func TestEnsureBootstrapToken(t *testing.T) {
	db, _ := statetest.New(t)
	ctx := context.Background()
	root := t.TempDir()
	log := slog.New(slog.DiscardHandler)

	res, err := EnsureBootstrapToken(ctx, db, testSeedRepos(db.Clock()), root, testVocab, log)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !res.Generated || !strings.HasPrefix(res.Secret, "flt_") {
		t.Fatalf("first run must generate, got %+v", res)
	}

	// 文件形态：单行明文。
	fileSecret, err := os.ReadFile(filepath.Join(root, "bootstrap-token")) //nolint:gosec // 测试夹具 temp 目录
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if strings.TrimSpace(string(fileSecret)) != res.Secret {
		t.Fatalf("file secret mismatch: %q vs %q", fileSecret, res.Secret)
	}

	// 库内行：name=bootstrap、owner 角色、sha256 可反查。
	tokens := tokenrepo.New(db.Clock())
	row, err := tokens.GetBySHA256(ctx, db.Runner(), identity.HashToken(res.Secret))
	if err != nil {
		t.Fatalf("lookup by sha: %v", err)
	}
	if row.Name != identity.BootstrapTokenName || row.RoleID != identity.RoleOwnerID || row.Revoked {
		t.Fatalf("row = %+v", row)
	}

	// journal 有去重锚。
	if !journalHasMarker(filepath.Join(root, "journal")) {
		t.Fatal("journal missing generation marker")
	}

	// 第二次运行：库内在册 → 不生成。
	again, err := EnsureBootstrapToken(ctx, db, testSeedRepos(db.Clock()), root, testVocab, log)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Generated {
		t.Fatal("alive bootstrap must suppress regeneration")
	}

	// 删文件 + 删库行（模拟灾难）→ journal 拦住重新生成。
	if err := os.Remove(filepath.Join(root, "bootstrap-token")); err != nil {
		t.Fatal(err)
	}
	err = db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE name = ?`, identity.BootstrapTokenName)
		return err
	})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	purge, err := EnsureBootstrapToken(ctx, db, testSeedRepos(db.Clock()), root, testVocab, log)
	if err != nil {
		t.Fatalf("post-purge run: %v", err)
	}
	if purge.Generated {
		t.Fatal("journal marker must suppress regeneration after file+row loss")
	}
}

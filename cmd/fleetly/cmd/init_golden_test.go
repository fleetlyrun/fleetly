package cmd

// init golden 双形态（F0.2 收口）：全新夹具（bootstrap 存活）→ init 消费
// → 旧 bootstrap 401 → 凭据文件续链 whoami → 已初始化后再 init 诚实报错。
// init 一次性消费 bootstrap（默认吊销），人类/机器两轮各用独立夹具。

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestGoldenInitHuman(t *testing.T) {
	h := newGoldenHarness(t)
	t.Setenv("FLEETLY_CREDENTIALS", filepath.Join(t.TempDir(), "credentials"))

	code, out, stderr := runCLI(t, "init", "--token", h.Token, "alice")
	if code != 0 || stderr != "" {
		t.Fatalf("init: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "init", normalizeGolden(out))

	// 默认吊销已生效：bootstrap 的下一个调用即 401。
	users := identityv1.NewUsersServiceClient(h.Conn)
	_, err := users.WhoAmI(sdk.WithToken(context.Background(), h.Token), &identityv1.WhoAmIRequest{})
	require.Error(t, err, "bootstrap must be revoked by init")
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// 凭据文件里已是新 token：清掉 env 注入的 bootstrap 后 whoami 续链
	//（conn 解析序 flag > env > file）。
	t.Setenv("FLEETLY_TOKEN", "")
	code, out, stderr = runCLI(t, "whoami")
	if code != 0 || stderr != "" {
		t.Fatalf("whoami via saved credentials: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "init-whoami", normalizeGolden(out))

	// 已初始化后再 init：bootstrap 已死，401 信封诚实呈现（信封渲染优
	// 先于外层包装文案——App>err.Error）。
	code, _, stderr = runCLI(t, "init", "--token", h.Token, "bob")
	if code == 0 {
		t.Fatalf("re-init with a dead bootstrap token must fail")
	}
	assert.Contains(t, stderr, "E_UNAUTHENTICATED")
}

func TestGoldenInitJSON(t *testing.T) {
	h := newGoldenHarness(t)
	t.Setenv("FLEETLY_CREDENTIALS", filepath.Join(t.TempDir(), "credentials"))

	code, out, stderr := runCLI(t, "init", "--token", h.Token, "--json", "alice")
	if code != 0 || stderr != "" {
		t.Fatalf("init --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "init-json", normalizeGolden(out))
}

func TestInitKeepBootstrap(t *testing.T) {
	h := newGoldenHarness(t)
	t.Setenv("FLEETLY_CREDENTIALS", filepath.Join(t.TempDir(), "credentials"))

	code, _, stderr := runCLI(t, "init", "--token", h.Token, "--keep-bootstrap", "carol")
	if code != 0 || stderr != "" {
		t.Fatalf("init --keep-bootstrap: code=%d stderr=%q", code, stderr)
	}
	users := identityv1.NewUsersServiceClient(h.Conn)
	_, err := users.WhoAmI(sdk.WithToken(context.Background(), h.Token), &identityv1.WhoAmIRequest{})
	require.NoError(t, err, "--keep-bootstrap must leave the bootstrap token alive")
}

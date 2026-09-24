package api

// SecretsService 的 API 面单测（E4 managed-databases §2.7，D-DB-7，W4-S4
// 验收 6）：set（覆盖即轮换）→ list（名称/指纹投影——值零出现）→ remove；
// 审计动作词表 secret.set/secret.removed 且值零进审计；名称形状校验与
// 缺失 404；读面 scope read 放行、写面 admin 拒绝（scope 断言在
// databases_rotate_test.go 的横向用例里）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// newSecretsTestEnv 起带 SecretsService 的 bufconn 测试环境。
func newSecretsTestEnv(t *testing.T) (*state.Store, *secrets.Box, serverv1.SecretsServiceClient, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	auth := NewAuthenticator(st)
	srv := newAuthServer(auth)
	serverv1.RegisterSecretsServiceServer(srv, NewSecretsService(st, box))
	conn := serveBufconn(t, srv)
	token := seedTokenPlain(t, st, "admin")
	return st, box, serverv1.NewSecretsServiceClient(conn), token
}

func TestSecretsSetListRemove(t *testing.T) {
	st, box, cl, token := newSecretsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	if _, err := testsupport.SeedAppE(t, st, "webapp"); err != nil {
		t.Fatalf("create app: %v", err)
	}

	const secretValue = "super-secret-9X"
	set, err := cl.SetSecret(ctx, &serverv1.SetSecretRequest{App: "webapp", Name: "dbpass", Value: secretValue})
	if err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if set.GetHash8() != naming.Hash8(secretValue) {
		t.Fatalf("hash8 = %s, want %s (content fingerprint)", set.GetHash8(), naming.Hash8(secretValue))
	}
	if strings.Contains(set.String(), secretValue) {
		t.Fatal("set response carries the secret value (write-only contract)")
	}

	// 密文落库（可解密回原值）+ 覆盖即轮换（hash8 重盖）。
	row, err := st.GetAppSecret(ctx, mustAppIDByName(t, st, "webapp"), "dbpass")
	if err != nil {
		t.Fatalf("get secret row: %v", err)
	}
	plain, err := box.Decrypt([]byte(row.ValueCipher))
	if err != nil || string(plain) != secretValue {
		t.Fatalf("stored cipher round-trip failed: %v", err)
	}
	set2, err := cl.SetSecret(ctx, &serverv1.SetSecretRequest{App: "webapp", Name: "dbpass", Value: "rotated-77Z"})
	if err != nil {
		t.Fatalf("rotate SetSecret: %v", err)
	}
	if set2.GetHash8() == set.GetHash8() {
		t.Fatal("rotation must change the fingerprint (rename-the-reference contract)")
	}

	// list：只投影名称/指纹/时间锚——值与密文零出现。
	list, err := cl.ListSecrets(ctx, &serverv1.ListSecretsRequest{App: "webapp"})
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	if len(list.GetSecrets()) != 1 || list.GetSecrets()[0].GetName() != "dbpass" {
		t.Fatalf("list = %+v, want one dbpass entry", list)
	}
	if strings.Contains(list.String(), secretValue) || strings.Contains(list.String(), "rotated-77Z") {
		t.Fatal("list response carries secret values")
	}

	// remove：成功 + 二次删除 404（幂等不做——与 RemoveEnv 同口径）。
	if _, err := cl.RemoveSecret(ctx, &serverv1.RemoveSecretRequest{App: "webapp", Name: "dbpass"}); err != nil {
		t.Fatalf("RemoveSecret: %v", err)
	}
	if _, err := cl.RemoveSecret(ctx, &serverv1.RemoveSecretRequest{App: "webapp", Name: "dbpass"}); status.Code(err) != codes.NotFound {
		t.Fatalf("second remove code = %v, want NotFound", status.Code(err))
	}

	// 审计词表：secret.set / secret.removed，值零出现。
	audits, err := st.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	setAudit, rmAudit := false, false
	for _, a := range audits {
		switch a.Action {
		case "secret.set":
			setAudit = true
			if strings.Contains(a.DiffSummary, secretValue) || strings.Contains(a.DiffSummary, "rotated-77Z") {
				t.Fatal("secret.set audit carries values")
			}
		case "secret.removed":
			rmAudit = true
		}
	}
	if !setAudit || !rmAudit {
		t.Fatalf("audit actions missing (set=%v removed=%v)", setAudit, rmAudit)
	}
}

// TestSecretsNameValidation 名称形状（/run/secrets/<name> 文件名安全——
// 与 compose 层同规则的 handler 镜像）。
func TestSecretsNameValidation(t *testing.T) {
	st, _, cl, token := newSecretsTestEnv(t)
	ctx := authCtx(context.Background(), token)
	if _, err := testsupport.SeedAppE(t, st, "shapeapp"); err != nil {
		t.Fatalf("create app: %v", err)
	}
	for _, name := range []string{"", "a/b", "..", "-lead", ".dot", strings.Repeat("x", 64)} {
		if _, err := cl.SetSecret(ctx, &serverv1.SetSecretRequest{App: "shapeapp", Name: name, Value: "v"}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("name %q accepted (code %v), want InvalidArgument", name, status.Code(err))
		}
	}
	// 合法形态：字母数字开头 + ._- 组合。
	ok, err := cl.SetSecret(ctx, &serverv1.SetSecretRequest{App: "shapeapp", Name: "a1-B.c", Value: "v"})
	if err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if ok.GetName() != "a1-B.c" {
		t.Fatalf("set name = %q", ok.GetName())
	}
}

func mustAppIDByName(t *testing.T, st *state.Store, name string) string {
	t.Helper()
	app, err := st.GetAppByName(context.Background(), name)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app.ID
}

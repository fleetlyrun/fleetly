package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GitKeysService 用户化语义矩阵（v0.3 W2-S2，rbac-teams 设计 §2.3 验收）：
//   - AddGitKey = 登录用户自服务（机具令牌 403——公钥归属用户，push 审计
//     actor 随署名用户）；
//   - ListGitKeys = 自己的；平台管理员/机具令牌 = 全部（存量无主键
//     user_id NULL 只读展示归全列消费方）；
//   - RemoveGitKey = 自己的或平台管理员；机具令牌删除 = admin scope；
//     可见集外一律 404。
//   - SSH 认证回调维度：指纹查行带出 user_id（push 审计署名的数据源）。

// newPubKeyLine 生成 ed25519 公钥的 authorized_keys 单行 + 指纹。
func newPubKeyLine(t *testing.T) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("to ssh key: %v", err)
	}
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub)))
	return line, gossh.FingerprintSHA256(sshPub)
}

// TestAddGitKeyUserSelfService 用户自服务加 key（行归属 user_id=自己）；
// 机具令牌恒 403；指纹查行带出属主（push 审计署名数据源）。
func TestAddGitKeyUserSelfService(t *testing.T) {
	env := newTokEnv(t)
	ctx := context.Background()
	gitKeys := serverv1.NewGitKeysServiceClient(env.conn)
	founder := env.seedUser(t, "founder@example.com")
	founderTok := env.userToken(t, founder.User.ID, ScopeRead)

	line, fingerprint := newPubKeyLine(t)
	resp, err := gitKeys.AddGitKey(authCtx(ctx, founderTok), &serverv1.AddGitKeyRequest{
		PublicKey: line, Note: "laptop",
	})
	if err != nil {
		t.Fatalf("AddGitKey: %v", err)
	}
	// 行归属与指纹。
	key, err := env.st.GetGitKeyByFingerprint(ctx, fingerprint)
	if err != nil {
		t.Fatalf("GetGitKeyByFingerprint: %v", err)
	}
	if key.UserID != founder.User.ID {
		t.Fatalf("key owner = %q, want %s", key.UserID, founder.User.ID)
	}
	if resp.GetId() != key.ID {
		t.Fatalf("resp id = %q, want %q", resp.GetId(), key.ID)
	}

	// 机具令牌（admin scope 亦然）：403——公钥归属用户，平台级凭据无
	// 自服务对象。
	adminMach := env.machineToken(t, ScopeAdmin)
	line2, _ := newPubKeyLine(t)
	if _, err := gitKeys.AddGitKey(authCtx(ctx, adminMach), &serverv1.AddGitKeyRequest{
		PublicKey: line2,
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("machine AddGitKey code = %v, want PermissionDenied", status.Code(err))
	}
	// 未鉴权：401。
	if _, err := gitKeys.AddGitKey(ctx, &serverv1.AddGitKeyRequest{PublicKey: line2}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anon AddGitKey code = %v, want Unauthenticated", status.Code(err))
	}
}

// TestListGitKeysVisibility 列表可见性：用户 = 自己的；平台管理员/机具
// 令牌 = 全部（含存量无主键的只读展示）。
func TestListGitKeysVisibility(t *testing.T) {
	env := newTokEnv(t)
	ctx := context.Background()
	gitKeys := serverv1.NewGitKeysServiceClient(env.conn)
	founder := env.seedUser(t, "founder@example.com")
	mate := env.seedUser(t, "mate@example.com")
	founderTok := env.userToken(t, founder.User.ID, ScopeRead)
	mateTok := env.userToken(t, mate.User.ID, ScopeRead)
	machTok := env.machineToken(t, ScopeRead)

	// 存量无主键（00018 前形态——user_id NULL，不回填）。
	legacyLine, legacyFP := newPubKeyLine(t)
	if _, err := env.st.CreateGitKey(ctx, state.GitKeyWrite{
		Fingerprint: legacyFP, PublicKey: legacyLine, KeyType: "ssh-ed25519", Note: "legacy",
	}); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	// founder 与 mate 各一把自服务 key。
	for _, tc := range []struct{ tok string }{{founderTok}, {mateTok}} {
		line, _ := newPubKeyLine(t)
		if _, err := gitKeys.AddGitKey(authCtx(ctx, tc.tok), &serverv1.AddGitKeyRequest{PublicKey: line}); err != nil {
			t.Fatalf("AddGitKey: %v", err)
		}
	}

	// mate：只见自己的 1 把（存量无主键不可见）。
	lr, err := gitKeys.ListGitKeys(authCtx(ctx, mateTok), &serverv1.ListGitKeysRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.GetKeys()) != 1 || lr.GetKeys()[0].GetUserId() != mate.User.ID {
		t.Fatalf("mate list = %+v, want own single row", lr.GetKeys())
	}
	// 平台管理员：全部（3 把，含无主键）。
	lr, err = gitKeys.ListGitKeys(authCtx(ctx, founderTok), &serverv1.ListGitKeysRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ownerless := 0
	for _, k := range lr.GetKeys() {
		if k.GetUserId() == "" {
			ownerless++
		}
	}
	if len(lr.GetKeys()) != 3 || ownerless != 1 {
		t.Fatalf("platform admin list = %d rows (%d ownerless), want 3/1", len(lr.GetKeys()), ownerless)
	}
	// 机具令牌：全部（平台管理员等价放行）。
	lr, err = gitKeys.ListGitKeys(authCtx(ctx, machTok), &serverv1.ListGitKeysRequest{})
	if err != nil {
		t.Fatalf("machine list: %v", err)
	}
	if len(lr.GetKeys()) != 3 {
		t.Fatalf("machine list = %d rows, want 3", len(lr.GetKeys()))
	}
}

// TestRemoveGitKeyPermissions 删除权矩阵：自己的 OK；他人/无主 key 对普通
// 用户 404；平台管理员任意；机具令牌删除 = admin scope。
func TestRemoveGitKeyPermissions(t *testing.T) {
	env := newTokEnv(t)
	ctx := context.Background()
	gitKeys := serverv1.NewGitKeysServiceClient(env.conn)
	founder := env.seedUser(t, "founder@example.com")
	mate := env.seedUser(t, "mate@example.com")
	founderTok := env.userToken(t, founder.User.ID, ScopeRead)
	mateCallTok := env.userToken(t, mate.User.ID, ScopeRead)
	adminMach := env.machineToken(t, ScopeAdmin)
	readMach := env.machineToken(t, ScopeRead)

	addKey := func(tok, note string) string {
		t.Helper()
		line, _ := newPubKeyLine(t)
		resp, err := gitKeys.AddGitKey(authCtx(ctx, tok), &serverv1.AddGitKeyRequest{PublicKey: line, Note: note})
		if err != nil {
			t.Fatalf("AddGitKey(%s): %v", note, err)
		}
		return resp.GetId()
	}
	legacyLine, legacyFP := newPubKeyLine(t)
	if _, err := env.st.CreateGitKey(ctx, state.GitKeyWrite{
		Fingerprint: legacyFP, PublicKey: legacyLine, KeyType: "ssh-ed25519", Note: "legacy",
	}); err != nil {
		t.Fatalf("seed legacy key: %v", err)
	}
	legacyID, err := env.st.GetGitKeyByFingerprint(ctx, legacyFP)
	if err != nil {
		t.Fatal(err)
	}
	founderKey := addKey(founderTok, "founder key")
	mateKey := addKey(mateCallTok, "mate key")

	// mate 删 founder 的 → 404。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, mateCallTok), &serverv1.RemoveGitKeyRequest{Id: founderKey}); status.Code(err) != codes.NotFound {
		t.Fatalf("remove other user's key code = %v, want NotFound", status.Code(err))
	}
	// mate 删存量无主 key → 404（可见集外）。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, mateCallTok), &serverv1.RemoveGitKeyRequest{Id: legacyID.ID}); status.Code(err) != codes.NotFound {
		t.Fatalf("user removing ownerless key code = %v, want NotFound", status.Code(err))
	}
	// read 机具令牌删除 → 403。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, readMach), &serverv1.RemoveGitKeyRequest{Id: mateKey}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read machine remove code = %v, want PermissionDenied", status.Code(err))
	}
	// mate 删自己的 → OK。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, mateCallTok), &serverv1.RemoveGitKeyRequest{Id: mateKey}); err != nil {
		t.Fatalf("remove own: %v", err)
	}
	// 平台管理员删他人 key → OK。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, founderTok), &serverv1.RemoveGitKeyRequest{Id: legacyID.ID}); err != nil {
		t.Fatalf("platform admin removing ownerless key: %v", err)
	}
	// admin 机具令牌删用户 key → OK。
	if _, err := gitKeys.RemoveGitKey(authCtx(ctx, adminMach), &serverv1.RemoveGitKeyRequest{Id: founderKey}); err != nil {
		t.Fatalf("admin machine removing user key: %v", err)
	}
	// 删除真实生效（物理删行）。
	if _, err := env.st.GetGitKey(ctx, founderKey); err != state.ErrGitKeyNotFound {
		t.Fatalf("GetGitKey after remove = %v, want ErrGitKeyNotFound", err)
	}
}

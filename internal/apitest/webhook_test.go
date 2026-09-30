package apitest_test

// Webhook 接收链服务面 e2e（F0.13）：验签对/错、token 无效、ping、重投
// 去重、commit 去重、skip 标记、watchPaths 命中/不命中、分支过滤、触发后
// 状态机推进到 succeeded、审计行 source=webhook / actor=hook:<app 名>、
// hook.push_accepted 事件。HTTP 入口的字节搬运在 assembly 适配层测试 +
// e2e dind smoke 覆盖。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/capability"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func signPayload(secret string, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// pushBody 构造 GitHub push 事件最小 payload（一个 commit 改动 paths）。
func pushBody(ref, after, headMsg string, paths ...string) string {
	quoted := ""
	for i, p := range paths {
		if i > 0 {
			quoted += ","
		}
		quoted += fmt.Sprintf("%q", p)
	}
	return fmt.Sprintf(`{"ref":%q,"after":%q,"head_commit":{"id":%q,"message":%q},"commits":[{"modified":[%s]}]}`,
		ref, after, after, headMsg, quoted)
}

func TestWebhookReceiveChain(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	auditq := identityv1.NewAuditQueryServiceClient(h.Conn)
	events := telemetryv1.NewEventsServiceClient(h.Conn)

	p, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "shop"})
	require.NoError(t, err)
	a, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: p.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)
	appID := a.GetApp().GetId()

	// ReceiveWebhook 是 PUBLIC 档：匿名 ctx（签名即凭证）。
	recv := func(token, event, delivery, signature, payload string) (*deliveryv1.ReceiveWebhookResponse, error) {
		return hooks.ReceiveWebhook(context.Background(), &deliveryv1.ReceiveWebhookRequest{
			Token: token, Payload: []byte(payload), Event: event, Delivery: delivery, Signature: signature,
		})
	}

	set, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main", WatchPaths: []string{"web/"},
	})
	require.NoError(t, err)
	secret := set.GetSecret()
	require.NotEmpty(t, secret, "first configuration must mint a token")

	// 再配置不换 Token（SetGitHook 是全量 upsert：字段以本次为准）；rotate
	// 双换且旧串即刻失效。
	again, err := hooks.SetGitHook(owner, &deliveryv1.SetGitHookRequest{
		AppId: appID, Repo: "https://github.com/acme/shop.git", Branch: "main", WatchPaths: []string{"web/"},
	})
	require.NoError(t, err)
	assert.Empty(t, again.GetSecret(), "re-configuring must not re-mint")
	rotated, err := hooks.RotateHookToken(owner, &deliveryv1.RotateHookTokenRequest{AppId: appID})
	require.NoError(t, err)
	require.NotEqual(t, secret, rotated.GetSecret())
	oldSecret := secret
	secret = rotated.GetSecret()
	_, err = recv(oldSecret, "ping", "d-old", signPayload(oldSecret, "{}"), "{}")
	require.Error(t, err, "the rotated-away token must be dead immediately")
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// ping → pong（验签通过后的连通性握手）。
	pong, err := recv(secret, "ping", "d-ping", signPayload(secret, "{}"), "{}")
	require.NoError(t, err)
	assert.Equal(t, "pong", pong.GetStatus())

	// 错误签名 → E_INVALID_SIGNATURE；无效 token → E_UNAUTHENTICATED。
	body := pushBody("refs/heads/main", "1111111111111111111111111111111111111111", "feat: one", "web/index.ts")
	_, err = recv(secret, "push", "d-bad", signPayload("flthook_wrong", body), body)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
	assert.Contains(t, err.Error(), "E_INVALID_SIGNATURE")
	_, err = recv("flthook_unknown", "push", "d-bad", signPayload("flthook_unknown", body), body)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// 过滤面：tag 不触发 / 分支不匹配 / skip 标记 / watch path 不命中。
	for _, tc := range []struct {
		delivery, ref, after, msg string
		paths                     []string
		wantReason                string
	}{
		{"d-tag", "refs/tags/v1.0", "2222222222222222222222222222222222222222", "release", []string{"web/a"}, "not a branch push"},
		{"d-branch", "refs/heads/dev", "3333333333333333333333333333333333333333", "feat", []string{"web/a"}, "does not match the configured filter"},
		{"d-skip", "refs/heads/main", "4444444444444444444444444444444444444444", "chore: [skip deploy]", []string{"web/a"}, "[skip deploy]"},
		{"d-path", "refs/heads/main", "5555555555555555555555555555555555555555", "docs", []string{"docs/readme.md"}, "no change under the watched paths"},
	} {
		body := pushBody(tc.ref, tc.after, tc.msg, tc.paths...)
		resp, err := recv(secret, "push", tc.delivery, signPayload(secret, body), body)
		require.NoError(t, err, tc.delivery)
		assert.Equal(t, "skipped", resp.GetStatus(), tc.delivery)
		assert.Contains(t, resp.GetReason(), tc.wantReason, tc.delivery)
	}

	// 命中：accepted → queued；commit 去重（同 commit 不同 delivery 返回
	// 既有 Deployment）；同 delivery 重投 → duplicate。
	commit1 := "1111111111111111111111111111111111111111"
	body1 := pushBody("refs/heads/main", commit1, "feat: one", "web/index.ts")
	accepted, err := recv(secret, "push", "d-1", signPayload(secret, body1), body1)
	require.NoError(t, err)
	assert.Equal(t, "accepted", accepted.GetStatus())
	depID := accepted.GetDeploymentId()
	require.NotEmpty(t, depID)

	dupCommit, err := recv(secret, "push", "d-1b", signPayload(secret, body1), body1)
	require.NoError(t, err)
	assert.Equal(t, "accepted", dupCommit.GetStatus())
	assert.Equal(t, depID, dupCommit.GetDeploymentId(), "same active commit must dedup to the same deployment")

	redelivered, err := recv(secret, "push", "d-1", signPayload(secret, body1), body1)
	require.NoError(t, err)
	assert.Equal(t, "duplicate", redelivered.GetStatus())

	// 非 ping/push 事件 → ignored（200 语义，不失败）。
	other, err := recv(secret, "issues", "d-9", signPayload(secret, "{}"), "{}")
	require.NoError(t, err)
	assert.Equal(t, "ignored", other.GetStatus())

	// 状态机推进：预置检出目录（跳过真实 clone）→ building → 假构建
	// digest → releasing → observing → succeeded。
	list, err := deployments.ListDeployments(owner, &deliveryv1.ListDeploymentsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, list.GetDeployments(), 1)
	dep := list.GetDeployments()[0]
	require.Equal(t, commit1, dep.GetCommitSha(), "CommitSHA must reach admission as the dedup anchor")
	require.NoError(t, os.MkdirAll(filepath.Join(h.DataRoot, "contexts", dep.GetToRevision()), 0o750))

	for i := 0; i < 40; i++ {
		h.Runtime.ReportRunning(appID+"-web", capability.Generation(dep.GetGeneration()))
		h.Drive(owner)
		h.Clock.Advance(120 * time.Second)
		h.Drive(owner)
		list, err = deployments.ListDeployments(owner, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		require.NoError(t, err)
		if list.GetDeployments()[0].GetState() == "succeeded" {
			break
		}
	}
	assert.Equal(t, "succeeded", list.GetDeployments()[0].GetState(),
		"webhook-triggered deployment must traverse the state machine, last error: %s", list.GetDeployments()[0].GetError())
	assert.NotEmpty(t, h.Builder.Calls(), "the fake builder must have been engaged")

	// 审计与事件：source=webhook、actor=hook:web；hook.push_accepted 落 outbox。
	entries, err := auditq.ListAudit(owner, &identityv1.ListAuditRequest{Source: "webhook", Limit: 100})
	require.NoError(t, err)
	actions := map[string]string{}
	for _, e := range entries.GetEntries() {
		actions[e.GetAction()] = e.GetActor()
	}
	require.Contains(t, actions, "deployment.create", "webhook deploy must leave an audit row")
	assert.Equal(t, "hook:web", actions["deployment.create"])
	assert.Equal(t, "hook:web", actions["hook.push"])

	found := false
	after := int64(0)
	for i := 0; i < 3 && !found; i++ {
		evs, err := events.ListEvents(owner, &telemetryv1.ListEventsRequest{AfterSeq: after, Limit: 200})
		require.NoError(t, err)
		for _, ev := range evs.GetEvents() {
			if ev.GetName() == "hook.push_accepted" && ev.GetAggregateId() == appID {
				found = true
			}
		}
		if evs.GetLastSeq() == after {
			break
		}
		after = evs.GetLastSeq()
	}
	assert.True(t, found, "hook.push_accepted event must land in the outbox")
}

package fleetlygrpc

// GitHub push webhook 接收链（F0.13）：验签（timing-safe HMAC-SHA256）→
// ping/push 分发 → 过滤（tag/分支/skip 标记/watchPaths）→ git 源 AppSpec
// 冻结 + admission（CommitSHA 去重锚）。重投的幂等语义经通用 Idempotency-
// Key（gateway 按 delivery 派生，ADR-0024/Q-21）。审计行 source=webhook、
// actor=hook:<App 名>（WithAuditOverride）。
//
// Token 材料永不落日志：本文件的全部错误信息只携带 delivery 标识（gateway
// 原生 handler 同一口径——见 assembly/gateway_hooks.go）。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
)

// skipDeployMarker 是头提交消息里的跳过标记（大小写不敏感）。
const skipDeployMarker = "[skip deploy]"

// pushPayload 是 GitHub push 事件的最小解析面（其余字段忽略——契约只取
// 部署判定所需）。
type pushPayload struct {
	Ref     string        `json:"ref"`
	After   string        `json:"after"`
	Head    *headCommit   `json:"head_commit"`
	Commits []commitEntry `json:"commits"`
}

type headCommit struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type commitEntry struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// ReceiveWebhook 是接收面主体（PUBLIC：HMAC 签名即凭证）。
func (svc *HooksService) ReceiveWebhook(ctx context.Context, req *deliveryv1.ReceiveWebhookRequest) (*deliveryv1.ReceiveWebhookResponse, error) {
	token := req.GetToken()
	if identity.TokenKind(token) != "hook" {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token")
	}
	h, err := svc.s.Hooks.GetByTokenSHA256(ctx, svc.s.DB.Runner(), identity.HashToken(token))
	if err != nil {
		// 不区分"行不存在"与"App 已删"——对发送方只呈现"凭证无效"。
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token")
	}
	// App 已删（Get 活跃行口径）与行不存在同形——对发送方只呈现"凭证无效"。
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), h.AppID)
	if err != nil {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token")
	}

	secret, err := svc.s.Cipher.Open(h.SecretCiphertext)
	if err != nil {
		// 信封解不开是平台侧密钥事故：脱敏报错（不回显密文）。
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the hook secret cannot be unsealed; check the master key under the data root keys/ directory")
	}
	if !verifyHMAC(secret, req.GetPayload(), req.GetSignature()) {
		return nil, apperr.New("E_INVALID_SIGNATURE",
			"webhook signature verification failed (X-Hub-Signature-256)").
			WithContext("delivery", req.GetDelivery()).
			WithSuggestion("Ensure the GitHub webhook secret matches the hook secret shown once by 'fleetly hooks set/rotate' (rotate the hook and update GitHub if the secret is lost).")
	}

	// 重投语义（Q-21 收口，ADR-0024）：gateway 派生幂等键 webhook:<delivery>，
	// 同 delivery 重投由拦截器重放首次响应（不达此处）；无键直达的重投由
	// admission 的 CommitSHA 去重。delivery 台账随受理事实一拍落库（见
	// handlePush 末事务），不再先于副作用独立提交——两步形态的崩溃窗口会
	// 把重投误判 duplicate 而丢部署。
	switch req.GetEvent() {
	case "ping":
		return &deliveryv1.ReceiveWebhookResponse{Status: "pong"}, nil
	case "push":
		return svc.handlePush(ctx, req, h, appRow)
	default:
		return &deliveryv1.ReceiveWebhookResponse{Status: "ignored", Reason: "event " + req.GetEvent() + " is not handled; only ping and push are"}, nil
	}
}

// handlePush 处理已验签的 push 事件：过滤判定 → 触发部署。
func (svc *HooksService) handlePush(ctx context.Context, req *deliveryv1.ReceiveWebhookRequest, h *hook.Hook, appRow *app.App) (*deliveryv1.ReceiveWebhookResponse, error) {
	var p pushPayload
	if err := json.Unmarshal(req.GetPayload(), &p); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "payload is not a valid push event body: %v", err).WithCause(err)
	}
	skip := func(reason string) (*deliveryv1.ReceiveWebhookResponse, error) {
		return &deliveryv1.ReceiveWebhookResponse{Status: "skipped", Reason: reason}, nil
	}

	branch, ok := branchFromRef(p.Ref)
	if !ok {
		return skip("ref " + p.Ref + " is not a branch push; tags never trigger deployments")
	}
	if h.Branch != "" && branch != h.Branch {
		return skip("branch " + branch + " does not match the configured filter " + h.Branch)
	}
	if strings.Trim(p.After, "0") == "" {
		return skip("branch " + branch + " was deleted")
	}
	if p.Head != nil && strings.Contains(strings.ToLower(p.Head.Message), skipDeployMarker) {
		return skip("head commit message carries " + skipDeployMarker)
	}
	if len(h.WatchPaths) > 0 && !watchPathsHit(changedPaths(p), h.WatchPaths) {
		return skip("no change under the watched paths")
	}

	// 触发部署：git 源 AppSpec（ref=push 后 commit）→ Revision 冻结 →
	// admission（CommitSHA 进去重锚；latest-wins 由 admission 收口）。
	appSpec, err := gitDeploySpec(appRow.ID, appRow.ProjectID, h, p.After)
	if err != nil {
		return nil, mapValidationError(err)
	}
	rev, err := freezeRevision(ctx, svc.s, appRow, appSpec)
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	// 审计标注：操作者是 hook 本身（source=webhook、actor=hook:<App 名>）——
	// revision 冻结与 engine.Submit 的审计行经 ctx 消费同一标注。
	ctx = authn.WithAuditOverride(ctx, "hook:"+appRow.Name, audit.SourceWebhook)
	d, err := svc.s.Engine.Submit(ctx, engine.SubmitRequest{
		AppID: appRow.ID, RevisionID: rev.ID, CommitSHA: p.After,
	})
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}

	// hook 受理事实（delivery 台账 + 审计行 + outbox 事件）一拍落库——台账
	// 与效果同事务，重投不产生第二行事实（幂等键重放根本不达此处；无键
	// 重投的部署侧去重由 admission CommitSHA 承担）。
	err = svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := svc.s.Hooks.RecordDelivery(ctx, tx, h.AppID, req.GetDelivery()); err != nil {
			return err
		}
		if err := svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Actor: "hook:" + appRow.Name, Source: audit.SourceWebhook,
			Action: "hook.push", Resource: "app/" + appRow.ID, AfterFP: p.After,
		}); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]string{"branch": branch, "commit": p.After, "deployment": d.ID})
		if err != nil {
			return err
		}
		_, err = svc.s.OutboxEvents.Append(ctx, tx, "hook.push_accepted", "app", appRow.ID, payload)
		return err
	})
	if err != nil {
		return nil, mapStateError(err, "hook push")
	}
	return &deliveryv1.ReceiveWebhookResponse{Status: "accepted", DeploymentId: d.ID}, nil
}

// verifyHMAC 校验 X-Hub-Signature-256 形态（"sha256=<hex>"；timing-safe）。
func verifyHMAC(secret, payload []byte, signature string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload) // hash.Write 恒成功（接口契约）
	return hmac.Equal(mac.Sum(nil), got)
}

// branchFromRef 解析 refs/heads/<branch>；tag/其他 ref 返回 false。
func branchFromRef(ref string) (string, bool) {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	branch := strings.TrimPrefix(ref, prefix)
	return branch, branch != ""
}

// changedPaths 汇总 push 内全部变更路径。
func changedPaths(p pushPayload) []string {
	var out []string
	for _, c := range p.Commits {
		out = append(out, c.Added...)
		out = append(out, c.Modified...)
		out = append(out, c.Removed...)
	}
	return out
}

// watchPathsHit 报告变更路径是否命中任一 watch 前缀（边界感知：watch
// "web" 命中 "web/index.ts" 不命中 "webapp/main.go"）。
func watchPathsHit(changed, watch []string) bool {
	for _, path := range changed {
		path = strings.Trim(path, "/")
		for _, w := range watch {
			w = strings.Trim(w, "/")
			if path == w || strings.HasPrefix(path, w+"/") {
				return true
			}
		}
	}
	return false
}

// gitDeploySpec 合成 git 源 AppSpec（单 web 进程引用构建产物；端口/探针
// 等运行时配置不进 webhook 面——需要时经 API 声明）。
func gitDeploySpec(appID, projectID string, h *hook.Hook, commitSHA string) (*specv1.AppSpec, error) {
	s := &specv1.AppSpec{
		SchemaVersion: spec.SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Git{Git: &specv1.GitSource{Repo: h.Repo, Ref: commitSHA}}},
		Build: &specv1.BuildSpec{
			Builder:  "dockerfile",
			Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: h.Dockerfile},
		},
		Processes: []*specv1.ProcessSpec{{
			Name:        "web",
			ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: "web"},
			Replicas:    1,
		}},
	}
	if err := spec.ValidateApp(s); err != nil {
		return nil, err
	}
	return s, nil
}

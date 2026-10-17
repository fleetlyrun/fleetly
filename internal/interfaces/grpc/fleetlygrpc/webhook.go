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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/hook"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
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

// ReceiveWebhook 是接收面主体（PUBLIC：HMAC 签名即凭证）。三处拒绝在
// envelope context 带 reject 站点码（token_kind/token_lookup/app_lookup）
// ——对外不泄细节（措辞恒同），只给排障一个可区分锚点。
func (svc *HooksService) ReceiveWebhook(ctx context.Context, req *deliveryv1.ReceiveWebhookRequest) (*deliveryv1.ReceiveWebhookResponse, error) {
	token := req.GetToken()
	if identity.TokenKind(token) != "hook" {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token").WithContext("reject", "token_kind")
	}
	h, err := svc.s.Hooks.GetByTokenSHA256(ctx, identity.HashToken(token))
	if err != nil {
		// 不区分"行不存在"与"App 已删"——对发送方只呈现"凭证无效"。
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token").WithContext("reject", "token_lookup")
	}
	// App 已删（Get 活跃行口径）与行不存在同形——对发送方只呈现"凭证无效"。
	appRow, err := svc.s.Apps.Get(ctx, h.AppID)
	if err != nil {
		return nil, apperr.New("E_UNAUTHENTICATED", "invalid hook token").WithContext("reject", "app_lookup:"+h.AppID)
	}
	// 审计 Team 轴（ADR-0035 决策 6）：PUBLIC 面无调用方身份——按 App 归属
	// Project 的 Team 标注（hook 触发的部署/冻结审计行归属资源 Team）。
	if proj, perr := svc.s.Projects.Get(ctx, appRow.ProjectID); perr == nil {
		ctx = audit.WithTeam(ctx, proj.TeamID)
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

	// 审计标注：操作者是 hook 本身（source=webhook、actor=hook:<App 名>）。
	// 必须先于 engine.SubmitSpec（冻结相）注入——冻结新 Revision 的
	// revision.create 审计行经同一 ctx 消费标注（此前标注在冻结之后，
	// webhook 冻结行的 source 错标为 api——ADR-0072 批修复的时序 bug）。
	ctx = authn.WithAuditOverride(ctx, "hook:"+appRow.Name, audit.SourceWebhook)

	// 摄取核（push/sync 共用链）：ctx 已带 hook:<App>/webhook 审计覆写。
	return svc.wrapIngest(svc.ingestGitMaterial(ctx, appRow, h, branch, p.After, req.GetDelivery(),
		"hook.push_accepted", "hook.push"))
}

// ingestGitMaterial 是 hook 材料摄取核（push 与 RefreshGitHook 双入口共用，
// 机制单源）：git 源 AppSpec（ref=commit）→ 受理单咽喉 engine.SubmitSpec
// （P3-7 下沉批：所有权合并——ADR-0072 决策 2 的 push 方向 = 新材料 + 当前
// 冻结 Spec 的运行时半边，console 声明的运行时配置跨触发存活；首触发无冻结
// 体 = 纯合成照旧。合并/三级变量合成/Revision 冻结（内容寻址复用）/
// admission（CommitSHA 进去重锚；latest-wins 由 admission 收口）全在 engine
// 一处）→ 台账/审计/outbox 事件一拍落库（重投不产生第二行事实；无键重投的
// 去重由 admission 承担）。ctx 的审计标注由触发面先行注入（push=
// hook:<App>/webhook 覆写；sync=调用方真实身份）——revision.create 与
// deployment.create 审计行经同一 ctx 消费标注（不变式④：覆写先于冻结）。
func (svc *HooksService) ingestGitMaterial(ctx context.Context, appRow *app.App, h *hook.Hook,
	branch, commit, delivery, eventName, auditAction string,
) (string, *deliveryv1.Admission, error) {
	appSpec, err := svc.gitBatchSpec(ctx, appRow, h, commit)
	if err != nil {
		return "", nil, err
	}
	// rev 响应面不回显（与旧链一致——deployment.to_revision 已承载锚定）。
	_, d, adm, err := svc.s.Engine.SubmitSpec(ctx, engine.SubmitSpecRequest{
		AppID: appRow.ID, Spec: appSpec,
		HookOwned: true, Merge: engine.MergeMaterial, // 新材料 + 冻结运行时（MergeHookOwned 参数序方向在 engine）
		Submit: &engine.SubmitOptions{CommitSHA: commit},
	})
	if err != nil {
		return "", nil, mapStateError(err, "revision")
	}
	payload, err := json.Marshal(hookPushAcceptedPayload{Branch: branch, Commit: commit, Deployment: d.ID})
	if err != nil {
		return "", nil, mapStateError(err, "hook")
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			_, err := st.Hooks.RecordDelivery(ctx, h.AppID, delivery)
			return err
		},
		facts: []func() writes.FactRow{
			// 事件 Team 铸入取 ctx 标注（P0-2）：本函数上文已按 App 归属
			// Project 的 Team 标注（资源轴，与审计同锚）。
			func() writes.FactRow {
				return writes.FactRow{
					Name: eventName, Aggregate: "app", ID: appRow.ID,
					Payload: payload, TeamID: audit.TeamFromContext(ctx),
					Action: auditAction, Resource: "app/" + appRow.ID, AfterFP: commit,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
		},
	})
	if err != nil {
		return "", nil, mapStateError(err, "hook")
	}
	return d.ID, admissionMsg(adm), nil
}

// RefreshGitHook 立即拉取 hook 材料并部署（push 的一等孪生：控制面拉取，不
// 依赖 GitHub 侧 webhook 配置——App Templates 第三方仓无法配 webhook 的
// 前提原语）。commit 缺省经 ls-remote 现解析（私有仓走同一材料解析面）；
// admission 与 push 同链（同 HEAD 重放回 deduplicated）。审计/事件带调用
// 方真实身份（source=api），与 webhook 面的 hook:<App> 覆写分野。
func (svc *HooksService) RefreshGitHook(ctx context.Context, req *deliveryv1.RefreshGitHookRequest) (*deliveryv1.RefreshGitHookResponse, error) {
	// app_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：空值由
	// protovalidate 拦截器在受理面拦截，文案与退役前逐字节一致；token/
	// signature/payload 校验是动态面，保留手写。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	h, err := svc.s.Hooks.Get(ctx, req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "hook")
	}
	branch := h.Branch
	if branch == "" {
		branch = "HEAD"
	}
	commit := strings.TrimSpace(req.GetCommit())
	if commit == "" {
		commit, err = svc.s.Engine.ResolveGitHead(ctx, appRow.ProjectID, h.Repo, branch)
		if err != nil {
			return nil, apperr.New("E_INTERNAL", "resolve %s @%s HEAD: %v", h.Repo, branch, err).WithCause(err)
		}
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, apperr.New("E_INTERNAL", "sync delivery id generation failed")
	}
	dID, adm, err := svc.ingestGitMaterial(ctx, appRow, h, branch, commit,
		"sync-"+hex.EncodeToString(nonce), "hook.refresh_accepted", "hook.refresh")
	if err != nil {
		return nil, err
	}
	return &deliveryv1.RefreshGitHookResponse{DeploymentId: dID, Commit: commit, Admission: adm}, nil
}

// wrapIngest 把摄取核产物包装成 webhook 应答（accepted + admission 附注；
// P10 三入口一致：同 commit 重复投递可见 deduplicated + 既有引用）。
func (svc *HooksService) wrapIngest(dID string, adm *deliveryv1.Admission, err error) (*deliveryv1.ReceiveWebhookResponse, error) {
	if err != nil {
		return nil, err
	}
	return &deliveryv1.ReceiveWebhookResponse{Status: "accepted", DeploymentId: dID, Admission: adm}, nil
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
// gitBatchSpec 构建摄取 spec：hook 文件字段按扩展名双语义——.yml/.yaml =
// compose 文件（git 批 compose 摄取：按 commit 克隆 → composeintake 同一
// 摄取面（镜像服务 → processes；顶层 name 剥除——平台管辖项目名，模板
// 仓常携带）；部署直拉镜像零构建）；其余 = Dockerfile 骨架（遗留形态，
// builder 以该路径构建）。
func (svc *HooksService) gitBatchSpec(ctx context.Context, appRow *app.App, h *hook.Hook, commit string) (*specv1.AppSpec, error) {
	base := strings.ToLower(h.Dockerfile)
	if !strings.HasSuffix(base, ".yml") && !strings.HasSuffix(base, ".yaml") {
		return gitDeploySpec(appRow.ID, appRow.ProjectID, h, commit)
	}
	dir, err := os.MkdirTemp("", "hookpull-")
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "hook pull staging failed: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := svc.s.Engine.CloneAt(ctx, appRow.ProjectID, h.Repo, commit, dir); err != nil {
		return nil, apperr.New("E_INTERNAL", "pull %s @%s: %v", h.Repo, shortSHA(commit), err).WithCause(err)
	}
	// 路径围栏：hook 文件路径必须落在检出目录内（../../../etc/passwd 形态
	// 的控制面文件探查在此拒绝——受理面 validateDockerfile 之外的纵深）。
	path := filepath.Join(dir, filepath.FromSlash(h.Dockerfile))
	if rel, rerr := filepath.Rel(dir, path); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, apperr.New("E_INVALID_ARGUMENT", "dockerfile: path escapes the checkout (%s)", h.Dockerfile)
	}
	//nolint:gosec // G304 围栏已执法；读取对象本就是检出内的仓库文件
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "compose file %s not found at %s @%s", h.Dockerfile, h.Repo, shortSHA(commit))
	}
	// 变量环境装载失败上抛（P0-7.1）：不吞错——DB 瞬断不得伪装成
	// "compose 变量不可消解"的受理失败。
	varLookup, err := deployVarLookup(ctx, svc.s, appRow)
	if err != nil {
		return nil, mapStateError(err, "variables")
	}
	out, err := composeintake.FromYAML(body, appRow.ID, appRow.ProjectID, varLookup)
	if err != nil {
		return nil, mapValidationError(err)
	}
	return out, nil
}

// shortSHA 是错误信息里的 commit 展示形态（材料与锚不进日志面）。
func shortSHA(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

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

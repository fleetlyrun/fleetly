package gitserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	sharedv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/shared/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/protobuf/encoding/protojson"
)

// webhook 入口（T2.19）：gateway 原生 HTTP handler（非 proto 派生——
// GitHub/Gitea 投递 JSON 不是 proto；端点例外清单在 cmd/fleetlyd/gateway.go
// 显式维护）。端点：POST /v1/apps/{app}/webhooks/{github|gitea}。
//
// 安全链（强制，无一例外）：
//  1. 验签 = HMAC-SHA256 对原始 body 字节（X-Hub-Signature-256，
//     GitHub/Gitea 同格式）；per-app secret 未配置 → 404（未配置即未启用）；
//     错签 → 401 退化信封（FZ-2，不加新码）。签名即认证——本路径豁免
//     Bearer 拦截，豁免精确到路径前缀 /v1/apps/{app}/webhooks/。
//  2. 防重放（时间窗口径，绑定）：GitHub/Gitea 签名无时间戳——落地为
//     delivery ID（X-GitHub-Delivery / X-Gitea-Delivery）TTL 缓存（默认
//     15 分钟，webhook.replay_ttl_seconds），TTL 内重复 ID 拒绝（409）；
//     携带时间戳头的自定义投递方（X-Fleetly-Timestamp，unix 秒）校验
//     ±5 分钟窗（TimestampWindow）。
//  3. 幂等去重（票面验收项）：按 (app, after-sha) 查 deployments.source
//     —— 该 sha 已有 enqueued/active/succeeded 部署 → 200 + duplicate
//     回执，不建新部署。failed/cancelled 不计入（重投允许重试）。
//  4. 分支过滤：ref ≠ refs/heads/<app 配置分支> → 200 ignored（只收不发）。

// WebhookPathPattern 是原生端点的精确路径形态（provider 白名单内联——
// 未知 provider 一律 404，豁免面不放宽）。gateway 侧根 handler 用同一线性
// 词形做分派（豁免面 = 分派面，cmd/fleetlyd/gateway.go 例外清单）。
var WebhookPathPattern = regexp.MustCompile(`^/v1/apps/([a-z0-9][a-z0-9-]{0,62})/webhooks/(github|gitea)$`)

// maxWebhookBody 是投递体上限（GitHub push 事件大型化防御；25MiB）。
const maxWebhookBody = 25 << 20

// WebhookHandler 是原生 webhook 端点处理器。
type WebhookHandler struct {
	src *Source
	log *slog.Logger
}

// NewWebhookHandler 构造（src 提供验签材料/防重放缓存/拉源/入队）。
func NewWebhookHandler(src *Source) *WebhookHandler {
	return &WebhookHandler{src: src, log: src.log}
}

// deliveryCache 是 delivery ID 的 TTL 缓存（LRU 语义即 TTL 窗口内的
// seen 集；窗口过期自然失效）。 Seen 记录并报告是否窗口内已出现。
type deliveryCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	seen map[string]time.Time
}

func newDeliveryCache(ttl time.Duration, now func() time.Time) *deliveryCache {
	return &deliveryCache{ttl: ttl, now: now, seen: make(map[string]time.Time)}
}

// Seen 记录 id 并报告 TTL 窗口内是否已出现（顺带清扫过期项——缓存体量
// 以 TTL 窗口内的投递量为上界）。
func (c *deliveryCache) Seen(id string) bool {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, at := range c.seen {
		if now.Sub(at) > c.ttl {
			delete(c.seen, k)
		}
	}
	if _, dup := c.seen[id]; dup {
		return true
	}
	c.seen[id] = now
	return false
}

// pushPayload 是 push 事件的最小投影（GitHub/Gitea 同形态）。
type pushPayload struct {
	Ref   string `json:"ref"`
	After string `json:"after"`
}

// webhookReceipt 是 200 回执（status ∈ enqueued|duplicate|ignored）。
type webhookReceipt struct {
	Status       string `json:"status"`
	DeploymentID string `json:"deployment_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// ServeHTTP 实现 http.Handler（路径精确匹配 WebhookPathPattern）。
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m := WebhookPathPattern.FindStringSubmatch(r.URL.Path)
	if m == nil {
		h.reject(w, http.StatusNotFound, "", "not found")
		return
	}
	app, provider := m[1], m[2]
	deliveryHeader := "X-Gitea-Delivery"
	if provider == "github" {
		deliveryHeader = "X-GitHub-Delivery"
	}
	deliveryID := r.Header.Get(deliveryHeader)

	appRow, err := h.src.st.GetAppByName(ctx, app)
	if err != nil {
		h.reject(w, http.StatusNotFound, deliveryID, "not found")
		return
	}
	cfg, err := h.src.st.GetAppGitConfig(ctx, appRow.ID)
	if err != nil || !cfg.SecretSet {
		// 未配置即未启用：secret 缺失的端点保持 404 语义（不暴露存在性）。
		h.reject(w, http.StatusNotFound, deliveryID, "not found")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		h.reject(w, http.StatusBadRequest, deliveryID, "unreadable or oversized body")
		return
	}

	// 1. 验签（强制）：secret 解密 → HMAC-SHA256 对原始 body 字节。
	secret, err := h.src.box.Decrypt([]byte(cfg.WebhookSecret))
	if err != nil {
		h.reject(w, http.StatusInternalServerError, deliveryID, "webhook secret undecryptable")
		return
	}
	if !verifySignature(r.Header.Get("X-Hub-Signature-256"), secret, body) {
		h.audit(ctx, appRow.ID, app, deliveryID, "rejected", "bad signature", "")
		h.reject(w, http.StatusUnauthorized, deliveryID, "invalid signature")
		return
	}

	// 2a. 防重放：delivery ID TTL 缓存（GitHub/Gitea 路径）。
	if deliveryID == "" {
		h.reject(w, http.StatusBadRequest, "", "missing delivery id header ("+deliveryHeader+")")
		return
	}
	if h.src.replay.Seen(deliveryID) {
		h.audit(ctx, appRow.ID, app, deliveryID, "rejected", "delivery replayed within TTL", "")
		h.reject(w, http.StatusConflict, deliveryID, "delivery already processed (replay window)")
		return
	}
	// 2b. 防重放：自定义投递方时间戳窗（±5 分钟；携带才校验）。
	if ts := r.Header.Get("X-Fleetly-Timestamp"); ts != "" {
		if !timestampInWindow(ts, h.src.replay.now()) {
			h.audit(ctx, appRow.ID, app, deliveryID, "rejected", "timestamp outside acceptance window", "")
			h.reject(w, http.StatusUnauthorized, deliveryID, "timestamp outside acceptance window")
			return
		}
	}

	// 3. 分支过滤 + branch 删除忽略（只收不发）。
	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.reject(w, http.StatusBadRequest, deliveryID, "malformed push payload")
		return
	}
	branch := cfg.Branch
	if branch == "" {
		branch = state.DefaultGitBranch
	}
	if payload.Ref != "refs/heads/"+branch {
		h.audit(ctx, appRow.ID, app, deliveryID, "ignored_branch", payload.Ref, "")
		h.reply(w, http.StatusOK, webhookReceipt{Status: "ignored", Reason: "branch not tracked"})
		return
	}
	if isZeroSHA(payload.After) {
		// after = 全零 → 分支删除形态（只收不发；无可部署对象）。
		h.audit(ctx, appRow.ID, app, deliveryID, "ignored", "branch deletion", "")
		h.reply(w, http.StatusOK, webhookReceipt{Status: "ignored", Reason: "branch deletion"})
		return
	}
	if !ValidSHA(payload.After) {
		h.audit(ctx, appRow.ID, app, deliveryID, "ignored", "missing or malformed after", "")
		h.reply(w, http.StatusOK, webhookReceipt{Status: "ignored", Reason: "missing after"})
		return
	}

	// 4. 幂等去重：该 sha 已有 enqueued/active/succeeded 部署 → duplicate。
	n, err := h.src.st.CountGitDeploymentsForSHA(ctx, appRow.ID, payload.After)
	if err == nil && n > 0 {
		h.audit(ctx, appRow.ID, app, deliveryID, "duplicate", payload.After, "")
		h.reply(w, http.StatusOK, webhookReceipt{Status: "duplicate"})
		return
	}

	// 5. 拉源（fetch 失败 → E_RUNTIME_UNAVAILABLE 信封，管线错误族）。
	if err := h.src.FetchRemote(ctx, app); err != nil {
		if !errors.Is(err, ErrFetchFailed) {
			err = fmt.Errorf("%w: %v", ErrFetchFailed, err)
		}
		h.auditErr(ctx, appRow.ID, app, deliveryID, "fetch failed: "+err.Error())
		h.envelope(w, apperr.New("E_RUNTIME_UNAVAILABLE",
			"webhook 拉源失败（app %s，sha %s）：%s", app, payload.After, err.Error()))
		return
	}

	// 6. 入队（与 git push 同一 DeployFromCommit 路径；校验期警告为非阻断
	// 标注，已随部署行落库——回执只承载部署 id 与状态）。
	rec, _, err := h.src.DeployFromCommit(ctx, DeployInput{
		App:         app,
		SHA:         payload.After,
		Ref:         payload.Ref,
		AuditAction: "git.webhook_deploy",
	})
	if err != nil {
		h.auditErr(ctx, appRow.ID, app, deliveryID, "deploy rejected: "+err.Error())
		h.appErr(w, err)
		return
	}
	h.audit(ctx, appRow.ID, app, deliveryID, "accepted", payload.After, rec.ID)
	h.reply(w, http.StatusOK, webhookReceipt{Status: "enqueued", DeploymentID: rec.ID})
}

// verifySignature 校验 X-Hub-Signature-256（"sha256=<hex>"；常量时间比对
// ——hex 摘要比对）。
func verifySignature(header string, secret, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.ToLower(strings.TrimPrefix(header, prefix))
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(got), []byte(want))
}

// timestampInWindow 校验 X-Fleetly-Timestamp（unix 秒）是否在 ±5 分钟窗。
func timestampInWindow(value string, now time.Time) bool {
	var sec int64
	if _, err := fmt.Sscanf(value, "%d", &sec); err != nil {
		return false
	}
	delta := now.Sub(time.Unix(sec, 0))
	if delta < 0 {
		delta = -delta
	}
	return delta <= TimestampWindow
}

// isZeroSHA 报告 sha 是否为全零（git 的分支删除哨兵形态）。
func isZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// audit 写 webhook 处置审计（系统动作；actor=system，不入事件流——事件
// 词汇仍是既有 deployment.*）。outcome ∈ accepted|duplicate|ignored|
// ignored_branch；outcome=rejected 走 auditErr（action=app.webhook_rejected，
// result=error）。
func (h *WebhookHandler) audit(ctx context.Context, appID, app, deliveryID, outcome, detail, deploymentID string) {
	target := "app:" + appID
	if deploymentID != "" {
		target = "deployment:" + deploymentID
	}
	if outcome == "rejected" {
		h.auditErr(ctx, appID, app, deliveryID, detail)
		return
	}
	if err := h.src.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "app.webhook_accepted",
			Target:      target,
			Result:      "ok",
			DiffSummary: auditDiff(app, deliveryID, outcome, detail),
		})
	}); err != nil {
		h.log.Warn("gitserver: webhook audit write failed", "app", app, "error", err.Error())
	}
}

// auditErr 写 webhook 拒绝审计（result=error；action 独立词根
// app.webhook_rejected——接受/拒绝在审计可分）。
func (h *WebhookHandler) auditErr(ctx context.Context, appID, app, deliveryID, detail string) {
	if err := h.src.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      "app.webhook_rejected",
			Target:      "app:" + appID,
			Result:      "error",
			DiffSummary: auditDiff(app, deliveryID, "rejected", detail),
		})
	}); err != nil {
		h.log.Warn("gitserver: webhook audit write failed", "app", app, "error", err.Error())
	}
}

// auditDiff 构造审计 diff 摘要（无敏感字段；delivery id 与 sha 均非密）。
func auditDiff(app, deliveryID, outcome, detail string) string {
	return `{"app":"` + app + `","delivery":"` + deliveryID + `","outcome":"` + outcome + `","detail":"` + detail + `"}`
}

// reply 输出 JSON 回执。
func (h *WebhookHandler) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.log.Warn("gitserver: webhook reply encode failed", "error", err.Error())
	}
}

// reject 输出退化信封（FZ-2：code 留空——401/404/409 无专用码，与鉴权
// 退化信封同口径；message 保底）。delivery id 进日志不入信封（观测面）。
func (h *WebhookHandler) reject(w http.ResponseWriter, code int, deliveryID, message string) {
	h.log.Warn("gitserver: webhook rejected", "delivery", deliveryID, "status", code, "message", message)
	env := &sharedv1.ErrorResponse{Message: message}
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}.Marshal(env)
	if err != nil {
		http.Error(w, message, code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw) //nolint:gosec // G705：输出为 protojson 序列化的固定信封（无用户可控标记）
}

// envelope 输出注册码错误信封（码的注册表 HTTP 映射决定状态码）。
func (h *WebhookHandler) envelope(w http.ResponseWriter, e *apperr.Error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}.Marshal(e.Envelope())
	if err != nil {
		http.Error(w, e.Message(), e.HTTPStatus())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_, _ = w.Write(raw) //nolint:gosec // G705：同上（注册表错误信封）
}

// appErr 按 *apperr.Error（注册码信封）渲染，否则 500 退化信封。
func (h *WebhookHandler) appErr(w http.ResponseWriter, err error) {
	var e *apperr.Error
	if errors.As(err, &e) {
		h.envelope(w, e)
		return
	}
	h.reject(w, http.StatusInternalServerError, "", "internal server error")
}

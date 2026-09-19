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
//     ±5 分钟窗（TimestampWindow）。占坑时机（一轮整改① + 二轮 R1/R2）：
//     **认证链（验签 + 时间窗）全部通过才进坑**——错签/时间窗 401 路径
//     绝不触碰缓存（公网无凭据 DoS 面：占坑的防爆破收益为零，map 填充
//     却是无界内存）；进坑为原子「查 + 占」（Claim），同 ID 并发请求自
//     进坑点起 409（并发重放窗口不再放大到 fetch 时长）；处理以 5xx 收场
//     （拉源失败/内部错误）时 Unmark 撤坑——GitHub/Gitea 官方重投
//     （Redeliver/自动重试）复用同一 delivery ID，失败重投链保持通；其余
//     终局（2xx 回执/4xx 确定性拒绝）保持已占坑。崩溃残留条目由 TTL 兜底。
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
	src *GitTriggers
	log *slog.Logger
}

// NewWebhookHandler 构造（src 提供验签材料/防重放缓存/拉源/入队）。
func NewWebhookHandler(src *GitTriggers) *WebhookHandler {
	return &WebhookHandler{src: src, log: src.log}
}

// deliveryCache 是 delivery ID 的 TTL 缓存（TTL 窗口内的 seen 集；窗口
// 过期自然失效）。Claim 原子「查 + 占」、Unmark 撤坑、Seen 只读探测——
// 占坑只发生在认证链之后（R1/R2，见文件头注释 2）。
type deliveryCache struct {
	mu   sync.Mutex
	ttl  time.Duration
	now  func() time.Time
	seen map[string]time.Time
	// capacity 是 seen 集的硬上限（R1 纵深②）：原生 HTTP handler 不在
	// gRPC 限流链上，缓存体量必须自限——超限时占坑淘汰最旧条目而非无限
	// 增长（有界内存）。
	capacity int
}

// deliveryCacheCapacity 是 seen 集缺省容量（15 分钟 TTL × 正常投递速率的
// 量级上界；测试可改写 capacity 字段收窄）。
const deliveryCacheCapacity = 8192

func newDeliveryCache(ttl time.Duration, now func() time.Time) *deliveryCache {
	return &deliveryCache{
		ttl:      ttl,
		now:      now,
		seen:     make(map[string]time.Time),
		capacity: deliveryCacheCapacity,
	}
}

// expire 清除 TTL 过期项（缓存体量以 TTL 窗口内的投递量为上界）。调用方
// 须持锁。命名口径（UBIQUITOUS_LANGUAGE §flagged-1）：sweep 专指周期扫描
// 轮（ingress 收敛/续期），本处是惰性过期删除，称 expire。
func (c *deliveryCache) expire(now time.Time) {
	for k, at := range c.seen {
		if now.Sub(at) > c.ttl {
			delete(c.seen, k)
		}
	}
}

// evictOldestLocked 淘汰时间戳最旧的条目（容量上限的出队侧；调用方须持
// 锁。O(n) 扫描只在超限占坑时发生，频率以容量为界）。
func (c *deliveryCache) evictOldestLocked() {
	oldestKey := ""
	var oldestAt time.Time
	for k, at := range c.seen {
		if oldestKey == "" || at.Before(oldestAt) {
			oldestKey, oldestAt = k, at
		}
	}
	if oldestKey != "" {
		delete(c.seen, oldestKey)
	}
}

// Seen 报告 TTL 窗口内 id 是否已出现过（只读，不占坑）。
func (c *deliveryCache) Seen(id string) bool {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now)
	_, dup := c.seen[id]
	return dup
}

// Claim 原子「查 + 占」：TTL 窗口内已出现 → false（重放，调用方 409）；
// 否则占坑 → true。原子性闭合并发同 ID 竞态（R2：第二个起 409，不再放
// 大到首个请求的 fetch 时长）。超容量时先淘汰最旧条目（R1 有界内存）。
func (c *deliveryCache) Claim(id string) bool {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now)
	if _, dup := c.seen[id]; dup {
		return false
	}
	if len(c.seen) >= c.capacity {
		c.evictOldestLocked()
	}
	c.seen[id] = now
	return true
}

// Unmark 撤坑（5xx 失败路径专用——失败重投链保持通；未占坑为 no-op）。
func (c *deliveryCache) Unmark(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, id)
}

// maxDeliveryIDLen 是 delivery ID 的长度上界（R1 纵深①：GitHub/Gitea 的
// delivery ID 是 UUID/SHA 词形，128 字节已富余；超长即缓存填充放大面）。
const maxDeliveryIDLen = 128

// validDeliveryID 守卫 delivery ID 词形：长度 ≤ maxDeliveryIDLen 且不含
// 控制字符（含 CR/LF——审计/日志回显的注入面）。在读任何缓存之前调用。
func validDeliveryID(id string) bool {
	if len(id) > maxDeliveryIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	return true
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
	// R1 纵深①：delivery ID 长度/词形守卫——在读任何缓存（与审计日志
	// 回显）之前执行；超长 ID（缓存填充放大面）与控制字符（日志注入面）
	// 直接 400，bad id 不回显。
	if !validDeliveryID(deliveryID) {
		h.reject(w, http.StatusBadRequest, "", "invalid delivery id header ("+deliveryHeader+")")
		return
	}

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
		// R1：401 路径绝不占坑——拒绝一个错签重试与查缓存开销相当，防爆破
		// 收益为零；占坑只会把缓存变成公网无凭据可填的无界 map。
		h.reject(w, http.StatusUnauthorized, deliveryID, "invalid signature")
		return
	}

	// 2a. delivery ID 在位性（缺头无法做防重放键）。
	if deliveryID == "" {
		h.reject(w, http.StatusBadRequest, "", "missing delivery id header ("+deliveryHeader+")")
		return
	}
	// 2b. 防重放：自定义投递方时间戳窗（±5 分钟；携带才校验）。401 同样
	// 不占坑（R1）。
	if ts := r.Header.Get("X-Fleetly-Timestamp"); ts != "" {
		if !timestampInWindow(ts, h.src.replay.now()) {
			h.audit(ctx, appRow.ID, app, deliveryID, "rejected", "timestamp outside acceptance window", "")
			h.reject(w, http.StatusUnauthorized, deliveryID, "timestamp outside acceptance window")
			return
		}
	}
	// 2c. 防重放入坑（R2 Mark-on-entry）：认证链（验签 + 时间窗）全部通过
	// 即原子「查 + 占」——同 ID 并发请求自此处起 409（并发重放窗口不再
	// 放大到首个请求的 fetch 时长）；处理以 5xx 收场时 Unmark 撤坑（见
	// 拉源失败与部署错误分支），失败重投链保持通（一轮 #1 语义）。
	if !h.src.replay.Claim(deliveryID) {
		h.audit(ctx, appRow.ID, app, deliveryID, "rejected", "delivery replayed within TTL", "")
		h.reject(w, http.StatusConflict, deliveryID, "delivery already processed (replay window)")
		return
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
		// 已占坑保持（2xx 终局：ignored）。
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
		// R2：5xx 收场撤坑——失败重投链保持通（同 ID 官方重投可重试）。
		h.src.replay.Unmark(deliveryID)
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
		// R2：仅 5xx 形态撤坑（内部错误可重试）；4xx 校验拒绝（如 compose
		// 词形错）是确定性终局，保持已占坑。
		status := http.StatusInternalServerError
		var ae *apperr.Error
		if errors.As(err, &ae) {
			status = ae.HTTPStatus()
		}
		if status >= http.StatusInternalServerError {
			h.src.replay.Unmark(deliveryID)
		}
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

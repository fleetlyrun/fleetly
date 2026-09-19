package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lynx-go/commands"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
)

// errChanges 是 plan/diff「有变化」的哨兵：exitCodeFor 映射为退出码 2
// （架构 §2.4 三态退出码：0=无变化 / 2=有变化 / 1=错误）。这是成功语义
// 而非错误，renderCLIError 对其只出提示行。
var errChanges = errors.New("changes detected")

// renderCLIError 渲染动词错误（stderr 单点）。信封解码双源：本地 apperr
// （validate/plan/diff 的 compose 违约——CLI 进程内构造）与远端 gRPC
// status detail 信封（服务端 E_* 错误随 status 传输，apperr.FromError 还
// 原）。无信封的原始错误照旧；连接/超时/鉴权三类 gRPC 错误附可行动
// 提示（S17-D3 与 401 hint 同风格）：Unauthenticated（401 信封退化形态）
// 附 bootstrap token 指引，Unavailable 附地址/守护进程排查，DeadlineExceeded
// 附重试与 --timeout 指引。
func renderCLIError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errChanges) {
		return "changes detected (exit 2)"
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		if e, ok := apperr.FromError(err); ok {
			ae = e
		}
	}
	if ae != nil {
		return renderAppErr(ae)
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unauthenticated:
			return err.Error() + "\n  hint: token 缺失或无效——设 --token / FLEETLY_TOKEN" +
				"（bootstrap admin token 见 fleetlyd 首启日志；后续 token 由管理员 fleetly tokens create 签发）"
		case codes.Unavailable:
			return err.Error() + "\n  hint: fleetlyd 不可达——检查 --addr（默认 127.0.0.1:8421，env FLEETLY_ADDR）" +
				"与守护进程状态（systemctl status fleetlyd）"
		case codes.DeadlineExceeded:
			return err.Error() + "\n  hint: 请求超时——fleetlyd 响应慢或网络问题，重试或加 --timeout" +
				"（一元 RPC 缺省 30s deadline）"
		}
	}
	var usage *commands.UsageError
	if errors.As(err, &usage) {
		return err.Error()
	}
	return err.Error()
}

// renderAppErr 渲染信封四件套（code/message + 注册表默认 suggestion/docs
// + 结构化 context）。
func renderAppErr(ae *apperr.Error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", ae.Error())
	env := ae.Envelope()
	for _, k := range sortedKeys(env.GetContext()) {
		fmt.Fprintf(&b, "\n  %s: %s", k, env.GetContext()[k])
	}
	if env.GetSuggestion() != "" {
		fmt.Fprintf(&b, "\n  suggestion: %s", env.GetSuggestion())
	}
	if env.GetDocs() != "" {
		fmt.Fprintf(&b, "\n  docs: %s", env.GetDocs())
	}
	return b.String()
}

// sortStrings 是字典序原地排序（std sort 的本地别名）。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// sortedKeys 是字典序键排序（proto map 上下文的稳定渲染）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// validateResult 是 validate --json 的输出形态。
type validateResult struct {
	Valid    bool              `json:"valid"`
	Name     string            `json:"name"`
	SpecHash string            `json:"spec_hash"`
	Services int               `json:"services"`
	Volumes  int               `json:"volumes"`
	Warnings []compose.Warning `json:"warnings,omitempty"`
}

// writeValidateHuman 渲染 validate 的人读输出。
func writeValidateHuman(w *strings.Builder, r validateResult, warnings []compose.Warning) {
	fmt.Fprintf(w, "%s: valid (spec_hash %.12s, %d services, %d volumes)\n", r.Name, r.SpecHash, r.Services, r.Volumes)
	writeWarnings(w, warnings)
}

// writeWarnings 渲染非阻断警告（注册码或 Kind 标识）。
func writeWarnings(w *strings.Builder, warnings []compose.Warning) {
	for _, warn := range warnings {
		label := warn.Code
		if label == "" {
			label = warn.Kind
		}
		if warn.Service != "" {
			fmt.Fprintf(w, "warning [%s] %s: %s\n", label, warn.Service, warn.Message)
		} else {
			fmt.Fprintf(w, "warning [%s] %s\n", label, warn.Message)
		}
	}
}

// marshalIndentJSON 是 --json 输出的统一编码（两空格缩进 + 尾换行）。
func marshalIndentJSON(v any) ([]byte, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// ── proto 面的 --json 编码（与 gateway 同源：protojson + UseProtoNames）──
// 直接回传服务端 proto 响应的动词（apps get / drift / ingress / tokens /
// logs / events）用 protojson——字段名 = proto 声明（snake_case）、时间 =
// RFC3339，与 REST 面同一 JSON 契约（CLI-over-SDK 双面同源）；CLI 侧合成
// 的视图（revisions/deployments 列表等）保持 encoding/json 自有形态。

// writeProtoJSON 是 proto 响应的 --json 输出（两空格缩进 + 尾换行）。
func writeProtoJSON(w io.Writer, msg proto.Message) error {
	raw, err := protoJSONOptions().Marshal(msg)
	if err != nil {
		return err
	}
	// protojson 输出携带刻意的随机空白（anti-dependency 特性）——经
	// json.Indent 归一为确定缩进（键序保持 proto 字段声明序），golden 与
	// 下游消费者都不依赖随机空白。
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

// writeProtoJSONL 是 proto 帧的 JSONL 单行输出。
func writeProtoJSONL(w io.Writer, msg proto.Message) error {
	raw, err := protoJSONOptions().Marshal(msg)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

// protoJSONOptions 统一 protojson 选项（UseProtoNames = 字段名按 proto 声
// 明；EmitUnpopulated=false = 零值字段不渲染，与 gateway marshaler 一致；
// Indent 留空——缩进由 json.Indent 确定性补齐）。
func protoJSONOptions() protojson.MarshalOptions {
	return protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}
}

// writeJSON 是 --json 输出统一出口（marshalIndentJSON + 写入 stdout）。
func writeJSON(w io.Writer, v any) error {
	raw, err := marshalIndentJSON(v)
	if err != nil {
		return err
	}
	_, err = w.Write(raw)
	return err
}

// ── 远端投影的 CLI JSON 形态（时间 RFC3339、枚举原样；RFC3339 零值时间
// 不输出——proto Timestamp nil 语义）───────────────────────────────────────

// deploymentJSON 是部署行的机器形态（T2.18 起投影自 DeploymentView——
// spec_hash/desired_hash 等执行细节不在 API 读面，行形态随契约收窄）。
type deploymentJSON struct {
	ID              string `json:"id"`
	App             string `json:"app"`
	Kind            string `json:"kind"`
	Status          string `json:"status"`
	Phase           string `json:"phase,omitempty"`
	RevisionID      string `json:"revision_id,omitempty"`
	SubstrateHalted bool   `json:"substrate_halted,omitempty"`
	FirstHealthyAt  string `json:"first_healthy_at,omitempty"`
	Recovery        string `json:"recovery,omitempty"`
	Verdict         string `json:"verdict,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`
	DowntimeMS      int64  `json:"downtime_ms,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	// git 触发来源（T2.19）：仅 git push/webhook 入队的部署非空——
	// API/CLI 读面可见部署来源（与 DeploymentView 同语义）。
	SourceGitSHA string `json:"source_git_sha,omitempty"`
	SourceGitRef string `json:"source_git_ref,omitempty"`
}

// toDeploymentJSON 把部署投影转为机器形态。
func toDeploymentJSON(rec *serverv1.DeploymentView) deploymentJSON {
	out := deploymentJSON{
		ID:              rec.GetId(),
		App:             rec.GetApp(),
		Kind:            rec.GetKind(),
		Status:          rec.GetStatus(),
		Phase:           rec.GetPhase(),
		RevisionID:      rec.GetRevisionId(),
		SubstrateHalted: rec.GetSubstrateHalted(),
		Recovery:        rec.GetRecovery(),
		Verdict:         rec.GetVerdict(),
		ErrorCode:       rec.GetErrorCode(),
		DowntimeMS:      rec.GetDowntimeMs(),
		SourceGitSHA:    rec.GetSourceGitSha(),
		SourceGitRef:    rec.GetSourceGitRef(),
	}
	if t := rec.GetFirstHealthyAt(); t != nil {
		out.FirstHealthyAt = t.AsTime().Format(time.RFC3339)
	}
	if t := rec.GetCreatedAt(); t != nil {
		out.CreatedAt = t.AsTime().Format(time.RFC3339)
	}
	if t := rec.GetUpdatedAt(); t != nil {
		out.UpdatedAt = t.AsTime().Format(time.RFC3339)
	}
	return out
}

// deploymentResultJSON 是 deploy/rollback --json 输出形态。
type deploymentResultJSON struct {
	Deployment deploymentJSON    `json:"deployment"`
	AppState   string            `json:"app_state"`
	Warnings   []compose.Warning `json:"warnings,omitempty"`
}

// buildJSON 是构建行的机器形态。
type buildJSON struct {
	ID          string `json:"id"`
	App         string `json:"app,omitempty"`
	Service     string `json:"service"`
	Driver      string `json:"driver"`
	Status      string `json:"status"`
	ImageRef    string `json:"image_ref,omitempty"`
	ImageDigest string `json:"image_digest,omitempty"`
	LogPath     string `json:"log_path,omitempty"`
	PlanPath    string `json:"plan_path,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

// toBuildJSON 把构建投影转为机器形态。
func toBuildJSON(b *serverv1.BuildView) buildJSON {
	out := buildJSON{
		ID:          b.GetId(),
		App:         b.GetApp(),
		Service:     b.GetService(),
		Driver:      b.GetDriver(),
		Status:      b.GetStatus(),
		ImageRef:    b.GetImageRef(),
		ImageDigest: b.GetImageDigest(),
		LogPath:     b.GetLogPath(),
		PlanPath:    b.GetPlanPath(),
		ErrorCode:   b.GetErrorCode(),
	}
	if t := b.GetStartedAt(); t != nil {
		out.StartedAt = t.AsTime().Format(time.RFC3339)
	}
	if t := b.GetFinishedAt(); t != nil {
		out.FinishedAt = t.AsTime().Format(time.RFC3339)
	}
	return out
}

// fromComposeWarnings 把服务端警告投影还原为本地 compose.Warning（渲染
// 复用同一形态）。
func fromComposeWarnings(ws []*serverv1.ComposeWarning) []compose.Warning {
	out := make([]compose.Warning, 0, len(ws))
	for _, w := range ws {
		out = append(out, compose.Warning{
			Kind: w.GetKind(), Code: w.GetCode(), Service: w.GetService(), Message: w.GetMessage(),
		})
	}
	return out
}

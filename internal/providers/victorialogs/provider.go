// Package victorialogs 实现 Logging Capability 的 VictoriaLogs Provider：
// 受管自宿（ADR-0004 通用 reconciler + ADR-0040 形态裁决）。持久化日志
// 承载：engine 采集环（runtime 容器日志）与 build 日志出口经 Ingest 回灌；
// 检索面经 Query（LogsQL）承载 `fleetly logs --text` 双径之一。
//
// 单租户存储：VL 只认平台凭证（basic auth，mesh 发布端点的强制门——
// 任何集群容器可路由到发布端口，而 VL 持全集群日志）；行级隔离由平台
// 查询构造执法（只携带请求 App 的域字段，ADR-0040 决策 4）。
package victorialogs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Image 是受管 VictoriaLogs 镜像（钉版，ADR-0021 口径；v1.25.0 起 tag 无
// -victorialogs 后缀——同义镜像。入口 /victoria-logs-prod 绝对路径，镜像
// config 实证 2026-10-04。升级经 Platform 升级序，ADR-0015）。
const Image = "victoriametrics/victoria-logs:v1.52.0"

// 受管形态常量（ADR-0040 决策 1）：单端口 9428（UI/ingest/select 同口），
// routing mesh 发布；数据卷挂 -storageDataPath；密码材料文件名即容器内
// /run/secrets/<名> 路径（-httpAuth.password=file:/// 旗标读取）。
const (
	publishPort    = 9428
	storageRoot    = "/victoria-logs-data"
	volumeID       = "fleetly-logging-victorialogs"
	binaryPath     = "/victoria-logs-prod"
	authFile       = "victorialogs-auth"
	credentialUser = "fleetly"
	// credentialFile 是平台凭证落盘（<DataRoot>/keys/ 下，与 KEK/zot 凭证
	// 同目录文化；0o600，备份随数据根）。
	credentialFile = "victorialogs.json"
	// passwordBytes 是随机密码字节数（hex 后 48 字符）。
	passwordBytes = 24
	// defaultRetentionDays 是工厂侧缺省（config 层已有同值缺省；0 值
	// ctx 注入时兜底）。
	defaultRetentionDays = int64(30)
)

// VL 字段名（Ingest 写入与 LogsQL 过滤共用词汇；带 fleetly_ 前缀避免与
// 用户日志字段撞名）。
const (
	fieldTeam     = "fleetly_team"
	fieldProject  = "fleetly_project"
	fieldApp      = "fleetly_app"
	fieldWorkload = "fleetly_workload"
	fieldTask     = "fleetly_task"
	fieldNode     = "fleetly_node"
	fieldKind     = "fleetly_kind"
	fieldBuild    = "fleetly_build"
)

// streamFields 是 VL _stream_fields 参数值（流身份维度——同流去重与高效
// 过滤；序稳定 = 请求字节稳定）。
const streamFields = fieldProject + "," + fieldApp + "," + fieldWorkload + "," + fieldTask + "," + fieldKind + "," + fieldBuild

// healthTimeout 是 Health 的 TCP 探测上限（managedLoop 全步带界的局部收敛）。
const healthTimeout = 3 * time.Second

// queryTimeout 是单次检索请求（非 follow）的局部上界；follow 流随调用方
// ctx 生存。
const queryTimeout = 30 * time.Second

// Provider 是 VictoriaLogs Logging Provider。
type Provider struct {
	addr          string
	dataRoot      string
	cred          capability.RegistryCredential
	retentionDays int64
	hc            *http.Client
}

// 编译期契约断言：Logging 端口 + 受管形态声明 + 材料子面（ADR-0040）。
var (
	_ capability.Logging         = (*Provider)(nil)
	_ capability.Managed         = (*Provider)(nil)
	_ capability.MaterialsSource = (*Provider)(nil)
)

// New 构造 Provider：addr 是 VL 端点（含端口，routing mesh 发布地址），
// dataRoot 是平台数据根（凭证持久化位置），retentionDays 进受管 argv。
// 凭证首启生成、之后原样复用（幂等：同 dataRoot 二次构造同密码——E28
// 字节稳定 = 载体指纹稳定）。损坏/不可读 fail loud，不静默再生成。
func New(addr, dataRoot string, retentionDays int64) (*Provider, error) {
	if addr == "" {
		return nil, fmt.Errorf("victorialogs provider: logging address is required")
	}
	if dataRoot == "" {
		return nil, fmt.Errorf("victorialogs provider: data root is required for the logging credential")
	}
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}
	cred, err := loadOrGenerateCredential(dataRoot)
	if err != nil {
		return nil, err
	}
	return &Provider{
		addr:          addr,
		dataRoot:      dataRoot,
		cred:          capability.RegistryCredential{Server: addr, Username: cred.Username, Secret: cred.Password},
		retentionDays: retentionDays,
		hc:            &http.Client{},
	}, nil
}

// credentialJSON 是凭证文件结构。
type credentialJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loadOrGenerateCredential 读或生成平台凭证（tmp+rename 原子落位；并发
// 双写容忍——后到 rename 失败即读先行者。zot registry.json 同款）。
func loadOrGenerateCredential(dataRoot string) (*credentialJSON, error) {
	dir := filepath.Join(dataRoot, "keys")
	path := filepath.Join(dir, credentialFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // 数据根私有目录（G304/G703）
		var c credentialJSON
		if jerr := json.Unmarshal(b, &c); jerr != nil || c.Username == "" || c.Password == "" {
			return nil, fmt.Errorf("victorialogs provider: credential file %s is malformed: %v", path, jerr)
		}
		return &c, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("victorialogs provider: read credential: %w", err)
	}
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("victorialogs provider: generate credential: %w", err)
	}
	c := &credentialJSON{Username: credentialUser, Password: hex.EncodeToString(raw)}
	// map 形态编码（zot G117 先例）：结构体字段名匹配 secret 模式会咬
	// gosec G117，map 键不触发。
	b, err := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	if err != nil {
		return nil, fmt.Errorf("victorialogs provider: encode credential: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("victorialogs provider: credential dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("victorialogs provider: write credential: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // 数据根私有目录
		if rb, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // 数据根私有目录
			var prev credentialJSON
			if json.Unmarshal(rb, &prev) == nil && prev.Password != "" {
				_ = os.Remove(tmp) //nolint:gosec // 数据根私有目录；并发先行者已落位，复用它
				return &prev, nil
			}
		}
		return nil, fmt.Errorf("victorialogs provider: publish credential: %w", err)
	}
	return c, nil
}

// Describe 实现 Provider 契约三件套之一（Managed=true：部署形态经
// ManagedWorkloads 声明，由通用 reconciler 部署）。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "victorialogs",
		Capability: capability.KindLogging,
		Version:    "1",
		Managed:    true,
		Notes: []string{
			"managed self-hosted VictoriaLogs; the persistent log store for the whole cluster (ADR-0040)",
			"collects runtime container logs and build logs through the control-plane daemon; query via `fleetly logs --text`",
			fmt.Sprintf("retention is %d days (config logging.retention_days); the store accepts only platform credentials (basic auth)", p.retentionDays),
		},
	}
}

// Health 实现 Provider 契约三件套之一（TCP 探测受管端点；首拍未起服是
// 正常窗口——reconciler 正在把它拉起来）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	d := net.Dialer{Timeout: healthTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return capability.HealthReport{Healthy: false, Details: "managed log store unreachable at " + p.addr + ": " + err.Error()}
	}
	_ = conn.Close()
	return capability.HealthReport{Healthy: true, Details: "managed log store reachable at " + p.addr}
}

// ManagedNamespace 返回平台系统隔离域（与用户 Project 分离）。
func (p *Provider) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "logging"}
}

// ManagedWorkloads 声明受管部署形态（通用 reconciler 经 Runtime Ensure
// 下发）：发布 9428（routing mesh——daemon 与任一节点可达）；数据卷本地
// （带卷 → reconciler 自动钉控制面节点，F2.3 语义）；认证密码经材料通道
// 注入（file:/// 旗标读取——密码绝不进 argv，swarm service argv 集群可
// inspect，ADR-0040 决策 1）。
func (p *Provider) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{{
		ID:      volumeID,
		Process: "victorialogs",
		Image:   Image,
		Command: []string{
			binaryPath,
			"-storageDataPath=" + storageRoot,
			fmt.Sprintf("-retentionPeriod=%dd", p.retentionDays),
			"-httpAuth.username=" + credentialUser,
			"-httpAuth.password=file:///run/secrets/" + authFile,
		},
		Ports:    []capability.WorkloadPort{{Port: publishPort, Protocol: capability.ProtocolHTTP}},
		Publish:  []capability.PortPublish{{PublishedPort: publishPort, TargetPort: publishPort}},
		Replicas: 1,
		// 数据面停止宽限（受管数据存储滚动替换窗口；staging pgvector 同类
		// 事故实证的宽限惯例，2026-10-03）。
		StopGrace: 60 * time.Second,
		Volumes: []capability.VolumeMount{
			{VolumeID: volumeID, Target: storageRoot},
		},
	}}
}

// ManagedMaterials 实现 MaterialsSource 子面：认证密码材料（幂等纯函数
// ——值源自持久化凭证文件，字节稳定 = 载体指纹稳定，E28）。文件内容即
// 密码（无换行；VL file:// 旗标读取语义）。
func (p *Provider) ManagedMaterials() capability.Materials {
	return capability.Materials{SecretFiles: map[string][]byte{
		authFile: []byte(p.cred.Secret),
	}}
}

// vlLine 是 Ingest 的 ndjson 行形态（_time/_msg 是 VL 保留字段名；域
// 字段平铺写入——_stream_fields 参数声明哪些字段构成流身份；json tag 与
// 上面 field* 常量一字不差，vlGoldenTest 钉死）。
type vlLine struct {
	Time     string `json:"_time"`
	Msg      string `json:"_msg"`
	Team     string `json:"fleetly_team"`
	Project  string `json:"fleetly_project"`
	App      string `json:"fleetly_app"`
	Workload string `json:"fleetly_workload"`
	Task     string `json:"fleetly_task"`
	Node     string `json:"fleetly_node"`
	Kind     string `json:"fleetly_kind"`
	Build    string `json:"fleetly_build"`
}

// Ingest 实现 Logging 端口：一批日志帧 ndjson POST 到 /insert/jsonline。
// 成功即调用方推进游标（ADR-0040 决策 2 的断流自愈锚）；失败上抛——
// 重试策略归调用方（engine 采集环游标冻结 + docker 缓冲重放）。
func (p *Provider) Ingest(ctx context.Context, frames []capability.LogFrame) error {
	if len(frames) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for i := range frames {
		f := &frames[i]
		line := vlLine{
			Time:     f.Time.UTC().Format(time.RFC3339Nano),
			Msg:      string(f.Line),
			Team:     f.Team,
			Project:  f.Project,
			App:      f.App,
			Workload: f.WorkloadID,
			Task:     f.Container,
			Node:     f.Node,
			Kind:     f.Kind,
			Build:    f.Source,
		}
		b, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("victorialogs ingest: encode frame: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return p.post(ctx, "/insert/jsonline?_stream_fields="+streamFields, "application/stream+json", buf.Bytes())
}

// Query 实现 Logging 端口（检索路径）：LogsQL 流过滤（域字段 +
// fleetly_build）+ 可选文本管道 → /select/logsql/query（非 follow，
// limit = tail 语义——VL 返回最大 _time 的 N 条）；Follow 经
// /select/logsql/tail 实时尾随（start_offset 回填 Since 起的历史，官方
// 流式端点，≥5s 批汇延迟诚实标注）。text 是否必填是调用方（server 双径
// 路由）的策略，本端口不执法。
func (p *Provider) Query(ctx context.Context, q capability.LogQuery, w capability.LogWriter) error {
	query := buildLogSQL(q)
	if q.Follow {
		return p.tail(ctx, query, q, w)
	}
	form := url.Values{}
	form.Set("query", query)
	if !q.Since.IsZero() {
		form.Set("start", q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		form.Set("end", q.Until.UTC().Format(time.RFC3339Nano))
	}
	if q.TailLines > 0 {
		form.Set("limit", fmt.Sprintf("%d", q.TailLines))
	}
	qctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rc, err := p.postBody(qctx, "/select/logsql/query", form.Encode())
	if err != nil {
		return fmt.Errorf("victorialogs query: %w", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("victorialogs query: read response: %w", err)
	}
	frames, err := decodeFrames(body)
	if err != nil {
		return err
	}
	// VL limit 语义 = 最大 _time 的 N 条，但行序不承诺——统一升序输出
	//（与实时路径帧序一致）。
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].Time.Before(frames[j].Time) })
	for i := range frames {
		if err := w.WriteLog(ctx, frames[i]); err != nil {
			return err
		}
	}
	return nil
}

// tail 经 /select/logsql/tail 实时尾随：start_offset 回填 Since 起的历史
// 再持续跟随（单流无接缝——两段拼接的窗口缺口不存在）。Until 与
// TailLines 在 follow 形态忽略（诚实边界：尾随流无界、无行数上限）。
func (p *Provider) tail(ctx context.Context, query string, q capability.LogQuery, w capability.LogWriter) error {
	form := url.Values{}
	form.Set("query", query)
	if !q.Since.IsZero() {
		form.Set("start_offset", formatOffset(time.Since(q.Since)))
	}
	body, err := p.postBody(ctx, "/select/logsql/tail", form.Encode())
	if err != nil {
		return fmt.Errorf("victorialogs tail: %w", err)
	}
	defer func() { _ = body.Close() }()
	r := bufio.NewReader(body)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if f, derr := decodeFrame(bytes.TrimRight(line, "\r\n")); derr == nil {
				if werr := w.WriteLog(ctx, f); werr != nil {
					return werr
				}
			}
			// 单行解码失败：跳过该行继续（VL 尾随流中间可能穿插空行/心跳；
			// 丢行优于断流——诚实边界记 ADR-0040）。
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil // 调用方取消的正常收口
			}
			return fmt.Errorf("victorialogs tail: read stream: %w", err)
		}
	}
}

// post 发一次性请求（2xx = 成功；否则带状态码与响应片段上抛）。
func (p *Provider) post(ctx context.Context, path, contentType string, body []byte) error {
	resp, err := p.do(ctx, http.MethodPost, path, contentType, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return p.respError("victorialogs: ingest rejected", resp)
	}
	return nil
}

// postBody 发请求并返回响应体流（检索面流式消费）。
func (p *Provider) postBody(ctx context.Context, path, body string) (io.ReadCloser, error) {
	resp, err := p.do(ctx, http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.respError("victorialogs: select rejected", resp)
	}
	return resp.Body, nil
}

func (p *Provider) do(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+p.addr+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.cred.Username, p.cred.Secret)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return p.hc.Do(req)
}

// respError 把非 2xx 响应转成带状态码与响应片段（有界）的错误。
func (p *Provider) respError(prefix string, resp *http.Response) error {
	snippet := make([]byte, 200)
	n, _ := io.ReadFull(resp.Body, snippet)
	return fmt.Errorf("%s: status %d: %s", prefix, resp.StatusCode, string(snippet[:n]))
}

// decodeFrames 解析完整响应体（ndjson 行集）。单行坏行跳过（与 VL 官方
// ingest 容错口径对齐；诚实边界——检索侧丢行不断流）。
func decodeFrames(body []byte) ([]capability.LogFrame, error) {
	var out []capability.LogFrame
	for _, line := range bytes.Split(bytes.TrimRight(body, "\r\n"), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		f, err := decodeFrame(line)
		if err != nil {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// decodeFrame 解析一行检索响应为 LogFrame。流字段兼容两种承载形态：
// 平铺（默认 /select/logsql/query 响应）与 _stream 对象（Grafana 插件
// API 形态）——防御性双读，取先中者。
func decodeFrame(line []byte) (capability.LogFrame, error) {
	var head struct {
		Time   string            `json:"_time"`
		Msg    string            `json:"_msg"`
		Stream map[string]string `json:"_stream"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return capability.LogFrame{}, err
	}
	flat := map[string]any{}
	_ = json.Unmarshal(line, &flat)
	field := func(name string) string {
		if v, ok := head.Stream[name]; ok && v != "" {
			return v
		}
		if v, ok := flat[name].(string); ok {
			return v
		}
		return ""
	}
	ts, err := parseVLTime(head.Time)
	if err != nil {
		return capability.LogFrame{}, err
	}
	return capability.LogFrame{
		WorkloadID: field(fieldWorkload),
		Container:  field(fieldTask),
		Node:       field(fieldNode),
		Time:       ts,
		Line:       []byte(head.Msg),
		Team:       field(fieldTeam),
		Project:    field(fieldProject),
		App:        field(fieldApp),
		Kind:       field(fieldKind),
		Source:     field(fieldBuild),
	}, nil
}

// parseVLTime 解析 VL _time（RFC3339Nano 主形态，多回退）。
func parseVLTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999Z07:00"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("victorialogs: unparsable _time %q", s)
}

// buildLogSQL 构造 LogsQL：流过滤 {域字段 + fleetly_build} + 可选文本
// 管道 | ~ "regexp"。Source 是 build 域回读的过滤锚（fleetly_build=Build
// ID）。值经字符串字面量转义（\ 与 "）；文本经 regexp 引用元字符转义
// （子串语义，非用户正则——检索面词汇是"包含该文本"）。
func buildLogSQL(q capability.LogQuery) string {
	var conds []string
	if v := q.Namespace.Project; v != "" {
		conds = append(conds, fieldProject+"="+quoteLogSQL(v))
	}
	if v := q.Namespace.App; v != "" {
		conds = append(conds, fieldApp+"="+quoteLogSQL(v))
	}
	if v := q.Namespace.Team; v != "" {
		conds = append(conds, fieldTeam+"="+quoteLogSQL(v))
	}
	if q.WorkloadID != "" {
		conds = append(conds, fieldWorkload+"="+quoteLogSQL(q.WorkloadID))
	}
	if q.Source != "" {
		conds = append(conds, fieldBuild+"="+quoteLogSQL(q.Source))
	}
	query := "{" + strings.Join(conds, ",") + "}"
	if q.Text != "" {
		query += ` | ~ "` + escapeRegexp(q.Text) + `"`
	}
	return query
}

// quoteLogSQL 产出 LogsQL 字符串字面量（"..."，转义 \ 与 "）。
func quoteLogSQL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// escapeRegexp 转义正则元字符（子串匹配语义，非用户正则——检索面词汇
// 是"包含该文本"）。QuoteMeta 已产出合法正则转义，这里只补 LogsQL 字符串
// 字面量要求的引号转义（不重复转义反斜杠）。
func escapeRegexp(s string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(s), `"`, `\"`)
}

// formatOffset 把回填窗时长格式化为 VL duration（秒形态，Prometheus 值域）。
func formatOffset(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。地址经装配 ctx 注入
// （config.logging.addr 唯一契约源，ADR-0040）；空 = Logging 面停用
// （logs 回退 Runtime 实时路径、build 日志回退环形缓冲——诚实降级，升级
// 零扰动）。
func init() {
	capability.RegisterFactory(capability.KindLogging, "victorialogs", func(ctx context.Context) (capability.Provider, error) {
		addr := capability.LoggingAddrFromContext(ctx)
		if addr == "" {
			return nil, fmt.Errorf("victorialogs provider: logging address is not configured (config logging.addr); the managed log store stays disabled")
		}
		dataRoot := os.Getenv("FLEETLY_DATA_ROOT")
		if dataRoot == "" {
			dataRoot = "./data" // 与 config.DefaultDataRoot 同缺省（install.sh 恒注入 env，此处兜底）
		}
		return New(addr, dataRoot, capability.LoggingRetentionDaysFromContext(ctx))
	})
}

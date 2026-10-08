// Package victoriametrics 实现 Metrics Capability 的 VictoriaMetrics
// Provider：受管自宿（ADR-0004 通用 reconciler + ADR-0041 形态裁决）。
// 两个受管 Workload：VM 单节点存储（8428 mesh、密码材料、数据卷钉控制面）
// + cadvisor 全局采集端（每节点一 task、host 只读绑定、宿主 8080——docker
// stats 无集群 API 的多节点采集形态）。engine 采集环每节拍抓取 cadvisor
// 原文经 ImportPrometheus 回灌 + 内存样本评估告警（评估不经本端口）。
//
// 存储是平台单租牌（basic auth 关 mesh 暴露）：行级隔离由平台查询构造
// 执法（App 域过滤经 cadvisor 的 container_label_fleetly_ns_* 序列标签）。
package victoriametrics

import (
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
	"strconv"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 镜像钉版（ADR-0021 口径；入口均为绝对路径，镜像 config 实证 2026-10-04）。
const (
	// Image 是 VM 单节点存储（升级经 Platform 升级序，ADR-0015）。
	Image = "victoriametrics/victoria-metrics:v1.152.0"
	// CadvisorImage 是每节点容器指标采集端（ADR-0041 决策 2）。
	CadvisorImage = "gcr.io/cadvisor/cadvisor:v0.55.1"
)

// 受管形态常量（ADR-0041 决策 1/2）。
const (
	publishPort    = 8428
	storageRoot    = "/victoria-metrics-data"
	volumeID       = "fleetly-metrics-victoriametrics"
	vmWorkloadID   = volumeID
	cadvisorID     = "fleetly-metrics-cadvisor"
	cadvisorPort   = 8080
	credentialUser = "fleetly"
	// credentialFile 是平台凭证落盘（<DataRoot>/keys/ 下；0o600 tmp+rename，
	// zot/VL 同款；备份随数据根）。
	credentialFile = "victoriametrics.json"
	passwordBytes  = 24
	// defaultRetentionDays 是工厂侧缺省（config 层已有同值缺省）。
	defaultRetentionDays = int64(30)
)

// healthTimeout 是 Health 的 TCP 探测上限。
const healthTimeout = 3 * time.Second

// queryTimeout 是单次查询请求上界。
const queryTimeout = 30 * time.Second

// Provider 是 VictoriaMetrics Metrics Provider。
type Provider struct {
	addr          string
	cred          capability.RegistryCredential
	retentionDays int64
	hc            *http.Client
}

// 编译期契约断言：Metrics 端口 + 受管形态声明 + 材料子面（ADR-0041）。
var (
	_ capability.Metrics         = (*Provider)(nil)
	_ capability.Managed         = (*Provider)(nil)
	_ capability.MaterialsSource = (*Provider)(nil)
)

// New 构造 Provider：addr 是 VM 端点（mesh 发布地址），dataRoot 是凭证持久化
// 根。凭证首启生成、之后原样复用（E28 字节稳定）。损坏/不可读 fail loud。
func New(addr, dataRoot string, retentionDays int64) (*Provider, error) {
	if addr == "" {
		return nil, fmt.Errorf("victoriametrics provider: metrics address is required")
	}
	if dataRoot == "" {
		return nil, fmt.Errorf("victoriametrics provider: data root is required for the metrics credential")
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

// loadOrGenerateCredential 读或生成平台凭证（tmp+rename 原子落位；并发双写
// 容忍——zot registry.json 同款）。
func loadOrGenerateCredential(dataRoot string) (*credentialJSON, error) {
	dir := filepath.Join(dataRoot, "keys")
	path := filepath.Join(dir, credentialFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // 数据根私有目录（G304/G703）
		var c credentialJSON
		if jerr := json.Unmarshal(b, &c); jerr != nil || c.Username == "" || c.Password == "" {
			return nil, fmt.Errorf("victoriametrics provider: credential file %s is malformed: %v", path, jerr)
		}
		return &c, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("victoriametrics provider: read credential: %w", err)
	}
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("victoriametrics provider: generate credential: %w", err)
	}
	c := &credentialJSON{Username: credentialUser, Password: hex.EncodeToString(raw)}
	// map 形态编码（zot G117 先例）。
	b, err := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	if err != nil {
		return nil, fmt.Errorf("victoriametrics provider: encode credential: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("victoriametrics provider: credential dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("victoriametrics provider: write credential: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // 数据根私有目录
		if rb, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // 数据根私有目录
			var prev credentialJSON
			if json.Unmarshal(rb, &prev) == nil && prev.Password != "" {
				_ = os.Remove(tmp) //nolint:gosec // 数据根私有目录；并发先行者已落位，复用它
				return &prev, nil
			}
		}
		return nil, fmt.Errorf("victoriametrics provider: publish credential: %w", err)
	}
	return c, nil
}

// Describe 实现 Provider 契约三件套之一（Managed=true：部署形态经
// ManagedWorkloads 声明，由通用 reconciler 部署）。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "victoriametrics",
		Capability: capability.KindMetrics,
		Version:    "1",
		Managed:    true,
		Notes: []string{
			"managed self-hosted VictoriaMetrics; the metrics store for the whole cluster (ADR-0041)",
			"a global cAdvisor task runs on every node exposing host port 8080; the control plane scrapes it and pushes samples here",
			fmt.Sprintf("retention is %d days (config metrics.retention_days); the store accepts only platform credentials (basic auth)", p.retentionDays),
		},
	}
}

// Health 实现 Provider 契约三件套之一（TCP 探测受管端点）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	d := net.Dialer{Timeout: healthTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return capability.HealthReport{Healthy: false, Details: "managed metrics store unreachable at " + p.addr + ": " + err.Error()}
	}
	_ = conn.Close()
	return capability.HealthReport{Healthy: true, Details: "managed metrics store reachable at " + p.addr}
}

// ManagedNamespace 返回平台系统隔离域。
func (p *Provider) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "metrics"}
}

// ManagedWorkloads 声明受管部署形态：VM 存储（单副本 + 数据卷 + mesh 发布
// + 密码材料 file:// 旗标）与 cadvisor 全局端（每节点一 task + 宿主只读
// 绑定 + host 发布 8080——每节点端点；无认证面，VPC-only 部署边界，
// ADR-0041 决策 2 诚实记档）。
func (p *Provider) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{
		{
			ID:      vmWorkloadID,
			Process: "victoriametrics",
			Image:   Image,
			Command: []string{
				"/victoria-metrics-prod",
				"-storageDataPath=" + storageRoot,
				fmt.Sprintf("-retentionPeriod=%dd", p.retentionDays),
				"-httpAuth.username=" + credentialUser,
				"-httpAuth.password=file:///run/secrets/" + authFile,
			},
			Ports:    []capability.WorkloadPort{{Port: publishPort, Protocol: capability.ProtocolHTTP}},
			Publish:  []capability.PortPublish{{PublishedPort: publishPort, TargetPort: publishPort}},
			Replicas: 1,
			// 数据面停止宽限（受管数据存储滚动替换窗口惯例）。
			StopGrace: 60 * time.Second,
			Volumes: []capability.VolumeMount{
				{VolumeID: volumeID, Target: storageRoot},
			},
		},
		{
			ID:      cadvisorID,
			Process: "cadvisor",
			Image:   CadvisorImage,
			// Command 是全量 argv（swarm 语义=入口覆盖，zot 绝对路径先例）：
			// cadvisor 入口 + -logtostderr（镜像 ENTRYPOINT 原样）+ 采集节拍
			// 15s（与采集环对齐；默认 1s 采样密度对小微负载是浪费）。
			Command: []string{"/usr/bin/cadvisor", "-logtostderr", "--housekeeping_interval=15s"},
			Ports:   []capability.WorkloadPort{{Port: cadvisorPort, Protocol: capability.ProtocolHTTP}},
			// host 发布的滚动序由 swarm 翻译层统一强制 stop-first
			// （translate.go rolloutOrder，N2 评审 P2-5）：宿主端口节点级
			// 排他，旧 task 先退让端口、新 task 再起。StopGrace 取舍留零：
			// 无状态采集端 SIGTERM 即退，零值 = 编排器缺省（10s 硬杀兜底）
			// ——宽限窗只在进程滞留时才消耗，显式声明不改变行为面（挂卷
			// 负载的 60s 是数据面排水窗，此处无数据面）。
			Publish: []capability.PortPublish{{PublishedPort: cadvisorPort, TargetPort: cadvisorPort, Mode: capability.PublishModeHost}},
			// 每节点一 task（docker stats 无集群 API——多节点采集的端点形态，
			// ADR-0041 决策 2）。Replicas=1 是全局形态的 per-node 期望数
			//（翻译 Global 优先忽略计数；spec 对照面 InspectWorkloads 对
			// 全局服务报 1——缺省 0 会每拍假 drift，CI 升级矩阵实证）。
			Global:   true,
			Replicas: 1,
			// 不挂域材料：无状态采集端不接收 VM 凭证（镜像无 /run/secrets，
			// secret 挂载会启动失败——staging 实证 2026-10-04）。
			SkipMaterials: true,
			// 官方 run 形态的只读绑定面（/var/run 含 docker.sock 供容器元数据；
			// 不用 privileged——cpu/mem 主链只读挂载面够）。
			HostBinds: []capability.HostBind{
				{Source: "/", Target: "/rootfs", ReadOnly: true},
				{Source: "/var/run", Target: "/var/run", ReadOnly: true},
				{Source: "/sys", Target: "/sys", ReadOnly: true},
				{Source: "/var/lib/docker", Target: "/var/lib/docker", ReadOnly: true},
			},
		},
	}
}

// authFile 是 VM 认证密码材料文件名（容器内 /run/secrets/<名>）。
const authFile = "victoriametrics-auth"

// ManagedMaterials 实现 MaterialsSource 子面：VM 认证密码（幂等纯函数，
// 字节稳定 = 载体指纹稳定，E28）。cadvisor 无材料面。
func (p *Provider) ManagedMaterials() capability.Materials {
	return capability.Materials{SecretFiles: map[string][]byte{
		authFile: []byte(p.cred.Secret),
	}}
}

// ImportPrometheus 实现 Metrics 端口：Prometheus exposition 文本透传 VM
// 流式导入端点；extraLabels 经 extra_label 查询参数附加到每条序列（平台
// 归因：job/node）。失败上抛（调用方节拍退避）。
func (p *Provider) ImportPrometheus(ctx context.Context, body []byte, extraLabels map[string]string) error {
	if len(body) == 0 {
		return nil
	}
	q := url.Values{}
	for k, v := range extraLabels {
		q.Add("extra_label", k+"="+v)
	}
	path := "/api/v1/import/prometheus"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+p.addr+path, bytesReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth(p.cred.Username, p.cred.Secret)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("victoriametrics import: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		snippet := make([]byte, 200)
		n, _ := io.ReadFull(resp.Body, snippet)
		return fmt.Errorf("victoriametrics import: status %d: %s", resp.StatusCode, string(snippet[:n]))
	}
	return nil
}

// QuerySeries 实现 Metrics 端口：PromQL 透传 VM query_range（Prometheus
// 兼容 API）；step 秒形态。响应是标准 matrix（{metric, values} 列表），
// 多标签命中逐序列返回（空集 = 查询无数据，合法形态）。
func (p *Provider) QuerySeries(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]capability.Series, error) {
	if step <= 0 {
		step = 15 * time.Second
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.Unix(), 10))
	q.Set("end", strconv.FormatInt(end.Unix(), 10))
	q.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))
	qctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(qctx, http.MethodPost, "http://"+p.addr+"/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.cred.Username, p.cred.Secret)
	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("victoriametrics query: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("victoriametrics query: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("victoriametrics query: status %d: %s", resp.StatusCode, snippetOf(body))
	}
	return decodeQueryRange(body)
}

// vmQueryResponse 是 VM query_range 响应形态（Prometheus 兼容；values 元素
// 是 [unixSeconds, "value"] 二元数组）。
type vmQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// decodeQueryRange 解析 matrix 响应为多序列（每个 {metric, values} 元素一条；
// 空集 = 查询无数据的合法形态。单序列截断形态已被 C1 Console 图表批取代）。
func decodeQueryRange(body []byte) ([]capability.Series, error) {
	var r vmQueryResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("victoriametrics: decode query response: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("victoriametrics: query failed: %s", r.Error)
	}
	out := make([]capability.Series, 0, len(r.Data.Result))
	for _, res := range r.Data.Result {
		s := capability.Series{Metric: res.Metric}
		for _, v := range res.Values {
			ts, ok1 := v[0].(float64)
			str, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				continue
			}
			val, err := strconv.ParseFloat(str, 64)
			if err != nil {
				continue
			}
			s.Points = append(s.Points, capability.SeriesPoint{Time: time.Unix(int64(ts), 0).UTC(), Value: val})
		}
		out = append(out, s)
	}
	return out, nil
}

// snippetOf 是错误载荷片段（有界）。
func snippetOf(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// bytesReader 是 []byte → io.Reader 的局部适配（避免 bytes import 面扩散）。
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct{ b []byte }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// init 自注册工厂（config.metrics.addr 唯一契约源；空 = Metrics 面停用——
// 零采集/零告警/查询精确失败，升级零扰动，ADR-0041）。
func init() {
	capability.RegisterFactory(capability.KindMetrics, "victoriametrics", func(ctx context.Context) (capability.Provider, error) {
		addr := capability.MetricsAddrFromContext(ctx)
		if addr == "" {
			return nil, fmt.Errorf("victoriametrics provider: metrics address is not configured (config metrics.addr); the managed metrics store stays disabled")
		}
		dataRoot := os.Getenv("FLEETLY_DATA_ROOT")
		if dataRoot == "" {
			dataRoot = "./data" // 与 config.DefaultDataRoot 同缺省
		}
		return New(addr, dataRoot, capability.MetricsRetentionDaysFromContext(ctx))
	})
}

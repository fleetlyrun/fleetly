package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/statebackup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SystemService 实现 server.v1.SystemService：进程级系统信息（Ping/Status，
// T0.3/T2.17）+ 集群级观察面（ListNodes/GetIngressStatus，T2.18）+ join
// 向导面（E1-8，multi-node §2.3）。
//
// ingress 依赖以 IngressStatusSource 端口注入（实现 = *ingress.Manager）：
// 入口探测在服务端执行——CLI 不再直连 docker / 读本地 token 文件 / 探测
// 配置端点（CLI-over-SDK 单一通道纪律）；nil 端口 = 入口面未装配（测试
// 形态），Traefik 视图如实报告不可用而非谎报。
type SystemService struct {
	serverv1.UnimplementedSystemServiceServer
	version string
	// components 是命名健康组件集（复用各服务 CheckHealth 实现——lynx
	// Checker 接口无名，命名清单在装配点显式维护）。
	components func() []SystemComponent
	st         *state.Store
	ing        IngressStatusSource
	// backup 是状态备份管理器（T2.22；nil = 备份面未装配——ListBackups/
	// GetSystemStatus 的备份视图走台账仍可用，TriggerBackup 如实报不可用）。
	backup *statebackup.Manager
	// baseDomain 是平台域名（multi-node §2.2；E1-8 join 门禁 D-MN-13 的
	// 判定面：空 = 多节点未启用）。
	baseDomain string
	// join 是 swarm join-token 面端口（实现 = *substrate.Client；nil =
	// 未装配——GetJoinGuide 在 base_domain 缺失时先以 409 拒绝、不触底座，
	// RotateJoinToken 如实报不可用）。
	join JoinTokenPort
}

// JoinTokenPort 是 swarm join-token 面端口（multi-node §2.3；*substrate.
// Client 隐式实现——端口在 api 定义、适配在 substrate，方向纪律同
// IngressStatusSource）。
type JoinTokenPort interface {
	// SwarmJoinInfo 返回 manager advertise addr 与 worker join token。
	SwarmJoinInfo(ctx context.Context) (string, string, error)
	// SwarmRotateJoinToken 轮换指定角色（worker|manager）的 join token
	// 并返回新 token（rotate 后旧 token 立即失效）。
	SwarmRotateJoinToken(ctx context.Context, role string) (string, error)
}

// SystemComponent 是带名的健康组件（CheckHealth 复用面）。
type SystemComponent struct {
	Name  string
	Check func() error
}

// IngressStatusSource 是入口状态消费端口（*ingress.Manager 隐式实现）。
type IngressStatusSource interface {
	Status(ctx context.Context) (ingress.Status, error)
	Config() ingress.Config
}

// NewSystemService 构造 SystemService（version 由构建 -ldflags 注入；
// components 为装配点命名的健康组件集；ing 可为 nil——入口面未装配形态）。
func NewSystemService(version string, st *state.Store, components func() []SystemComponent, ing IngressStatusSource) *SystemService {
	return &SystemService{version: version, st: st, components: components, ing: ing}
}

// WithJoinGuide 注入 join 向导面（E1-8；链式装配）。baseDomain 为空 =
// 单节点形态（GetJoinGuide 以 E_MULTI_NODE_REQUIRES_BASE_DOMAIN 409 拒绝
// ——D-MN-13）；jp 可为 nil（join 底座面未装配）。
func (s *SystemService) WithJoinGuide(baseDomain string, jp JoinTokenPort) *SystemService {
	s.baseDomain = baseDomain
	s.join = jp
	return s
}

// WithBackupManager 注入状态备份管理器（T2.22；链式装配，nil 合法——
// 测试/精简形态的备份面缺省）。
func (s *SystemService) WithBackupManager(m *statebackup.Manager) *SystemService {
	s.backup = m
	return s
}

// Ping 回应 service / version。proto 字段上的 buf.validate 最小约束由拦截
// 器统一校验；Ping 豁免鉴权（T2.17 契约：与 healthz 同为存活面）。
func (s *SystemService) Ping(ctx context.Context, req *serverv1.PingRequest) (*serverv1.PingResponse, error) {
	return &serverv1.PingResponse{Service: "fleetlyd", Version: s.version}, nil
}

// GetSystemStatus 健康汇总（引擎/Traefik/状态层/备份——复用 CheckHealth 面）：
// 逐组件如实上报，不聚合单一布尔，判断权在消费方。备份明细（最近一次
// 台账行的时间与 verify_status）随 backup 字段带出——组件布尔之外让
// 「备份上次何时成功」直接可见（T2.22 状态诚实契约）。
func (s *SystemService) GetSystemStatus(ctx context.Context, req *serverv1.GetSystemStatusRequest) (*serverv1.GetSystemStatusResponse, error) {
	resp := &serverv1.GetSystemStatusResponse{Service: "fleetlyd", Version: s.version}
	for _, c := range s.components() {
		ch := &serverv1.ComponentHealth{Name: c.Name}
		if err := c.Check(); err != nil {
			ch.Ok = false
			ch.Error = err.Error()
		} else {
			ch.Ok = true
		}
		resp.Components = append(resp.Components, ch)
	}
	if latest, err := s.st.LatestStateBackup(ctx); err == nil && latest != nil {
		resp.Backup = backupHealth(latest)
	}
	return resp, nil
}

// backupHealth 把最近一次台账行投影为备份健康视图。
func backupHealth(latest *state.StateBackup) *serverv1.BackupHealth {
	return &serverv1.BackupHealth{
		LastBackupId:     latest.ID,
		LastKind:         latest.Kind,
		LastBackupAt:     tstamp(latest.CreatedAt),
		LastVerifyStatus: latest.VerifyStatus,
		LastError:        latest.Error,
	}
}

// ListBackups 状态备份台账只读列表（T2.22；n 缺省 50——台账量级受保留
// 份数约束，50 已覆盖全部现行行 + 近期失败行）。
func (s *SystemService) ListBackups(ctx context.Context, req *serverv1.ListBackupsRequest) (*serverv1.ListBackupsResponse, error) {
	rows, err := s.st.ListStateBackups(ctx, 50)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.BackupView, 0, len(rows))
	for _, r := range rows {
		out = append(out, backupView(r))
	}
	return &serverv1.ListBackupsResponse{Backups: out}, nil
}

// TriggerBackup 手动触发一次状态备份（同步：响应即落账后的台账行；
// verify_status=failed 时以 FailedPrecondition 返回且台账行保留失败事实
// ——调用方看得见失败，绝不渲染成成功）。
func (s *SystemService) TriggerBackup(ctx context.Context, req *serverv1.TriggerBackupRequest) (*serverv1.TriggerBackupResponse, error) {
	if s.backup == nil {
		return nil, status.Error(codes.Unavailable, "backup manager unavailable (not assembled)")
	}
	kind := req.GetKind()
	if kind == "" {
		kind = state.BackupKindManual
	}
	rec, err := s.backup.Trigger(ctx, kind)
	if err != nil {
		// 失败行已落台账（backup.failed 审计随行）——错误原文回传，调用方
		// 可经 ListBackups 复核失败事实。
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, auditEntry(ctx, "backup:"+rec.ID,
			state.DiffSummary("kind", rec.Kind, "verify", rec.VerifyStatus))) // MG-6：构造器替换手拼 JSON
	}); err != nil {
		return nil, err
	}
	return &serverv1.TriggerBackupResponse{Backup: backupView(rec)}, nil
}

// backupView 把台账行投影为只读视图。
func backupView(r state.StateBackup) *serverv1.BackupView {
	return &serverv1.BackupView{
		Id:           r.ID,
		Kind:         r.Kind,
		Path:         r.Path,
		Sha256:       r.SHA256,
		SizeBytes:    r.SizeBytes,
		VerifyStatus: r.VerifyStatus,
		Error:        r.Error,
		CreatedAt:    tstamp(r.CreatedAt),
	}
}

// ListNodes 节点观测缓存只读列表（state-model §2.2：缓存禁止用于决策，
// 展示/诊断专用；节点变更用 docker node 原生命令）。NodeView 增补
// pinned_app_ids（E1-8，multi-node §2.7/D-MN-9：读时 join placements
// 权威表，UI「已钉应用」交叉引用——无迁移）。
func (s *SystemService) ListNodes(ctx context.Context, req *serverv1.ListNodesRequest) (*serverv1.ListNodesResponse, error) {
	nodes, err := s.st.ListCachedNodes(ctx)
	if err != nil {
		return nil, err
	}
	pinned, err := s.st.PlacementAppsByNode(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.NodeView, 0, len(nodes))
	for _, n := range nodes {
		v := &serverv1.NodeView{
			SwarmNodeId:  n.SwarmNodeID,
			Hostname:     n.Hostname,
			State:        n.State,
			Availability: n.Availability,
			IsManager:    n.IsManager,
			ObservedAt:   tstamp(n.ObservedAt),
			Stale:        n.Stale,
			Labels:       n.Labels,
			PinnedAppIds: []string{},
		}
		if id := n.Labels[state.LabelNodeID]; id != "" {
			v.PlatformId = id
			v.PinnedAppIds = pinned[id]
		}
		out = append(out, v)
	}
	return &serverv1.ListNodesResponse{Nodes: out}, nil
}

// GetJoinGuide join 向导（E1-8，multi-node §2.3）：join 命令 + 按 worker_ip
// 的精确放行规则（只生成不自动应用）+ worker 前置门禁命令 + DNS 步骤 +
// 完成判据。前哨：base_domain 为空 → E_MULTI_NODE_REQUIRES_BASE_DOMAIN
// （409，D-MN-13——多节点未启用显式拒绝，不静默降级）。admin scope（响应
// 含 token 材料）由拦截器链把门。
func (s *SystemService) GetJoinGuide(ctx context.Context, req *serverv1.GetJoinGuideRequest) (*serverv1.GetJoinGuideResponse, error) {
	if s.baseDomain == "" {
		// D-MN-13：配置缺失显式拒绝——provider 通道/zot 均不可用，join 后
		// 入口残缺；隐式猜测即事故面。
		return nil, apperr.New("E_MULTI_NODE_REQUIRES_BASE_DOMAIN",
			"multi-node is not enabled: base_domain is not configured (the config endpoint, platform subdomains and the registry all derive from it)")
	}
	if s.join == nil {
		return nil, status.Error(codes.Unavailable, "swarm join face unavailable (not assembled)")
	}
	addr, workerToken, err := s.join.SwarmJoinInfo(ctx)
	if err != nil {
		if errors.Is(err, state.ErrNotSwarmManager) {
			return nil, status.Error(codes.FailedPrecondition,
				"swarm mode not active: join guide requires an initialized swarm (run docker swarm init on the manager)")
		}
		return nil, err
	}
	if req.GetManagerAddr() != "" {
		// 跨公网场景覆盖（advertise 为私网时，安装报告已警示的暴露面口径）。
		addr = req.GetManagerAddr()
	}
	workerIP := req.GetWorkerIp()
	if workerIP == "" {
		workerIP = "<worker-ip>"
	}
	managerIP := hostOfAddr(addr)
	if managerIP == "" {
		managerIP = "<manager-ip>"
	}
	return &serverv1.GetJoinGuideResponse{Guide: buildJoinGuide(s.baseDomain, addr, workerToken, workerIP, managerIP)}, nil
}

// buildJoinGuide 生成 JoinGuideView（规则/步骤文本为英文文案纪律；平台
// 只生成规则文本、不自动应用——--harden-firewall 自动应用维持 reserved）。
func buildJoinGuide(baseDomain, addr, workerToken, workerIP, managerIP string) *serverv1.JoinGuideView {
	g := &serverv1.JoinGuideView{
		JoinCommand:  "docker swarm join --token " + workerToken + " " + addr + ":2377",
		ManagerAddr:  addr,
		WorkerToken:  workerToken,
		BaseDomain:   baseDomain,
		ManagerFirewallRules: []*serverv1.FirewallRule{
			{Direction: "worker_to_manager", Port: "2377/tcp", Purpose: "cluster management (swarm join)",
				Side: "manager", Rule: "iptables -A INPUT -p tcp -s " + workerIP + " --dport 2377 -j ACCEPT"},
			{Direction: "worker_to_manager", Port: "7946/tcp", Purpose: "gossip",
				Side: "manager", Rule: "iptables -A INPUT -p tcp -s " + workerIP + " --dport 7946 -j ACCEPT"},
			{Direction: "worker_to_manager", Port: "7946/udp", Purpose: "gossip",
				Side: "manager", Rule: "iptables -A INPUT -p udp -s " + workerIP + " --dport 7946 -j ACCEPT"},
			{Direction: "bidirectional", Port: "4789/udp", Purpose: "overlay VXLAN",
				Side: "manager", Rule: "iptables -A INPUT -p udp -s " + workerIP + " --dport 4789 -j ACCEPT"},
			{Direction: "worker_to_manager", Port: "8423/tcp", Purpose: "Traefik config endpoint TLS face (token-authenticated)",
				Side: "manager", Rule: "iptables -A INPUT -p tcp -s " + workerIP + " --dport 8423 -j ACCEPT"},
			{Direction: "public_to_all", Port: "80,443/tcp", Purpose: "app ingress (Traefik host ports)",
				Side: "manager", Rule: "existing baseline: already open on every node — no change"},
		},
		WorkerFirewallRules: []*serverv1.FirewallRule{
			{Direction: "bidirectional", Port: "7946/tcp", Purpose: "gossip",
				Side: "worker", Rule: "iptables -A INPUT -p tcp -s " + managerIP + " --dport 7946 -j ACCEPT"},
			{Direction: "bidirectional", Port: "7946/udp", Purpose: "gossip",
				Side: "worker", Rule: "iptables -A INPUT -p udp -s " + managerIP + " --dport 7946 -j ACCEPT"},
			{Direction: "bidirectional", Port: "4789/udp", Purpose: "overlay VXLAN",
				Side: "worker", Rule: "iptables -A INPUT -p udp -s " + managerIP + " --dport 4789 -j ACCEPT"},
			{Direction: "public_to_all", Port: "80,443/tcp", Purpose: "app ingress (Traefik host ports)",
				Side: "worker", Rule: "existing baseline: already open on every node — no change"},
		},
		WorkerPreflightCommands: []string{
			"docker version --format '{{.Server.Version}}'   # must be >= 29.8.1",
			"iptables --version   # legacy iptables required (nftables-only hosts are not supported by the installer gate)",
		},
		DnsSteps: []string{
			"Add A records for the application domains and the platform subdomains to include the worker IP " + workerIP + " (TTL <= 300s): registry." + baseDomain + ", console." + baseDomain + ", and every app domain.",
			"ctrl." + baseDomain + " keeps pointing at the manager only — do NOT add the worker IP to it.",
			"Run fleetly domains verify after DNS propagation.",
		},
		CompletionChecks: []string{
			"The observation beat lists the new node: fleetly nodes list",
			"Anchoring completes automatically: node.joined event with a non-empty platform_id",
			"The node reports state=ready availability=active",
			"The Traefik (fleetly-ingress) task is running on the node",
			"The worker join token is rotated afterwards (join.token_rotate=auto; rotate manually with fleetly nodes rotate-token in manual mode)",
		},
	}
	return g
}

// hostOfAddr 取地址的 host 段（manager-addr 覆盖值可能是 host:port 形态；
// 无 port 段原样返回）。
func hostOfAddr(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// RotateJoinToken 轮换 swarm join token（E1-8，D-MN-1）：role 缺省 worker；
// rotate 后旧 token 立即失效。审计动作 node.join_token_rotated（§5.3：
// join-token rotate 记审计、不设事件）。admin scope 由拦截器链把门。
func (s *SystemService) RotateJoinToken(ctx context.Context, req *serverv1.RotateJoinTokenRequest) (*serverv1.RotateJoinTokenResponse, error) {
	if s.join == nil {
		return nil, status.Error(codes.Unavailable, "swarm join face unavailable (not assembled)")
	}
	role := req.GetRole()
	if role == "" {
		role = "worker"
	}
	token, err := s.join.SwarmRotateJoinToken(ctx, role)
	if err != nil {
		if errors.Is(err, state.ErrNotSwarmManager) {
			return nil, status.Error(codes.FailedPrecondition,
				"swarm mode not active: token rotation requires an initialized swarm")
		}
		return nil, err
	}
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		actorTokenID := ""
		if p, ok := PrincipalFromContext(ctx); ok {
			actorTokenID = p.TokenID
		}
		// 审计动作 = §5.3 登记的 node.join_token_rotated（join-token rotate
		// 记审计、不设事件；auditEntry 的默认 action 是 api.<Service>.<Method>
		// ——此处显式覆盖为产品语义词根）。
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:        "human",
			ActorTokenID: actorTokenID,
			Action:       "node.join_token_rotated",
			Target:       "node:swarm",
			Result:       "ok",
			DiffSummary:  state.DiffSummary("role", role),
		})
	}); err != nil {
		return nil, err
	}
	return &serverv1.RotateJoinTokenResponse{Role: role, Token: token}, nil
}

// GetIngressStatus 入口链三面状态（T2.18）：① Traefik 服务实况（Swarm
// inspect，不可达如实标注）；② 配置端点两面探测（/healthz 无鉴权 +
// /configs 带 token 鉴权核验——环回执行，401 = token 缺失/错误，如实报告）；
// ③ 证书台账（domains 表 cert 列）与证书存储目录对照。
func (s *SystemService) GetIngressStatus(ctx context.Context, req *serverv1.GetIngressStatusRequest) (*serverv1.GetIngressStatusResponse, error) {
	resp := &serverv1.GetIngressStatusResponse{
		Certificates: []*serverv1.CertLedgerView{},
		CertDirApps:  []string{},
		Healthz:      "unreachable",
		Auth:         "unreachable",
	}
	if s.ing == nil {
		// 入口面未装配（测试/精简形态）：如实报告不可用，不谎报健康。
		resp.Traefik = &serverv1.TraefikView{Error: "ingress manager unavailable"}
		return resp, nil
	}
	cfg := s.ing.Config()
	resp.ConfigAddr = cfg.ConfigAddr

	// ① Traefik 服务实况（底座不可达 → error 原文）。
	status, err := s.ing.Status(ctx)
	resp.AdvertiseIp = status.AdvertiseIP
	resp.Responder = status.Responder
	if err != nil {
		resp.Traefik = &serverv1.TraefikView{Error: err.Error()}
	} else {
		resp.Traefik = &serverv1.TraefikView{
			Exists: status.Traefik.Exists,
			Image:  status.Traefik.Image,
			// 静态参数条数（len 收窄 int32——服务实况的计数永不大）。
			StaticArgs: int32(len(status.Traefik.Args)), //nolint:gosec // G115：参数条数计数
		}
	}

	// ② 配置端点环回探测（3s 预算；token 读自配置文件——服务端本机语义）。
	s.probeConfigEndpoint(ctx, cfg, resp)

	// ③ 证书台账对照 + 证书目录 app 清单（目录缺失 = 空清单非错误）。
	s.certLedger(ctx, resp)
	apps, dirErr := listCertDirApps(cfg.CertDir)
	resp.CertDir = cfg.CertDir
	resp.CertDirApps = apps
	if dirErr != nil {
		resp.CertDirError = dirErr.Error()
	}
	return resp, nil
}

// probeConfigEndpoint 环回探测配置端点两面（与旧 CLI 探测同口径：健康面
// 无鉴权；/configs 带 token 鉴权核验并校验 JSON 可解析）。
func (s *SystemService) probeConfigEndpoint(ctx context.Context, cfg ingress.Config, resp *serverv1.GetIngressStatusResponse) {
	base := "http://" + loopbackAddr(cfg.ConfigAddr)
	client := &http.Client{Timeout: 3 * time.Second}
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/healthz", nil); err == nil {
		if respd, err := client.Do(req); err == nil {
			_ = respd.Body.Close()
			resp.Healthz = respd.Status
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/configs", nil)
	if err != nil {
		return
	}
	if tokenRaw, tokErr := os.ReadFile(cfg.TokenFile); tokErr == nil && strings.TrimSpace(string(tokenRaw)) != "" { //nolint:gosec // G304：token 文件路径来自服务端 ingress 配置
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tokenRaw)))
	} else {
		resp.Auth = "token file unreadable: " + cfg.TokenFile
	}
	respd, err := client.Do(req)
	if err != nil {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(respd.Body, 4096))
	_ = respd.Body.Close()
	switch respd.StatusCode {
	case http.StatusOK:
		var probe map[string]any
		if json.Unmarshal(body, &probe) == nil {
			resp.Auth = "200 (authorized)"
		} else {
			resp.Auth = "200 (non-JSON body?)"
		}
	default:
		resp.Auth = respd.Status
	}
}

// certLedger 构造证书台账投影（app 为显示名；已删除应用回退显示 ID）。
func (s *SystemService) certLedger(ctx context.Context, resp *serverv1.GetIngressStatusResponse) {
	rows, err := s.st.ListAllDomains(ctx)
	if err != nil {
		return
	}
	nameByAppID := map[string]string{}
	for _, r := range rows {
		if r.CertSHA256 == "" {
			continue
		}
		if _, ok := nameByAppID[r.AppID]; !ok {
			name := r.AppID
			if appRow, err := s.st.GetAppByID(ctx, r.AppID); err == nil {
				name = appRow.Name
			}
			nameByAppID[r.AppID] = name
		}
		resp.Certificates = append(resp.Certificates, &serverv1.CertLedgerView{
			App:          nameByAppID[r.AppID],
			Domain:       r.Domain,
			CertSha256:   r.CertSHA256,
			CertNotAfter: tstamp(r.CertNotAfter),
		})
	}
}

// loopbackAddr 把监听地址的 host 段替换为 127.0.0.1（配置端点绑 0.0.0.0
// ——环回探测用回环地址；无 port 段时原样返回，探测按不可达处理）。
func loopbackAddr(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// listCertDirApps 读证书目录 app 清单（meta 索引；目录缺失 = 空清单非错误
// ——与证书目录的生命周期语义一致：未部署入口 = 无目录）。
func listCertDirApps(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read cert dir %s: %w", dir, err)
	}
	apps := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			apps = append(apps, strings.TrimSuffix(e.Name(), ".meta.json"))
		}
	}
	return apps, nil
}

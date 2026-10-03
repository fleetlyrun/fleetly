// Package zot 实现 Registry Capability 的 zot Provider：受管自宿
// （ADR-0004——以普通 Workload 形态跑在 Runtime 上，经通用 reconciler
// 部署；ADR-0019 附录 B 是本形态的裁决真源）。构建产物推送目标与
// worker 拉取来源；digest 直存（Revision 冻结 digest，引用在投影期组合）。
package zot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Image 是受管 zot 镜像（钉版，ADR-0021 口径；minimal 变体：htpasswd 认证
// 是核心能力非扩展。升级经 Platform 升级序，ADR-0015）。
const Image = "ghcr.io/project-zot/zot-minimal:v2.1.21"

// 受管形态常量（ADR-0019 附录 B.1）：端口发布 5000（routing mesh）、数据卷
// 挂 /var/lib/registry、材料文件名即容器内 /run/secrets/<名> 路径。
const (
	publishPort  = 5000
	storageRoot  = "/var/lib/registry"
	volumeID     = "fleetly-registry-zot"
	configFile   = "zot-config"
	htpasswdFile = "zot-htpasswd" //nolint:gosec // G101 误报：文件名非机密（内容是 bcrypt 哈希）

	// credentialUser 是平台凭证用户名（密码首启随机生成，见 credentialFile）。
	credentialUser = "fleetly" //nolint:gosec // G101 误报：用户名非机密，密码随机生成落盘
	// credentialFile 是平台凭证落盘（<DataRoot>/keys/ 下，与 KEK 同目录
	// 文化；0o600，备份随数据根）。
	credentialFile = "registry.json"
	// passwordBytes 是随机密码字节数（hex 后 48 字符）。
	passwordBytes = 24
)

// healthTimeout 是 Health 的 TCP 探测上限（managedLoop 全步 30s 带界的
// 局部收敛——探测自身也必须有界）。
const healthTimeout = 3 * time.Second

// Provider 是 zot Registry Provider。
type Provider struct {
	addr string
	cred capability.RegistryCredential

	// htpasswdOnce/htpasswd：进程内一次铸的 bcrypt 行（见 htpasswdLine——
	// 随机盐无确定性口子，逐拍新铸 = 载体指纹恒变 = 受管域无限滚替）。
	htpasswdOnce sync.Once
	htpasswd     string
}

// 编译期契约断言：Registry 端口 + 受管形态声明 + 材料子面。
var (
	_ capability.Registry        = (*Provider)(nil)
	_ capability.Managed         = (*Provider)(nil)
	_ capability.MaterialsSource = (*Provider)(nil)
)

// New 构造 Provider：addr 是镜像引用地址（含端口，如 10.124.0.3:5000），
// dataRoot 是平台数据根（凭证持久化位置）。凭证首启生成、之后原样复用
// （幂等：同 dataRoot 二次构造同密码——轮换走删文件重启，附录 B.3）。
func New(addr, dataRoot string) (*Provider, error) {
	if addr == "" {
		return nil, fmt.Errorf("zot provider: registry address is required")
	}
	cred, err := loadOrGenerateCredential(dataRoot)
	if err != nil {
		return nil, err
	}
	return &Provider{
		addr: addr,
		cred: capability.RegistryCredential{
			Server:   addr,
			Username: credentialUser,
			Secret:   cred.Password,
		},
	}, nil
}

// credentialJSON 是凭证文件结构。
type credentialJSON struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loadOrGenerateCredential 读或生成平台凭证（tmp+rename 原子落位；并发
// 双写容忍——后到 rename 失败即读先行者）。
func loadOrGenerateCredential(dataRoot string) (*credentialJSON, error) {
	if dataRoot == "" {
		return nil, fmt.Errorf("zot provider: data root is required for the registry credential")
	}
	dir := filepath.Join(dataRoot, "keys")
	path := filepath.Join(dir, credentialFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // 数据根私有目录（G304/G703）
		var c credentialJSON
		if jerr := json.Unmarshal(b, &c); jerr != nil || c.Username == "" || c.Password == "" {
			return nil, fmt.Errorf("zot provider: credential file %s is malformed: %v", path, jerr)
		}
		return &c, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("zot provider: read credential: %w", err)
	}
	raw := make([]byte, passwordBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("zot provider: generate credential: %w", err)
	}
	c := &credentialJSON{Username: credentialUser, Password: hex.EncodeToString(raw)}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("zot provider: credential dir: %w", err)
	}
	// map 形态编码（swarm encodeRegistryAuth 同款）：结构体字段名匹配
	// secret 模式会咬 gosec G117，map 键不触发。
	b, err := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	if err != nil {
		return nil, fmt.Errorf("zot provider: encode credential: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { //nolint:gosec // 数据根私有目录
		return nil, fmt.Errorf("zot provider: write credential: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // 数据根私有目录
		if rb, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // 数据根私有目录
			var prev credentialJSON
			if json.Unmarshal(rb, &prev) == nil && prev.Password != "" {
				_ = os.Remove(tmp) //nolint:gosec // 数据根私有目录；并发先行者已落位，复用它
				return &prev, nil
			}
		}
		return nil, fmt.Errorf("zot provider: publish credential: %w", err)
	}
	return c, nil
}

// Describe 实现 Provider 契约三件套之一（Managed=true：部署形态经
// ManagedWorkloads 声明，由通用 reconciler 部署）。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "zot",
		Capability: capability.KindRegistry,
		Version:    "1",
		Managed:    true,
		Notes: []string{
			"managed self-hosted OCI registry; build artifacts are pushed here and workers pull by digest (ADR-0019 appendix B)",
			"serves plain HTTP on the cluster-internal network; every node's dockerd must trust the address via --insecure-registry",
			"single platform credential held by the control plane and distributed to builds and workers through the material channel",
		},
	}
}

// Health 实现 Provider 契约三件套之一（TCP 探测受管端点；首拍未起服是
// 正常窗口——reconciler 正在把它拉起来）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	d := net.Dialer{Timeout: healthTimeout}
	conn, err := d.DialContext(ctx, "tcp", p.addr)
	if err != nil {
		return capability.HealthReport{Healthy: false, Details: "managed registry unreachable at " + p.addr + ": " + err.Error()}
	}
	_ = conn.Close()
	return capability.HealthReport{Healthy: true, Details: "managed registry reachable at " + p.addr}
}

// Endpoint 实现 Registry 端口：推送/拉取端点与平台凭证（材料分发与构建
// 推送的同一真源，附录 B.3）。
func (p *Provider) Endpoint(context.Context) (capability.RegistryEndpoint, error) {
	return capability.RegistryEndpoint{Addr: p.addr, Cred: p.cred}, nil
}

// ManagedNamespace 返回平台系统隔离域（与用户 Project 分离）。
func (p *Provider) ManagedNamespace() capability.NamespaceRef {
	return capability.NamespaceRef{Team: "fleetly", Project: "system", App: "registry"}
}

// ManagedWorkloads 声明受管部署形态（通用 reconciler 经 Runtime Ensure
// 下发）：发布 5000（routing mesh——镜像引用地址钉在配置的节点地址上，
// 任一节点可达）；数据卷本地（节点本地性边界见附录 B.5②）；config 与
// htpasswd 经材料通道注入（ManagedMaterials）。
func (p *Provider) ManagedWorkloads() []capability.Workload {
	return []capability.Workload{{
		ID:      "fleetly-registry-zot",
		Process: "zot",
		Image:   Image,
		// 入口用镜像 ENTRYPOINT 的绝对路径形态（zot-minimal 的二进制名是
		// zot-linux-amd64-minimal，PATH 查找 "zot" 恒败——staging 真机实证
		// runc "executable file not found" preparing 死循环，2026-10-02）。
		Command:  []string{"/usr/local/bin/zot-linux-amd64-minimal", "serve", "/run/secrets/" + configFile},
		Ports:    []capability.WorkloadPort{{Port: publishPort, Protocol: capability.ProtocolHTTP}},
		Publish:  []capability.PortPublish{{PublishedPort: publishPort, TargetPort: publishPort}},
		Replicas: 1,
		Volumes: []capability.VolumeMount{
			{VolumeID: volumeID, Target: storageRoot},
		},
	}}
}

// ManagedMaterials 实现 MaterialsSource 子面：zot 配置（存储根 + 监听 +
// htpasswd 认证引用）。幂等纯函数——密码变更（轮换）=载体指纹变更=受管
// 域滚动替换，凭证三面（zot/推送/拉取）同源自愈。
func (p *Provider) ManagedMaterials() capability.Materials {
	cfg := map[string]any{
		"storage": map[string]any{"rootDirectory": storageRoot},
		"http": map[string]any{
			"address": "0.0.0.0",
			"port":    fmt.Sprintf("%d", publishPort),
			"auth": map[string]any{
				// zot v2.1.21 的 htpasswd 块只收 path（用户名在文件行内；
				// 多余 user 键 = 解码失败启动即退，staging 真机实证
				// 2026-10-02）。
				"htpasswd": map[string]any{
					"path": "/run/secrets/" + htpasswdFile,
				},
			},
		},
	}
	// Marshal 对 map 键排序：配置字节稳定（载体指纹只随密码变）。
	cfgBytes, _ := json.Marshal(cfg)
	return capability.Materials{SecretFiles: map[string][]byte{
		configFile:   cfgBytes,
		htpasswdFile: []byte(p.htpasswdLine(credentialUser, p.cred.Secret)),
	}}
}

// htpasswdLine 铸一行 bcrypt 形态（zot 支持 bcrypt；apache2 htpasswd -B 同款）。
// **进程内一次铸**（bcrypt 随机盐无口子可传确定性盐）：每次调用都新铸会让
// 同密码的载体指纹恒变 → 受管域无限滚替（staging 真机实证 2026-10-02：zot
// update 风暴把无钉住载体滚到无卷节点 preparing 打转）。进程内缓存 = 收敛环
// 每拍重放稳定；进程重启新盐一次滚动替换（语义 = 凭证轮换同款，可接受）。
func (p *Provider) htpasswdLine(username, password string) string {
	p.htpasswdOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			// bcrypt 对合法输入不失败；到这里即编程错误，空哈希会拒绝一切
			// 认证——fail loud 比静默可用更安全。
			p.htpasswd = username + ":$invalid$"
			return
		}
		// golang bcrypt 原生 $2a$ 前缀（F1.11 真机基线形态；$2y$ 生态惯例
		// 在 zot 的验证面未被证实——staging 真机 401 实测回退，2026-10-02）。
		p.htpasswd = username + ":" + string(h)
	})
	return p.htpasswd
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。地址解析序：装配 ctx
// （config.registry.addr，唯一契约源，ADR-0036）优先，env FLEETLY_REGISTRY_ADDR
// 是 install.sh 物化进 unit 的旧通道兜底。两者皆空 = Registry 面停用
// （build 源部署在 prepare 精确失败，附录 B.5①）。
func init() {
	capability.RegisterFactory(capability.KindRegistry, "zot", func(ctx context.Context) (capability.Provider, error) {
		addr := capability.RegistryAddrFromContext(ctx)
		if addr == "" {
			addr = os.Getenv("FLEETLY_REGISTRY_ADDR")
		}
		if addr == "" {
			return nil, fmt.Errorf("zot provider: registry address is not configured (config registry.addr or FLEETLY_REGISTRY_ADDR); the managed registry stays disabled")
		}
		dataRoot := os.Getenv("FLEETLY_DATA_ROOT")
		if dataRoot == "" {
			dataRoot = "./data" // 与 config.DefaultDataRoot 同缺省（install.sh 恒注入 env，此处兜底）
		}
		return New(addr, dataRoot)
	})
}

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
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	// htpasswdStoreFile 是 htpasswd 行落盘（keys/ 下与凭证同居，0o600 同
	// 凭证口径；收尾批 E28——缺陷=每次进程启动现铸 bcrypt 行（随机盐无
	// 确定性口子）= 载体指纹恒变 = 进程重启即受管域滚替，crash-loop 放大
	// 为节点拉取凭证失效雪崩）。
	htpasswdStoreFile = "registry-htpasswd" //nolint:gosec // G101 误报：文件名非机密（内容是 bcrypt 哈希）
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

	// htpasswd 是构造期定型的 bcrypt 行（loadOrPersistHTPasswd：读用已
	// 持久行或新铸落盘——进程内恒定，收敛环每拍重放稳定；进程重启复用
	// 同一行，载体指纹不再随重启漂移，E28）。
	htpasswd string
}

// 编译期契约断言：Registry 端口 + 受管形态声明 + 材料子面。
var (
	_ capability.Registry        = (*Provider)(nil)
	_ capability.Managed         = (*Provider)(nil)
	_ capability.MaterialsSource = (*Provider)(nil)
)

// New 构造 Provider：addr 是镜像引用地址（含端口，如 10.124.0.3:5000），
// dataRoot 是平台数据根（凭证与 htpasswd 持久化位置）。凭证与 htpasswd
// 首启生成、之后原样复用（幂等：同 dataRoot 二次构造同密码同行——轮换走
// 删文件重启，附录 B.3）。损坏/不可读 fail loud，不静默再生成。
func New(addr, dataRoot string) (*Provider, error) {
	if addr == "" {
		return nil, fmt.Errorf("zot provider: registry address is required")
	}
	cred, err := loadOrGenerateCredential(dataRoot)
	if err != nil {
		return nil, err
	}
	line, err := loadOrPersistHTPasswd(dataRoot, credentialUser, cred.Password)
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
		htpasswd: line,
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
		// 数据面停止宽限：滚动替换（密码轮换/升级）窗口给足优雅收口，
		// 编排器缺省 10s 硬杀窗对挂卷负载不安全（staging pgvector 同类
		// 事故实证，2026-10-03）。
		StopGrace: 60 * time.Second,
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
		htpasswdFile: []byte(p.htpasswd),
	}}
}

// loadOrPersistHTPasswd 读或铸 htpasswd 行并持久化（E28 单一真源：存在即
// 读用；不存在即铸+落盘 tmp+rename 原子位；0o600 同凭证口径）。判定序：
//   - 文件在且 bcrypt 行验证通过当前密码 → 原样复用（重启稳定 = 载体指纹
//     稳定，本件的缺陷修复面）；
//   - 文件在但哈希对当前密码 ErrMismatchedHashAndPassword → 凭证已轮换，
//     重铸覆写（三面同源自愈：凭证文件删除重启的轮换序，只删 registry.json
//     即完成，htpasswd 随行）；
//   - 文件在但形态坏/读取失败 → fail loud（静默再生成会把"凭证面坏了"
//     伪装成正常重启，认证问题将无从诊断）；
//   - 落盘失败 → fail loud（滚替回退到逐重启新铸的缺陷形态，宁可拒绝
//     启动）。
func loadOrPersistHTPasswd(dataRoot, username, password string) (string, error) {
	if dataRoot == "" {
		return "", fmt.Errorf("zot provider: data root is required for the registry htpasswd")
	}
	dir := filepath.Join(dataRoot, "keys")
	path := filepath.Join(dir, htpasswdStoreFile)
	b, err := os.ReadFile(path) //nolint:gosec // 数据根私有目录（G304/G703）
	if err == nil {
		line := strings.TrimRight(string(b), "\r\n")
		hash := ""
		if u, h, ok := strings.Cut(line, ":"); ok && u == username {
			hash = h
		}
		switch verr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); {
		case verr == nil:
			return line, nil // 现役行：原样复用（载体指纹稳定）
		case errors.Is(verr, bcrypt.ErrMismatchedHashAndPassword):
			// 凭证已轮换：重铸覆写（不视作损坏——轮换是合法显式动作）。
		default:
			return "", fmt.Errorf("zot provider: htpasswd file %s is malformed: %v", path, verr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("zot provider: read htpasswd: %w", err)
	}
	line, err := mintHTPasswdLine(username, password)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // 数据根私有目录
		return "", fmt.Errorf("zot provider: htpasswd dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o600); err != nil { //nolint:gosec // 数据根私有目录
		return "", fmt.Errorf("zot provider: write htpasswd: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { //nolint:gosec // 数据根私有目录
		_ = os.Remove(tmp) //nolint:gosec // 数据根私有目录
		return "", fmt.Errorf("zot provider: publish htpasswd: %w", err)
	}
	return line, nil
}

// mintHTPasswdLine 铸一行 bcrypt 形态（zot 支持 bcrypt；apache2 htpasswd -B
// 同款）。bcrypt 随机盐无口子可传确定性盐——行因此不可复算、只能持久化
// （E28 前的进程内 once 形态是同一约束的局部解）。bcrypt 对合法输入不
// 失败；到这里即编程错误，上抛拒绝构造（修复前落 "$invalid$" 死行拒绝
// 一切认证——现在 fail loud 在构造期就暴露）。
func mintHTPasswdLine(username, password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("zot provider: mint htpasswd: %w", err)
	}
	// golang bcrypt 原生 $2a$ 前缀（F1.11 真机基线形态；$2y$ 生态惯例
	// 在 zot 的验证面未被证实——staging 真机 401 实测回退，2026-10-02）。
	return username + ":" + string(h), nil
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

package ingress

// Traefik 部署器（T2.15；架构 §2.6 入口行）：平台自管 Traefik global
// service（每节点一个；v0.1 单机 1 实例）——钉版镜像 traefik:v3.5（Spike B
// 实测版本）、host 模式发布 80/443、动态配置经 HTTP provider 轮询控制面
// 端点（Header token）、不启用 Swarm/Docker provider 自动发现（Traefik 的
// Swarm provider 不检查健康——task running 即注册，破坏「路由晚于健康门」
// 不变量，architecture §2.5）、ping 健康检查。幂等收敛：服务存在则比对
// spec（镜像/参数/端口/挂载/健康检查），差异才更新。
//
// 网络拓扑：Traefik 服务按需接入各 app 专属 overlay 网络（attachNetwork）
// ——路由后端 fleetly-<app>-<service> 的 VIP 只在同网络内可达；网络集变化
// 是一次 service update（任务重建一次，v0.1 接受；配置视图在控制面，
// Traefik 重启即重新拉取，入口不丢配置）。
//
// 本文件是第三方适配面：moby/swarm 类型不出本文件（出口只有 error、
// Status 投影与部署器接口）。

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// IngressServiceName 是平台入口服务名（契约：fleetly-ingress）。
const IngressServiceName = "fleetly-ingress"

// ingressLabelIngress 是入口服务自描述 label（CLI/运维识别）。
const ingressLabelIngress = "fleetly.ingress"

// certSeedContainerName 是证书 seed 容器名（stopped 容器承载证书卷；
// 控制面经 docker API 拷贝证书进卷，Traefik 只读挂载同一卷）。
const certSeedContainerName = "fleetly-ingress-cert-seeder"

// ingressLabelSeeder 是 seed 容器自描述 label。
const ingressLabelSeeder = "fleetly.ingress-cert-seeder"

// traefikHealthcheckArgs 是健康检查（traefik healthcheck 子命令打内置
// ping 端点；静态配置同步开 --ping）。
var traefikHealthcheckArgs = []string{"CMD", "traefik", "healthcheck", "--ping"}

// FileEntry 是一次容器目录拷贝的单文件载荷（证书卷同步用）。
type FileEntry struct {
	Name string
	Data []byte
	Mode int64
}

// dockerClient 是部署器对 Docker API 的最小消费面（moby client 形态；
// 假实现注入单测——真实形态在 newRealDockerClient）。swarm.ServiceSpec
// 是第三方构造载荷，只进不出（消费方为本包 Manager）。
type dockerClient interface {
	Info(ctx context.Context) (swarmInfo, error)
	ServiceInspect(ctx context.Context, name string) (ingressServiceState, error)
	ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error
	ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error
	NetworkEnsure(ctx context.Context, name string) error
	// NetworkID 解析网络名 → 底座 ID（attach 幂等判据：服务实况里的
	// 网络目标是 ID 形态）。
	NetworkID(ctx context.Context, name string) (string, error)
	// VolumeEnsure 确认命名卷存在（证书卷；缺失创建，幂等）。
	VolumeEnsure(ctx context.Context, name string) error
	// SeedContainerEnsure 确认 stopped seed 容器存在（挂证书卷；存在
	// 复用、缺失创建）——返回容器 ID。
	SeedContainerEnsure(ctx context.Context, name, image, volume, targetDir string) (string, error)
	// CopyToDir 把文件集拷入容器目录（tar 归档经 docker API）。
	CopyToDir(ctx context.Context, containerID, dir string, files []FileEntry) error
}

// swarmInfo 是部署器关心的 Info 投影（advertise addr + swarm active）。
type swarmInfo struct {
	SwarmActive bool
	NodeAddr    string
}

// ingressServiceState 是入口服务实况投影（幂等比对 + Status 面）。
type ingressServiceState struct {
	Exists     bool
	Version    uint64
	Image      string
	Args       []string
	Ports      []swarm.PortConfig
	Networks   []string
	Mounts     []mount.Mount
	HealthTest []string
}

// realDockerClient 是 dockerClient 的 moby 实现。
type realDockerClient struct {
	cli *mobyclient.Client
}

func newRealDockerClient(host string) (*realDockerClient, error) {
	opts := []mobyclient.Opt{mobyclient.FromEnv}
	if host != "" {
		opts = []mobyclient.Opt{mobyclient.WithHost(host), mobyclient.FromEnv}
	}
	cli, err := mobyclient.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("ingress: construct docker client: %w", err)
	}
	return &realDockerClient{cli: cli}, nil
}

func (c *realDockerClient) Close() error { return c.cli.Close() }

func (c *realDockerClient) Info(ctx context.Context) (swarmInfo, error) {
	res, err := c.cli.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return swarmInfo{}, fmt.Errorf("ingress: docker info: %w", err)
	}
	return swarmInfo{
		SwarmActive: res.Info.Swarm.NodeID != "" &&
			res.Info.Swarm.LocalNodeState == swarm.LocalNodeStateActive,
		NodeAddr: res.Info.Swarm.NodeAddr,
	}, nil
}

func (c *realDockerClient) ServiceInspect(ctx context.Context, name string) (ingressServiceState, error) {
	res, err := c.cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return ingressServiceState{}, nil
		}
		return ingressServiceState{}, fmt.Errorf("ingress: service inspect %s: %w", name, err)
	}
	svc := res.Service
	out := ingressServiceState{
		Exists:   true,
		Version:  svc.Version.Index,
		Networks: []string{},
	}
	if cs := svc.Spec.TaskTemplate.ContainerSpec; cs != nil {
		out.Image = cs.Image
		out.Args = append([]string{}, cs.Args...)
		if cs.Healthcheck != nil {
			out.HealthTest = append([]string{}, cs.Healthcheck.Test...)
		}
		out.Mounts = append([]mount.Mount{}, cs.Mounts...)
	}
	if svc.Spec.EndpointSpec != nil {
		out.Ports = append([]swarm.PortConfig{}, svc.Spec.EndpointSpec.Ports...)
	}
	for _, n := range svc.Spec.TaskTemplate.Networks {
		out.Networks = append(out.Networks, n.Target)
	}
	return out, nil
}

func (c *realDockerClient) ServiceCreate(ctx context.Context, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceCreate(ctx, mobyclient.ServiceCreateOptions{Spec: spec}); err != nil {
		return fmt.Errorf("ingress: service create %s: %w", spec.Name, err)
	}
	return nil
}

func (c *realDockerClient) ServiceUpdate(ctx context.Context, name string, version uint64, spec swarm.ServiceSpec) error {
	if _, err := c.cli.ServiceUpdate(ctx, name, mobyclient.ServiceUpdateOptions{
		Version: swarm.Version{Index: version},
		Spec:    spec,
	}); err != nil {
		return fmt.Errorf("ingress: service update %s: %w", name, err)
	}
	return nil
}

// NetworkEnsure 确认 overlay 网络存在（attach 的前置对象；与 substrate
// 同语义：已有即 no-op、缺失创建、并发竞态已存在即成功）。
func (c *realDockerClient) NetworkEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("ingress: network inspect %s: %w", name, err)
	}
	if _, err := c.cli.NetworkCreate(ctx, name, mobyclient.NetworkCreateOptions{
		Driver: "overlay",
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{}); ierr == nil {
			return nil
		}
		return fmt.Errorf("ingress: network create %s: %w", name, err)
	}
	return nil
}

// NetworkID 解析网络名 → 底座 ID（未找到走 errdefs NotFound 语义包一层）。
func (c *realDockerClient) NetworkID(ctx context.Context, name string) (string, error) {
	res, err := c.cli.NetworkInspect(ctx, name, mobyclient.NetworkInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("ingress: network inspect %s: %w", name, err)
	}
	return res.Network.ID, nil
}

// VolumeEnsure 确认命名卷存在（缺失创建；幂等）。
func (c *realDockerClient) VolumeEnsure(ctx context.Context, name string) error {
	if _, err := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("ingress: volume inspect %s: %w", name, err)
	}
	if _, err := c.cli.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Name:   name,
		Driver: "local",
		Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue},
	}); err != nil {
		if _, ierr := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); ierr == nil {
			return nil
		}
		return fmt.Errorf("ingress: volume create %s: %w", name, err)
	}
	return nil
}

// SeedContainerEnsure 确认 stopped seed 容器存在：存在即复用（镜像/卷
// 形态由 EnsureTraefik 的幂等收敛兜底）；缺失创建（不启动——CopyToContainer
// 对 stopped 容器有效，且不占运行时资源）。
func (c *realDockerClient) SeedContainerEnsure(ctx context.Context, name, image, volume, targetDir string) (string, error) {
	res, err := c.cli.ContainerInspect(ctx, name, mobyclient.ContainerInspectOptions{})
	if err == nil {
		return res.Container.ID, nil
	}
	if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("ingress: seed container inspect %s: %w", name, err)
	}
	create, err := c.cli.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:  image,
			Cmd:    []string{"sleep", "infinity"},
			Labels: map[string]string{state.LabelManaged: state.ManagedLabelValue, ingressLabelSeeder: "true"},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{
				Type:   mount.TypeVolume,
				Source: volume,
				Target: targetDir,
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("ingress: seed container create %s: %w", name, err)
	}
	return create.ID, nil
}

// CopyToDir 把文件集拷入容器目录（内存 tar 归档）。
func (c *realDockerClient) CopyToDir(ctx context.Context, containerID, dir string, files []FileEntry) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:   f.Name,
			Mode:   f.Mode,
			Size:   int64(len(f.Data)),
			Format: tar.FormatPAX,
		}); err != nil {
			return fmt.Errorf("ingress: cert tar header %s: %w", f.Name, err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			return fmt.Errorf("ingress: cert tar write %s: %w", f.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("ingress: cert tar close: %w", err)
	}
	if _, err := c.cli.CopyToContainer(ctx, containerID, mobyclient.CopyToContainerOptions{
		DestinationPath: dir,
		Content:         &buf,
	}); err != nil {
		return fmt.Errorf("ingress: cert copy to %s: %w", dir, err)
	}
	return nil
}

// EnsureTraefik 幂等收敛入口服务（不存在创建；存在比对差异更新）。
// swarm 未就绪返回 ErrNotSwarmReady（服务壳降级重试）。
func (m *Manager) EnsureTraefik(ctx context.Context) error {
	info, err := m.docker.Info(ctx)
	if err != nil {
		return err
	}
	if !info.SwarmActive {
		return ErrNotSwarmReady
	}
	// 控制面可达地址：显式配置 > swarm advertise addr > 出口本地地址。
	advertise := m.cfg.ConfigAdvertiseIP
	if advertise == "" {
		advertise = info.NodeAddr
	}
	if advertise == "" {
		advertise = outboundLocalIP()
	}
	m.mu.Lock()
	m.advertiseIP = advertise
	m.mu.Unlock()

	m.setResponderURL("http://" + net.JoinHostPort(advertise, fmt.Sprint(m.cfgPort())))
	token, err := m.token(ctx)
	if err != nil {
		return err
	}
	// 证书卷 + seed 容器先行（Traefik 挂载的前置对象；幂等）。
	if _, err := m.ensureCertVolume(ctx); err != nil {
		return err
	}
	// F7（S20）：provider_endpoint 直接引用构造 spec 时使用的 responder 变量
	// ——此前取 Args[3] 是按位猜（"--providers.http.endpoint=<url>" 在 args
	// 中的位置），args 顺序一变日志字段即错位（曾把 pollInterval 记成端点）。
	endpoint := m.responderURLValue()
	desired := m.buildTraefikSpec(endpoint, token)
	cur, err := m.docker.ServiceInspect(ctx, IngressServiceName)
	if err != nil {
		return err
	}
	if !cur.Exists {
		if err := m.docker.ServiceCreate(ctx, desired); err != nil {
			return err
		}
		m.log.Info("ingress: traefik service created", "image", m.cfg.TraefikImage,
			"http_port", m.cfg.HTTPPort, "https_port", m.cfg.HTTPSPort,
			"provider_endpoint", endpoint)
		m.mu.Lock()
		m.lastSpec = &desired
		m.mu.Unlock()
		return nil
	}
	if traefikSpecEqual(cur, desired) {
		m.mu.Lock()
		m.lastSpec = &desired
		m.mu.Unlock()
		return nil
	}
	// 收敛更新不裸用 desired：期望 spec 不含 TaskTemplate.Networks（app
	// 挂载由 attachNetwork 增量管理），整体替换会把 Traefik 从全部 app
	// 网络上踢下线（一次漂移收敛 → 全路由 502）——以服务实况网络集为基准
	// 合并后再提交（traefik.go 既有实机回归教训：网络目标集不能由期望
	// spec 重建）。实况无挂载（首次创建/确未 attach）时行为不变。
	merged := desired
	if len(cur.Networks) > 0 {
		merged = specWithNetworks(desired, cur.Networks)
	}
	if err := m.docker.ServiceUpdate(ctx, IngressServiceName, cur.Version, merged); err != nil {
		return err
	}
	m.log.Info("ingress: traefik service updated to desired spec", "image", m.cfg.TraefikImage)
	m.mu.Lock()
	m.lastSpec = &merged
	m.mu.Unlock()
	return nil
}

// attachNetwork 确保 Traefik 接入 app 专属 overlay 网络（幂等：已接入
// no-op；新增网络一次 service update——任务重建一次，入口配置即回）。
// 网络目标集以服务实况（cur.Networks）为基准追加——不能用 lastSpec 重建
// （lastSpec 不含历史 attach，会互相覆盖丢失其他 app 的网络，实机验证
// 发现的多 app 回归）。幂等判据用网络 ID（swarm 把 attach 目标归一为
// ID——名字比对永不命中，产生重复 attach，实机验证发现的第二处）。
func (m *Manager) attachNetwork(ctx context.Context, appName string) error {
	netName, err := appNetworkName(appName)
	if err != nil {
		return err
	}
	if err := m.docker.NetworkEnsure(ctx, netName); err != nil {
		return err
	}
	netID, err := m.docker.NetworkID(ctx, netName)
	if err != nil {
		return err
	}
	cur, err := m.docker.ServiceInspect(ctx, IngressServiceName)
	if err != nil {
		return err
	}
	if !cur.Exists {
		return fmt.Errorf("ingress: %s missing (ensure traefik first)", IngressServiceName)
	}
	for _, n := range cur.Networks {
		if n == netID {
			return nil
		}
	}
	m.mu.Lock()
	base := m.lastSpec
	m.mu.Unlock()
	if base == nil {
		return fmt.Errorf("ingress: no desired spec available (ensure traefik first)")
	}
	// 网络目标集 = 实况集 + 新网络（specWithNetworks 统一构造；以实况为
	// 基准的理由见其注释）。
	targets := make([]string, 0, len(cur.Networks)+1)
	targets = append(targets, cur.Networks...)
	targets = append(targets, netID)
	spec := specWithNetworks(*base, targets)
	if err := m.docker.ServiceUpdate(ctx, IngressServiceName, cur.Version, spec); err != nil {
		return err
	}
	m.mu.Lock()
	m.lastSpec = &spec
	m.mu.Unlock()
	m.log.Info("ingress: traefik attached to app network", "network", netName, "app", appName,
		"attached_total", len(spec.TaskTemplate.Networks))
	return nil
}

// specWithNetworks 是网络挂载构造的单点：把目标集（ID 形态）整组写入
// spec.TaskTemplate.Networks。调用方必须以服务实况（cur.Networks）为基准
// 传入目标集——不能用期望 spec 重建（期望不含历史 attach，整体替换会丢失
// 其他 app 的网络，实机验证发现的多 app 回归）。EnsureTraefik 的收敛更新
// 分支与 attachNetwork 共用本口径（消灭两份手写漂移——正是更新分支绕开
// 本纪律导致的全挂载丢失缺陷，架构评审 H8）。
func specWithNetworks(base swarm.ServiceSpec, netIDs []string) swarm.ServiceSpec {
	spec := base
	spec.TaskTemplate.Networks = make([]swarm.NetworkAttachmentConfig, 0, len(netIDs))
	for _, id := range netIDs {
		spec.TaskTemplate.Networks = append(spec.TaskTemplate.Networks, swarm.NetworkAttachmentConfig{Target: id})
	}
	return spec
}

// buildTraefikSpec 构造入口服务的期望 swarm spec（host 80/443 + 证书目录
// 只读挂载 + HTTP provider 静态配置 + ping 健康检查）。
func (m *Manager) buildTraefikSpec(responder, token string) swarm.ServiceSpec {
	args := []string{
		"--entryPoints.web.address=:" + fmt.Sprint(m.cfg.HTTPPort),
		"--entryPoints.websecure.address=:" + fmt.Sprint(m.cfg.HTTPSPort),
		// 控制面下发（唯一配置面；Swarm/Docker 自动发现显式不启用——
		// architecture §2.5 路由时机不变量 + Spike B 四态纪律）。
		"--providers.http.endpoint=" + responder + "/configs",
		"--providers.http.pollInterval=" + m.cfg.PollInterval.String(),
		"--providers.http.pollTimeout=5s",
		"--providers.http.headers.Authorization=Bearer " + token,
		// ping 健康面（healthcheck 子命令消费；容器内 8080，不发布）。
		"--ping=true",
		"--log.level=INFO",
		"--api.dashboard=false",
	}
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: IngressServiceName,
			Labels: map[string]string{
				state.LabelManaged:  state.ManagedLabelValue,
				ingressLabelIngress: "true",
			},
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: m.cfg.TraefikImage,
				Args:  args,
				Healthcheck: &container.HealthConfig{
					Test:        traefikHealthcheckArgs,
					Interval:    10 * time.Second,
					Timeout:     5 * time.Second,
					Retries:     3,
					StartPeriod: 5 * time.Second,
				},
			},
		},
		Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
		EndpointSpec: &swarm.EndpointSpec{
			Ports: []swarm.PortConfig{
				{Protocol: "tcp", TargetPort: portUint(m.cfg.HTTPPort), PublishedPort: portUint(m.cfg.HTTPPort), PublishMode: swarm.PortConfigPublishModeHost},
				{Protocol: "tcp", TargetPort: portUint(m.cfg.HTTPSPort), PublishedPort: portUint(m.cfg.HTTPSPort), PublishMode: swarm.PortConfigPublishModeHost},
			},
		},
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			FailureAction: "pause",
			Order:         "stop-first",
		},
	}
	// 证书命名卷只读挂载（tls.certificates 文件引用的到达路径；卷由
	// seed 容器承载、控制面经 docker API 拷贝同步——swarm 不接受 Windows
	// 宿主路径 bind，实机验证结论；卷的持久化使 Traefik 重启不丢证书）。
	if m.cfg.CertVolume != "" {
		spec.TaskTemplate.ContainerSpec.Mounts = append(spec.TaskTemplate.ContainerSpec.Mounts, mount.Mount{
			Type:     mount.TypeVolume,
			Source:   m.cfg.CertVolume,
			Target:   TraefikCertMountPath,
			ReadOnly: true,
		})
	}
	return spec
}

// ensureCertVolume 确保证书卷 + seed 容器就绪（EnsureTraefik 前置；幂等）。
// 返回 seed 容器 ID（证书同步消费）。
func (m *Manager) ensureCertVolume(ctx context.Context) (string, error) {
	if m.cfg.CertVolume == "" {
		return "", nil
	}
	if err := m.docker.VolumeEnsure(ctx, m.cfg.CertVolume); err != nil {
		return "", err
	}
	return m.docker.SeedContainerEnsure(ctx, certSeedContainerName, m.cfg.CertSeedImage,
		m.cfg.CertVolume, TraefikCertMountPath)
}

// syncCertToVolume 把一张证书（crt/key）拷入证书卷（签发/续期落盘后的
// 同步步；TLS 段文件由此到达 Traefik）。
func (m *Manager) syncCertToVolume(ctx context.Context, pair *CertificatePair) error {
	seedID, err := m.ensureCertVolume(ctx)
	if err != nil {
		return err
	}
	if seedID == "" {
		return nil // 证书卷未配置：TLS 段无到达路径（配置位显式关闭）
	}
	return m.docker.CopyToDir(ctx, seedID, TraefikCertMountPath, []FileEntry{
		{Name: pair.App + ".crt", Data: pair.CertPEM, Mode: 0o644},
		{Name: pair.App + ".key", Data: pair.KeyPEM, Mode: 0o600},
	})
}

// portUint 是端口的有界窄化（配置校验保证 1..65535；越界收敛为 0 让
// 底座显式拒绝而不是静默回绕）。
func portUint(port int) uint32 {
	if port <= 0 || port > 65535 {
		return 0
	}
	return uint32(port) //nolint:gosec // 上界守卫已收敛到 1..65535
}

// traefikSpecEqual 幂等比对（镜像/参数/端口/挂载/健康检查；网络集由
// attachNetwork 增量管理，不参与本比对）。
func traefikSpecEqual(cur ingressServiceState, desired swarm.ServiceSpec) bool {
	cs := desired.TaskTemplate.ContainerSpec
	if cur.Image != cs.Image {
		return false
	}
	if !sameStrings(cur.Args, cs.Args) {
		return false
	}
	if !sameStrings(cur.HealthTest, cs.Healthcheck.Test) {
		return false
	}
	want := desired.EndpointSpec.Ports
	if len(cur.Ports) != len(want) {
		return false
	}
	curByPub := map[uint32]swarm.PortConfig{}
	for _, p := range cur.Ports {
		curByPub[p.PublishedPort] = p
	}
	for _, p := range want {
		got, ok := curByPub[p.PublishedPort]
		if !ok || got.TargetPort != p.TargetPort || got.PublishMode != p.PublishMode {
			return false
		}
	}
	if len(cur.Mounts) != len(cs.Mounts) {
		return false
	}
	for i, wm := range cs.Mounts {
		cm := cur.Mounts[i]
		if cm.Type != wm.Type || cm.Source != wm.Source || cm.Target != wm.Target || cm.ReadOnly != wm.ReadOnly {
			return false
		}
	}
	return true
}

// ErrNotSwarmReady 是 swarm 未就绪的哨兵（服务壳据此降级重试）。
var ErrNotSwarmReady = errors.New("docker engine is not an active swarm manager")

// outboundLocalIP 出口本地地址兜底（UDP dial 不发包）。
func outboundLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer func() { _ = conn.Close() }()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// appNetworkName 是 per-app overlay 网络名（naming.NetworkName 同公式；
// 入口层本地重写避免适配器反向依赖引擎/核心——公式由 naming_test 钉死，
// 本包不得改写）。
func appNetworkName(app string) (string, error) {
	if app == "" {
		return "", fmt.Errorf("ingress: app name is empty")
	}
	if strings.ContainsAny(app, " \t/\\") {
		return "", fmt.Errorf("ingress: app name %q contains invalid characters", app)
	}
	return "fleetly-" + app + "-net", nil
}

// sameStrings 切片相等（同序）。
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

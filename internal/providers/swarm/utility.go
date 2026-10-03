package swarm

// 工具容器执行（ADR-0039 决策 3）：RuntimeUtility 子面的 swarm 实现——
// 控制面 daemon 一次性容器（manager 的 daemon 即控制面 docker，构建链
// 同源）。attachable overlay 承担多节点可达性（daemon 容器入项目网解析
// db-<id> DNS）；载体标签 fleetly.utility=true、结束即删零残留。
//
// hermetic 测试经 utilityExec 函数值缝（builders push seam 同款理由：
// attach 走 postHijacked 的独立 dialer，传输级假面够不到；零值回退生产
// 实现，真机路径由 dind 演练 e2e 承担）。

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// 工具容器载体命名与标记（Provider 私有；标签供清扫与诊断识别）。
const (
	labelUtility  = "fleetly.utility"
	utilityPrefix = "fleetly-utility-"
)

// utilityRemoveTimeout 是收尾删除的独立带界（WithoutCancel——主 ctx 可能
// 已因超时/取消退出，删除仍须发生；短界防 Remove 自身挂死拖住调用方）。
const utilityRemoveTimeout = 15 * time.Second

// utilityContainerSpec 是 daemon 生命周期的完整输入（组装纯逻辑的产品，
// hermetic 测试可独立断言）。
type utilityContainerSpec struct {
	name       string
	image      string
	argv       []string
	env        []string // 排序稳定（spec 对账/测试确定性）
	networks   []string // 载体名（全部附着；首个进 NetworkMode）
	binds      []string // bind 语法（材料文件 ro / 命名卷按需）
	openStdin  bool     // Stdin 在场
	attachHint string   // 不可附着类错误的可行动提示（存量网络 flag-day）
}

// RunUtility 实现 RuntimeUtility 子面：受理校验 → 材料落盘（bind 承载）
// → 组装 spec → 生命周期执行 → 退出码语义映射。
func (p *Provider) RunUtility(ctx context.Context, req capability.UtilityRequest, stdout, stderr io.Writer) error {
	if req.ID == "" || req.Image == "" || len(req.Argv) == 0 || len(req.Networks) == 0 {
		return fmt.Errorf("swarm utility: id, image, argv and networks are required")
	}
	// 材料文件落盘：临时目录 + 只读 bind（值不落 label/env；目录随执行
	// 结束移除）。文件名钳制（防穿越出目录——端口不信任调用方）。
	dir, err := os.MkdirTemp("", "fleetly-utility-")
	if err != nil {
		return fmt.Errorf("swarm utility: material dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binds := make([]string, 0, len(req.SecretFiles)+1)
	for name, value := range req.SecretFiles {
		if err := safeUtilityMaterialName(name); err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, value, 0o600); err != nil {
			return fmt.Errorf("swarm utility: write material %q: %w", name, err)
		}
		binds = append(binds, path+":/run/secrets/"+name+":ro")
	}
	if req.Volume != nil {
		mode := "rw"
		if req.Volume.ReadOnly {
			mode = "ro"
		}
		binds = append(binds, fmt.Sprintf("%s:%s:%s", volumeCarrierName(req.Volume.VolumeID), req.Volume.Target, mode))
	}
	env := make([]string, 0, len(req.Env))
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	spec := utilityContainerSpec{
		name:       sanitizeNamePart(utilityPrefix + req.ID),
		image:      req.Image,
		argv:       req.Argv,
		env:        env,
		networks:   utilityNetworkCarriers(req.Namespace, req.Networks),
		binds:      binds,
		openStdin:  req.Stdin != nil,
		attachHint: "project networks created before fleetly F2.2 are not attachable; see the operations runbook to recreate the network",
	}
	exec := p.daemonUtilityExec
	if p.utilityExec != nil {
		exec = p.utilityExec
	}
	exitCode, err := exec(ctx, spec, stdout, stderr, req.Stdin)
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("swarm utility %s: container exited with code %d", req.ID, exitCode)
	}
	return nil
}

// daemonUtilityExec 是工具容器的 daemon 生命周期（create → attach →
// start → wait → remove；镜像缺失时先匿名拉取）。返回退出码。
func (p *Provider) daemonUtilityExec(ctx context.Context, spec utilityContainerSpec, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
	if err := p.ensureUtilityImage(ctx, spec.image); err != nil {
		return 0, err
	}
	endpoints := make(map[string]*network.EndpointSettings, len(spec.networks))
	for _, n := range spec.networks {
		endpoints[n] = &network.EndpointSettings{}
	}
	created, err := p.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: spec.name,
		Config: &container.Config{
			Image:      spec.image,
			Cmd:        spec.argv,
			Env:        spec.env,
			OpenStdin:  spec.openStdin,
			Labels:     map[string]string{labelManaged: "true", labelUtility: "true"},
			Entrypoint: []string{},
		},
		HostConfig: &container.HostConfig{
			Binds:       spec.binds,
			NetworkMode: container.NetworkMode(spec.networks[0]),
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: endpoints},
	})
	if err != nil {
		return 0, wrapUtilityEnvError(fmt.Errorf("swarm utility: create container: %w", err), spec.attachHint)
	}
	id := created.ID
	// 结束即删（含中断路径；daemon 对已消失容器的 Remove 幂等）。
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), utilityRemoveTimeout)
		defer cancel()
		_, _ = p.cli.ContainerRemove(rctx, id, client.ContainerRemoveOptions{Force: true})
	}()

	attach, err := p.cli.ContainerAttach(ctx, id, client.ContainerAttachOptions{
		Stream: true, Stdin: spec.openStdin, Stdout: true, Stderr: true,
	})
	if err != nil {
		return 0, fmt.Errorf("swarm utility: attach container: %w", err)
	}
	defer attach.Close()

	if _, err := p.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return 0, wrapUtilityEnvError(fmt.Errorf("swarm utility: start container: %w", err), spec.attachHint)
	}

	// stdout/stderr 泵（多路复用帧解复用；容器退出后流自然 EOF）。泵错
	// 误经 channel 回收——在途帧不丢（wait 返回先于管道排空是常态）。
	pumpErr := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(stdout, stderr, attach.Reader)
		pumpErr <- err
	}()
	// stdin 流（恢复面）：EOF 即半关——容器读端见到流结束。
	if stdin != nil {
		go func() {
			_, _ = io.Copy(attach.Conn, stdin)
			_ = attach.CloseWrite()
		}()
	}

	waitRes := p.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case resp := <-waitRes.Result:
		if resp.Error != nil {
			return int(resp.StatusCode), fmt.Errorf("swarm utility: container wait: %s", resp.Error.Message)
		}
		if err := <-pumpErr; err != nil {
			return int(resp.StatusCode), fmt.Errorf("swarm utility: stream pump: %w", err)
		}
		return int(resp.StatusCode), nil
	case werr := <-waitRes.Error:
		return 0, fmt.Errorf("swarm utility: container wait: %w", werr)
	case <-ctx.Done():
		return 0, fmt.Errorf("swarm utility: %w", ctx.Err())
	}
}

// wrapUtilityEnvError 给环境性失败追加可行动提示（存量非 attachable 网络
// 的 flag-day 指引，ADR-0039 决策 4——attachable 语义错由 create/start
// 面如实透传，提示是唯一增量）。
func wrapUtilityEnvError(err error, hint string) error {
	if hint == "" {
		return err
	}
	return fmt.Errorf("%w (%s)", err, hint)
}

// ensureUtilityImage 保证镜像在本地 daemon（缺失即匿名拉取——模板钉版皆
// 公共镜像，架构由 daemon 按本机选择）。
func (p *Provider) ensureUtilityImage(ctx context.Context, image string) error {
	if _, err := p.cli.ImageInspect(ctx, image); err == nil {
		return nil
	}
	pull, err := p.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("swarm utility: pull image %s: %w", image, err)
	}
	defer func() { _ = pull.Close() }()
	for _, err := range pull.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("swarm utility: pull image %s: %w", image, err)
		}
	}
	return nil
}

// safeUtilityMaterialName 钳制材料文件名（basename 形态、禁穿越/绝对路径/
// 盘符——bind 拼接的输入面）。
func safeUtilityMaterialName(name string) error {
	if name == "" || name != filepath.Base(filepath.FromSlash(name)) || strings.ContainsAny(name, `/\:`) {
		return fmt.Errorf("swarm utility: material name %q must be a plain file name", name)
	}
	return nil
}

// utilityNetworkCarriers 把平台网络名解析为载体名（稳定序；全部附着，首
// 个为 NetworkMode——任一项目网都可解析 db-<id>，全部在场消除"恰好选到
// 不可附着旧网"的偶然性）。
func utilityNetworkCarriers(ns capability.NamespaceRef, platformNames []string) []string {
	out := make([]string, 0, len(platformNames))
	for _, n := range platformNames {
		out = append(out, carrierNetworkName(ns, n))
	}
	sort.Strings(out)
	return out
}

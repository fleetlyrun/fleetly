package substrate

// build/image 端口的 moby/client 实现（镜像可见性 + 产物装载 + 平台自管
// buildkitd 容器编排）。第三方类型只在本包内部；错误归一为端口哨兵
//（build.ErrImageNotFound），架构 §2.8。

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/build"
)

// InspectImage 实现 build.ImageSource 端口：返回本机镜像 ID（不可变配置
// 摘要，D9 部署引用形态）；缺失归一为 build.ErrImageNotFound。
func (c *Client) InspectImage(ctx context.Context, ref string) (build.ImageInfo, error) {
	res, err := c.cli.ImageInspect(ctx, ref)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return build.ImageInfo{}, fmt.Errorf("%w: %s", build.ErrImageNotFound, ref)
		}
		return build.ImageInfo{}, fmt.Errorf("substrate: image inspect %s: %w", ref, err)
	}
	if res.ID == "" {
		return build.ImageInfo{}, fmt.Errorf("substrate: image inspect %s: empty image id", ref)
	}
	return build.ImageInfo{ID: res.ID}, nil
}

// LoadImage 实现 build.ImageSource 端口：把 docker-format tar 流装入本机
// daemon（构建产物落本机；响应流必须排空——流未读完装载未完成）。
func (c *Client) LoadImage(ctx context.Context, dockerTar io.Reader) error {
	res, err := c.cli.ImageLoad(ctx, dockerTar, mobyclient.ImageLoadWithQuiet(false))
	if err != nil {
		return fmt.Errorf("substrate: image load: %w", err)
	}
	defer func() { _ = res.Close() }()
	if _, err := io.Copy(io.Discard, res); err != nil {
		return fmt.Errorf("substrate: image load response: %w", err)
	}
	return nil
}

// EnsureImagePresent 实现 build.DaemonManager 端口：确保镜像在本机存在
// （缺失时拉取并排空响应流；幂等）。
func (c *Client) EnsureImagePresent(ctx context.Context, image string) error {
	_, err := c.InspectImage(ctx, image)
	if err == nil {
		return nil
	}
	if !errors.Is(err, build.ErrImageNotFound) {
		return err
	}
	res, err := c.cli.ImagePull(ctx, image, mobyclient.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("substrate: image pull %s: %w", image, err)
	}
	defer func() { _ = res.Close() }()
	if _, err := io.Copy(io.Discard, res); err != nil {
		return fmt.Errorf("substrate: image pull response %s: %w", image, err)
	}
	return nil
}

// EnsureVolumePresent 实现 build.DaemonManager 端口：确保命名卷存在
// （幂等；卷已存在即 no-op）。
func (c *Client) EnsureVolumePresent(ctx context.Context, name string) error {
	if _, err := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); err == nil {
		return nil
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("substrate: volume inspect %s: %w", name, err)
	}
	_, err := c.cli.VolumeCreate(ctx, mobyclient.VolumeCreateOptions{
		Driver: "local",
		Name:   name,
		Labels: map[string]string{"fleetly.managed": "build-cache"},
	})
	if err != nil {
		// 并发创建竞态：已存在即成功。
		if _, ierr := c.cli.VolumeInspect(ctx, name, mobyclient.VolumeInspectOptions{}); ierr == nil {
			return nil
		}
		return fmt.Errorf("substrate: volume create %s: %w", name, err)
	}
	return nil
}

// EnsureContainerRunning 实现 build.DaemonManager 端口：把容器收敛到
// 「存在且运行」（镜像缺失拉取 → 容器缺失创建 → 未运行启动；已在运行
// no-op）。镜像钉版变化不重建既有容器（v0.1 保守：平台不偷偷替换用户宿主
// 上的长驻容器——升级走文档化的人工删除动作，下次 Ensure 按新镜像重建）。
func (c *Client) EnsureContainerRunning(ctx context.Context, spec build.DaemonSpec) error {
	if err := c.EnsureImagePresent(ctx, spec.Image); err != nil {
		return err
	}
	list, err := c.cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: mobyclient.Filters{}.Add("name", "/"+spec.Name),
	})
	if err != nil {
		return fmt.Errorf("substrate: container list %s: %w", spec.Name, err)
	}
	for _, item := range list.Items {
		if !containsName(item.Names, "/"+spec.Name) {
			continue // name 过滤是子串匹配，精确复核
		}
		if string(item.State) == "running" {
			return nil // 已在运行：幂等 no-op
		}
		if _, err := c.cli.ContainerStart(ctx, item.ID, mobyclient.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("substrate: container start %s: %w", spec.Name, err)
		}
		return nil
	}

	// 容器缺失：创建并启动（Spike A §5 选型：privileged buildkitd + cgroup
	// 硬限额；内部工作缓存命名卷挂 /var/lib/buildkit——容器重建不丢层缓存；
	// local cache 的客户端数据根在 fleetlyd 宿主侧，不经容器挂载）。
	hostCfg := &container.HostConfig{
		Privileged: true,
		Resources: container.Resources{
			Memory:   spec.MemoryBytes,
			NanoCPUs: spec.NanoCPUs,
		},
	}
	if spec.CacheVolume != "" {
		hostCfg.Mounts = []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: spec.CacheVolume,
			Target: build.InternalCacheMountPath,
		}}
	}
	res, err := c.cli.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Name: spec.Name,
		Config: &container.Config{
			Image: spec.Image,
			Labels: map[string]string{
				"fleetly.managed": "buildkitd",
			},
		},
		HostConfig: hostCfg,
	})
	if err != nil {
		return fmt.Errorf("substrate: container create %s: %w", spec.Name, err)
	}
	if _, err := c.cli.ContainerStart(ctx, res.ID, mobyclient.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("substrate: container start %s: %w", spec.Name, err)
	}
	return nil
}

// TagImage 给既有镜像打新 tag（镜像身份操作；preflight 实机工作流与镜像
// 管理面使用）。
func (c *Client) TagImage(ctx context.Context, source, target string) error {
	if _, err := c.cli.ImageTag(ctx, mobyclient.ImageTagOptions{Source: source, Target: target}); err != nil {
		return fmt.Errorf("substrate: image tag %s -> %s: %w", source, target, err)
	}
	return nil
}

// RemoveImage 按引用删除镜像（显式管理动作；平台永不自动调用——
// build/imagestore.go 清理策略）。
func (c *Client) RemoveImage(ctx context.Context, ref string) error {
	_, err := c.cli.ImageRemove(ctx, ref, mobyclient.ImageRemoveOptions{})
	if err != nil {
		return fmt.Errorf("substrate: image remove %s: %w", ref, err)
	}
	return nil
}

// containsName 报告容器 names 列表（`/name` 形态）是否包含目标。
func containsName(names []string, target string) bool {
	for _, n := range names {
		if n == target {
			return true
		}
	}
	return false
}

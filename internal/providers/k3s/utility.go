package k3s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// utilityHostDir 是工具容器输入文件的宿主侧暂存目录（ADR-0052 决策 4：
// k3s 无文件级 bind，Input 经宿主目录 hostPath 进容器——Provider 与工具
// Pod 同文件系统 = 控制面节点单机假设，ADR-0039/ADR-0019 同款合法形态；
// 工具 Pod nodeSelector 钉 control-plane 节点保证同机）。
const utilityHostDir = "/var/lib/fleetly/utility"

// controlPlaneSelector 钉 control-plane 节点（k8s 原生 label；单 server
// 试点形态——多 server 的收敛面挂账 ADR-0052 决策 9.7）。
var controlPlaneSelector = map[string]string{"node-role.kubernetes.io/control-plane": ""}

// RunUtility 实现 RuntimeUtility 子面（ADR-0039 备份执行链的 k3s 形态）：
// 一次性工具 Pod（同项目 Namespace → 域内 DNS 达 db-<id>），等待退出，
// 日志流式转发（k8s 日志合流——stdout/stderr 同流，stderr writer 不再
// 分流是 k8s 诚实边界，调用方报文面以退出码承载）。载体结束即删（中断
// 同样清理，零残留）；不打 managed 标记（非 Workload，不进 Watch 观测面）。
func (p *Provider) RunUtility(ctx context.Context, req capability.UtilityRequest, stdout, stderr io.Writer) error {
	nsName := namespaceName(req.Namespace)
	if err := p.ensureNamespace(ctx, nsName); err != nil {
		return fmt.Errorf("k3s utility %s: %w", req.ID, err)
	}
	podName := "fleetly-util-" + sanitizeNamePart(req.ID)
	hostDir := filepath.Join(utilityHostDir, sanitizeNamePart(req.ID))
	cleanup := func() {
		_ = p.cli.CoreV1().Pods(nsName).Delete(context.Background(), podName, metav1.DeleteOptions{})
		_ = os.RemoveAll(hostDir)
	}
	defer cleanup()

	pod, err := p.buildUtilityPod(ctx, req, nsName, podName, hostDir)
	if err != nil {
		return fmt.Errorf("k3s utility %s: %w", req.ID, err)
	}
	if _, err := p.cli.CoreV1().Pods(nsName).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("k3s utility %s: carrier %s already exists (utility IDs never reuse)", req.ID, podName)
		}
		return fmt.Errorf("k3s utility %s: create: %w", req.ID, err)
	}

	// 日志跟随与终态等待并行（日志面即 stdout 面）。
	logsDone := make(chan error, 1)
	go func() {
		logsDone <- p.followUtilityLogs(ctx, nsName, podName, stdout)
	}()
	exitCode, werr := p.waitUtilityExit(ctx, nsName, podName)
	// 终态后等日志流排空（容器日志此刻已终态；有界等待）。
	select {
	case <-logsDone:
	case <-time.After(5 * time.Second):
	}
	if werr != nil {
		return fmt.Errorf("k3s utility %s: %w", req.ID, werr)
	}
	if exitCode != 0 {
		return fmt.Errorf("k3s utility %s: exited with code %d", req.ID, exitCode)
	}
	return nil
}

// buildUtilityPod 组装工具 Pod 声明（材料 Secret + 输入 hostPath + 卷 PVC）。
func (p *Provider) buildUtilityPod(ctx context.Context, req capability.UtilityRequest, nsName, podName, hostDir string) (*corev1.Pod, error) {
	env := make([]corev1.EnvVar, 0, len(req.Env))
	for _, k := range sortedKeys(req.Env) {
		env = append(env, corev1.EnvVar{Name: k, Value: req.Env[k]})
	}
	container := corev1.Container{
		Name:    "utility",
		Image:   req.Image,
		Command: req.Argv,
		Env:     env,
	}
	spec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		NodeSelector:  controlPlaneSelector,
		Containers:    []corev1.Container{container},
	}
	// Secret 材料文件（/run/secrets/<名> 只读）。
	for _, platformName := range sortedKeys(req.SecretFiles) {
		objName := "fleetly-util-mat-" + sanitizeNamePart(platformName)
		if _, err := p.cli.CoreV1().Secrets(nsName).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: objName},
			Data:       map[string][]byte{"value": req.SecretFiles[platformName]},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure utility secret: %w", err)
		}
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "mat-" + sanitizeNamePart(platformName),
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: objName, DefaultMode: ptr(int32(0o444))},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "mat-" + sanitizeNamePart(platformName),
			MountPath: "/run/secrets/" + platformName,
			ReadOnly:  true,
		})
	}
	// 输入文件（恢复流）：宿主暂存目录 hostPath 进容器（写侧 = Provider
	// 本机文件系统；挂点 = Target 的父目录，文件名 = Target 的 base——
	// swarm 文件级 bind 的 k8s 对应物）。
	if req.Input != nil {
		if err := os.MkdirAll(hostDir, 0o750); err != nil {
			return nil, fmt.Errorf("stage input dir: %w", err)
		}
		fileName := filepath.Base(strings.TrimSuffix(req.Input.Target, "/"))
		staged := filepath.Join(hostDir, fileName)
		f, err := os.Create(staged) //nolint:gosec // 路径由平台 ULID 与 dbtemplate 钉定挂点合成，非用户自由输入
		if err != nil {
			return nil, fmt.Errorf("stage input file: %w", err)
		}
		if _, err := io.Copy(f, req.Input.Content); err != nil {
			_ = f.Close() // 失败路径收尾，主错误已是 Copy
			return nil, fmt.Errorf("stage input content: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("stage input close: %w", err) // 暂存不完整会让恢复工具容器读到截断档
		}
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "input",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: hostDir},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "input",
			MountPath: filepath.Dir(req.Input.Target),
			ReadOnly:  true,
		})
	}
	// 平台卷挂载（预置卷恢复形态）。
	if req.Volume != nil {
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "volume",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName(req.Volume.VolumeID),
					ReadOnly:  req.Volume.ReadOnly,
				},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "volume",
			MountPath: req.Volume.Target,
			ReadOnly:  req.Volume.ReadOnly,
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			// 无 managed 标记：非 Workload，不进 Watch 观测面（ADR-0039 契约）。
			Labels: map[string]string{"fleetly.utility": "true"},
		},
		Spec: spec,
	}, nil
}

// followUtilityLogs 跟随工具 Pod 日志到容器终止（stdout 转写）。
func (p *Provider) followUtilityLogs(ctx context.Context, nsName, podName string, stdout io.Writer) error {
	opts := &corev1.PodLogOptions{Follow: true, Container: "utility"}
	stream, err := p.cli.CoreV1().Pods(nsName).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }() //nolint:errcheck // 只读流收尾，错误无处置面（swarm 同款口径）
	r := bufio.NewReader(stream)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if _, werr := stdout.Write(line); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// waitUtilityExit 轮询等待工具 Pod 终态（Succeeded/Failed），返回退出码。
func (p *Provider) waitUtilityExit(ctx context.Context, nsName, podName string) (int, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
			pod, err := p.cli.CoreV1().Pods(nsName).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return -1, err
			}
			switch pod.Status.Phase {
			case corev1.PodSucceeded:
				return 0, nil
			case corev1.PodFailed:
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Terminated != nil {
						return int(cs.State.Terminated.ExitCode), nil //nolint:gosec // 退出码域内
					}
				}
				return 1, nil
			}
		}
	}
}

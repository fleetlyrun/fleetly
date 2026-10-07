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

// utilityNodeSelector 解析工具 Pod 的钉住目标（hostPath 输入通道与
// Provider 同机）：优先原生 control-plane 角色 label 的节点，钉住键用
// 平台自身锚定的 fleetly.node.id——k3s 原生角色 label 的 value 形态有
// 空/非空两种世界，直接按它选节点会调度永不匹配（e2e restore 段
// FailedScheduling 实证）；锚定 label 是平台单源、恒在场。单 server
// 试点：control-plane 角色缺位时回退唯一锚定节点（多 server 收敛面
// 挂账 ADR-0052 决策 9.7）。
func (p *Provider) utilityNodeSelector(ctx context.Context) (map[string]string, error) {
	nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes for utility pinning: %w", err)
	}
	pick := ""
	for i := range nodes.Items {
		n := &nodes.Items[i]
		id := n.Labels[labelNodeID]
		if id == "" {
			continue
		}
		if _, cp := n.Labels["node-role.kubernetes.io/control-plane"]; cp {
			return map[string]string{labelNodeID: id}, nil
		}
		if pick == "" {
			pick = id
		}
	}
	if pick != "" {
		return map[string]string{labelNodeID: pick}, nil
	}
	return nil, fmt.Errorf("no anchored node available for utility pinning")
}

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

	// 日志跟随与终态等待并行（日志面即 stdout 面）。计数写者守卫：超短命
	// 工具 Pod（秒败）终态→cleanup 删 pod 可能快于跟随流的 404 重试窗——
	// 零产出时在 cleanup 前做一次终态直拉兜底（e2e 取证：exit 1 报文恒无
	// 日志尾的末位根因）。
	counting := &countingWriter{w: stdout}
	logsDone := make(chan error, 1)
	go func() {
		logsDone <- p.followUtilityLogs(ctx, nsName, podName, counting)
	}()
	exitCode, werr := p.waitUtilityExit(ctx, nsName, podName)
	// 终态后等日志流排空（容器日志此刻已终态；有界等待）。
	select {
	case <-logsDone:
	case <-time.After(5 * time.Second):
	}
	if counting.n == 0 {
		_ = p.fetchUtilityLogsOnce(ctx, nsName, podName, counting) //nolint:errcheck // 兜底拉取失败维持现状，主错误面在退出码
	}
	if werr != nil {
		return fmt.Errorf("k3s utility %s: %w%s", req.ID, werr, p.utilityPodDiagnosis(ctx, nsName, podName))
	}
	if exitCode != 0 {
		return fmt.Errorf("k3s utility %s: exited with code %d%s", req.ID, exitCode, p.utilityPodDiagnosis(ctx, nsName, podName))
	}
	return nil
}

// countingWriter 计数透传写者（日志兜底直拉的零产出判据）。
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += len(p)
	return c.w.Write(p)
}

// fetchUtilityLogsOnce 终态单次拉取（非 follow）。
func (p *Provider) fetchUtilityLogsOnce(ctx context.Context, nsName, podName string, w io.Writer) error {
	stream, err := p.cli.CoreV1().Pods(nsName).GetLogs(podName, &corev1.PodLogOptions{Container: "utility"}).Stream(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }() //nolint:errcheck // 只读流收尾
	_, err = io.Copy(w, stream)
	return err
}

// utilityPodDiagnosis 拼工具 Pod 的失败诊断面（等待原因/终态/事件尾）——
// 容器未起（挂载/镜像/调度期失败）时无日志产出，报文只带退出码对排障
// 失明（e2e restore 段取证：exit 1 恒无真相）。清理前调用。
func (p *Provider) utilityPodDiagnosis(ctx context.Context, nsName, podName string) string {
	pod, err := p.cli.CoreV1().Pods(nsName).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	var parts []string
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && w.Reason != "" {
			parts = append(parts, fmt.Sprintf("waiting=%s:%s", w.Reason, w.Message))
		}
		if t := cs.State.Terminated; t != nil {
			parts = append(parts, fmt.Sprintf("terminated=%s:exit=%d:%s", t.Reason, t.ExitCode, t.Message))
		}
	}
	events, err := p.cli.CoreV1().Events(nsName).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + podName,
	})
	if err == nil {
		for i := range events.Items {
			ev := &events.Items[i]
			if ev.Type == corev1.EventTypeWarning {
				parts = append(parts, fmt.Sprintf("event=%s:%s", ev.Reason, ev.Message))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, "; ") + "]"
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
	pin, err := p.utilityNodeSelector(ctx)
	if err != nil {
		return nil, fmt.Errorf("k3s utility %s: %w", req.ID, err)
	}
	spec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		NodeSelector:  pin,
		// 工具 Pod 同样不消费 k8s API（SA token 关闭——载体面同款裁决）。
		AutomountServiceAccountToken: ptr(false),
		Containers:                   []corev1.Container{container},
	}
	// Secret 材料文件：projected 卷挂 /run/secrets，value 键投影为文件
	// <平台名>（docker secrets 语义对齐——裸 Secret 卷的两级目录形态会让
	// _FILE env 指到目录即崩，载体面同款实证）。
	if len(req.SecretFiles) > 0 {
		sources := make([]corev1.VolumeProjection, 0, len(req.SecretFiles))
		for _, platformName := range sortedKeys(req.SecretFiles) {
			objName := "fleetly-util-mat-" + sanitizeNamePart(platformName)
			if _, err := p.cli.CoreV1().Secrets(nsName).Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: objName},
				Data:       map[string][]byte{secretDataKey: req.SecretFiles[platformName]},
			}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				return nil, fmt.Errorf("ensure utility secret: %w", err)
			}
			sources = append(sources, corev1.VolumeProjection{
				Secret: &corev1.SecretProjection{
					LocalObjectReference: corev1.LocalObjectReference{Name: objName},
					Items: []corev1.KeyToPath{{
						Key:  secretDataKey,
						Path: platformName,
					}},
				},
			})
		}
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: secretsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources:     sources,
					DefaultMode: ptr(int32(0o444)),
				},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      secretsVolumeName,
			MountPath: "/run/secrets",
			ReadOnly:  true,
		})
	}
	// 输入文件（恢复流）：宿主暂存目录 hostPath 进容器（写侧 = Provider
	// 本机文件系统）。挂载形态 = 父目录挂载（契约路径 BackupInputPath 带
	// 目录段——根级路径的父目录是 "/"，挂载即覆盖容器根、工具二进制消失，
	// e2e restore 段秒败实证；subPath 形态对 hostPath 不达，同批实证）。
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

// followUtilityLogs 跟随工具 Pod 日志到容器终止（stdout 转写）。起流带
// 404/400 重试：Create 返回时 pod 尚未调度/容器未起，立即 GetLogs 即
// NotFound/BadRequest——一次失败就放弃会让短命工具 Pod 的日志恒丢
// （e2e restore 取证：exit 1 报文永远无真相的另一半根因）。
func (p *Provider) followUtilityLogs(ctx context.Context, nsName, podName string, stdout io.Writer) error {
	opts := &corev1.PodLogOptions{Follow: true, Container: "utility"}
	for {
		stream, err := p.cli.CoreV1().Pods(nsName).GetLogs(podName, opts).Stream(ctx)
		if err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsBadRequest(err) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(300 * time.Millisecond):
					continue
				}
			}
			return err
		}
		r := bufio.NewReader(stream)
		for {
			line, rerr := r.ReadBytes('\n')
			if len(line) > 0 {
				if _, werr := stdout.Write(line); werr != nil {
					_ = stream.Close() //nolint:errcheck // 写侧失败即收流，错误以写侧为准
					return werr
				}
			}
			if rerr != nil {
				_ = stream.Close() //nolint:errcheck // 只读流收尾，错误无处置面（swarm 同款口径）
				if rerr == io.EOF {
					return nil
				}
				return rerr
			}
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

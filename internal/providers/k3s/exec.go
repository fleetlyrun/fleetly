package k3s

// exec 子面的 k3s 实现（F3.2/ADR-0049 契约，ADR-0053 决策 1 集中形态）：
// 执行面 = apiserver 原生 exec（remotecommand SPDY——apiserver→kubelet 通道
// 是 k8s 自身基础设施，无 swarm 侧"exec 是节点本地 API"的缺口）；会话路由
// 与节点侧代理循环在 relayagent.go（per-node 回环注册，engine 零改动）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// execPodSelect 是 exec 目标解析的 label 选择器（平台标记 + Workload 锚；
// 双验纵深与 swarm execContainer 同纪律）。
func execPodSelect(workloadID string) string {
	return labels.Set(map[string]string{labelManaged: "true", labelWorkload: workloadID}).String()
}

// ExecTarget 实现 RuntimeExec：label 选择器实时快照（观测缓存不参与决策）
// 取首个 Running pod（名字典序——确定性，与 swarm task ID 字典序同款）。
// Instance = pod 名（agent 侧定位锚）；CarrierNodeID = pod 所在 k8s 节点名
//（平台锚定表反查平台节点 ID——与 Watch 锚定同源）。
func (p *Provider) ExecTarget(ctx context.Context, workloadID string) (capability.ExecTargetInstance, error) {
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: execPodSelect(workloadID)})
	if err != nil {
		return capability.ExecTargetInstance{}, fmt.Errorf("k3s exec target: pod list: %w", err)
	}
	running := make([]string, 0, len(pods.Items))
	byName := make(map[string]*corev1.Pod, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodRunning && pod.Spec.NodeName != "" {
			running = append(running, pod.Name)
			byName[pod.Name] = pod
		}
	}
	if len(running) == 0 {
		return capability.ExecTargetInstance{}, fmt.Errorf("%w: %s", capability.ErrExecNoRunning, workloadID)
	}
	sort.Strings(running)
	pod := byName[running[0]]
	return capability.ExecTargetInstance{Instance: pod.Name, CarrierNodeID: pod.Spec.NodeName}, nil
}

// ExecClusterToken 实现 RuntimeExec：k3s node token 对照（与 Enrollment 同
// 源文件——活 token 等价集群成员权，C3；/v1/relay 与 /v1/platform/binary
// 原生入口的鉴权单源）。
func (p *Provider) ExecClusterToken(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("empty relay credential")
	}
	want, err := readNodeToken()
	if err != nil {
		return fmt.Errorf("k3s exec token: %w", err)
	}
	if token != want {
		return errors.New("relay credential does not match the k3s node token")
	}
	return nil
}

// readNodeToken 读 node token 文件（Enrollment 与 ExecClusterToken 共用）。
func readNodeToken() (string, error) {
	b, err := os.ReadFile(nodeTokenPath)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("node token file is empty")
	}
	return tok, nil
}

// ExecWorkload 实现 RuntimeExec：apiserver SPDY exec。目标解析 =（平台
// 标记 + Workload 锚 + 实例名）三验；目标不在（漂移/退出）返回
// ErrExecTargetGone。TTY 形态 PTY 合流（Stderr 恒空——契约语义）；resize
// 经 TerminalSizeQueue 订阅；stdin 由 remotecommand 读至 EOF。退出码从
// SPDY 错误流还原（"command terminated with exit code N"——client-go 无
// 结构化退出码面，kubectl 同款机制；解析失败如实上抛）。
func (p *Provider) ExecWorkload(ctx context.Context, req capability.ExecWorkloadRequest) (int, error) {
	if req.WorkloadID == "" || req.Instance == "" || len(req.Argv) == 0 {
		return 0, fmt.Errorf("k3s exec: workload, instance and argv are required")
	}
	ns, err := p.execPodNamespace(ctx, req.WorkloadID, req.Instance)
	if err != nil {
		return 0, err
	}
	if p.execFn != nil { // 单测接缝（fake 执行器；生产 nil 走 SPDY）
		return p.execFn(ctx, req, ns, req.Instance)
	}
	return p.spdyExec(ctx, req, ns, req.Instance)
}

// execPodNamespace 解析目标 pod 的 namespace（label 选择器 + 客户端实例名
// 匹配——swarm execContainer 同构；解析不到 = ErrExecTargetGone——受理后
// 实例漂移/容器退出的诚实形态）。
func (p *Provider) execPodNamespace(ctx context.Context, workloadID, instance string) (string, error) {
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: execPodSelect(workloadID),
	})
	if err != nil {
		return "", fmt.Errorf("k3s exec: pod list: %w", err)
	}
	for i := range pods.Items {
		if pods.Items[i].Name == instance {
			return pods.Items[i].Namespace, nil
		}
	}
	return "", fmt.Errorf("%w: workload %s instance %s", capability.ErrExecTargetGone, workloadID, instance)
}

// spdyExec 经 apiserver 对目标 pod 执行（remotecommand SPDY 通道；exec 是
// apiserver 子资源——client-go 无 typed 方法，RESTRequest 手拼同 kubectl）。
func (p *Provider) spdyExec(ctx context.Context, req capability.ExecWorkloadRequest, ns, name string) (int, error) {
	execURL := p.cli.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(name).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: req.Argv,
			Stdin:   true,
			Stdout:  true,
			Stderr:  !req.TTY,
			TTY:     req.TTY,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(p.restCfg, "POST", execURL.URL())
	if err != nil {
		return 0, fmt.Errorf("k3s exec: executor: %w", err)
	}
	opts := remotecommand.StreamOptions{
		Stdin:  req.Stdin,
		Stdout: req.Stdout,
		Tty:    req.TTY,
	}
	if req.TTY {
		opts.Stderr = nil // PTY 合流语义（契约：TTY 形态只写 Stdout）
	} else {
		opts.Stderr = req.Stderr
	}
	if req.Resize != nil {
		// resize 订阅（nil = 无终端关注；通道关闭即 Next 返回 nil 停订阅）。
		opts.TerminalSizeQueue = &resizeQueue{ch: req.Resize}
	}
	err = exec.StreamWithContext(ctx, opts)
	if err == nil {
		return 0, nil
	}
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		var code int
		if _, perr := fmt.Sscanf(statusErr.Error(), "command terminated with exit code %d", &code); perr == nil {
			return code, nil
		}
	}
	return 0, fmt.Errorf("k3s exec: %w", err)
}

// resizeQueue 把 Resize 订阅适配为 remotecommand 的 TerminalSizeQueue
//（通道关闭即 Next 返回 nil——订阅停止）。
type resizeQueue struct {
	ch <-chan capability.ExecSize
}

func (q *resizeQueue) Next() *remotecommand.TerminalSize {
	sz, ok := <-q.ch
	if !ok {
		return nil
	}
	return &remotecommand.TerminalSize{Width: sz.Cols, Height: sz.Rows}
}

// relayFrameCap 是单帧输出分块上限（与 swarm 侧 agentFrameWriter 对齐——
// manager 侧读上限同值）。
const relayFrameCap = 32 * 1024

package k3s

// Exec 子面单测（ADR-0053 决策 1）：目标解析（label 快照/确定性/哨兵）、
// 三验与 ErrExecTargetGone、执行器接缝的 argv/stdin/resize/退出码管道、
// 会话多路复用（open→ack→exit 帧序）。真 SPDY 链路在 e2e。

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// execPod 是 exec 目标解析的 pod 夹具。
func execPod(ns, name, node string, phase corev1.PodPhase) runtime.Object {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    map[string]string{labelManaged: "true", labelWorkload: "wl-1"},
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
}

// TestExecTarget：Running pod 解析（多副本名字典序确定性）+ Pending 不计
// + 无在跑实例哨兵。
func TestExecTarget(t *testing.T) {
	p, _ := newFakeProvider(
		execPod("fleetly-shop", "web-b", "node-2", corev1.PodRunning),
		execPod("fleetly-shop", "web-a", "node-1", corev1.PodRunning),
		execPod("fleetly-shop", "web-c", "node-1", corev1.PodPending),
	)
	got, err := p.ExecTarget(context.Background(), "wl-1")
	require.NoError(t, err)
	assert.Equal(t, "web-a", got.Instance, "multiple running pods must pick lexicographically first")
	assert.Equal(t, "node-1", got.CarrierNodeID)
}

func TestExecTargetNoRunning(t *testing.T) {
	p, _ := newFakeProvider(execPod("fleetly-shop", "web-a", "node-1", corev1.PodPending))
	_, err := p.ExecTarget(context.Background(), "wl-1")
	require.ErrorIs(t, err, capability.ErrExecNoRunning)
}

func TestExecTargetWrongWorkload(t *testing.T) {
	p, _ := newFakeProvider() // 无任何 pod
	_, err := p.ExecTarget(context.Background(), "wl-1")
	require.ErrorIs(t, err, capability.ErrExecNoRunning)
}

// TestExecWorkloadTargetGone：实例名不在（漂移/退出）→ ErrExecTargetGone。
func TestExecWorkloadTargetGone(t *testing.T) {
	p, _ := newFakeProvider(execPod("fleetly-shop", "web-a", "node-1", corev1.PodRunning))
	_, err := p.ExecWorkload(context.Background(), capability.ExecWorkloadRequest{
		WorkloadID: "wl-1", Instance: "web-gone", Argv: []string{"true"},
	})
	require.ErrorIs(t, err, capability.ErrExecTargetGone)
}

// TestExecWorkloadRequiresFields：三必填校验。
func TestExecWorkloadRequiresFields(t *testing.T) {
	p, _ := newFakeProvider()
	_, err := p.ExecWorkload(context.Background(), capability.ExecWorkloadRequest{
		WorkloadID: "wl-1", Instance: "web-a",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argv")
}

// TestExecWorkloadSeam：接缝直通——argv/namespace/退出码/stdin-EOF/resize
// 的管道完整性（fake clientset 承载目标解析，fake 执行器承载执行面）。
func TestExecWorkloadSeam(t *testing.T) {
	p, cli := newFakeProvider(execPod("fleetly-shop", "web-a", "node-1", corev1.PodRunning))
	var gotNS, gotName string
	var gotReq capability.ExecWorkloadRequest
	var gotStdin []byte
	p.execFn = func(ctx context.Context, req capability.ExecWorkloadRequest, ns, name string) (int, error) {
		gotNS, gotName, gotReq = ns, name, req
		buf, _ := io.ReadAll(req.Stdin)
		gotStdin = buf
		if _, err := req.Stdout.Write([]byte("hello\n")); err != nil {
			return 0, err
		}
		sz, ok := <-req.Resize
		assert.True(t, ok, "resize subscription must deliver")
		assert.Equal(t, uint16(120), sz.Cols)
		return 42, nil
	}
	var out bytes.Buffer
	resize := make(chan capability.ExecSize, 1)
	resize <- capability.ExecSize{Cols: 120, Rows: 40}
	code, err := p.ExecWorkload(context.Background(), capability.ExecWorkloadRequest{
		WorkloadID: "wl-1", Instance: "web-a", Argv: []string{"/bin/sh", "-c", "echo hi"},
		Stdin:  bytes.NewReader([]byte("stdin-bytes")),
		Stdout: &out,
		Stderr: io.Discard,
		Resize: resize,
	})
	require.NoError(t, err)
	assert.Equal(t, 42, code)
	assert.Equal(t, "fleetly-shop", gotNS)
	assert.Equal(t, "web-a", gotName)
	assert.Equal(t, []string{"/bin/sh", "-c", "echo hi"}, gotReq.Argv)
	assert.Equal(t, "stdin-bytes", string(gotStdin))
	assert.Equal(t, "hello\n", out.String())
	_ = cli
}

// TestAgentSessionFrameFlow：会话多路复用的帧序（open → ack → stdin →
// exit；执行失败 → error 帧）。不经 WS——send 通道直读（dial/hello 的线
// 上形态在 e2e）。
func TestAgentSessionFrameFlow(t *testing.T) {
	p, _ := newFakeProvider(execPod("fleetly-shop", "web-a", "node-1", corev1.PodRunning))
	p.execFn = func(ctx context.Context, req capability.ExecWorkloadRequest, ns, name string) (int, error) {
		buf, _ := io.ReadAll(req.Stdin)
		if string(buf) != "ping" {
			return 0, assert.AnError
		}
		_, _ = req.Stdout.Write([]byte("pong"))
		return 7, nil
	}
	a := &agentConn{
		provider: p,
		send:     make(chan []byte, 16),
		sessions: map[string]*agentSession{},
	}
	defer a.shutdown()
	ctx := context.Background()
	sid := "01H" + strings.Repeat("Z", 22) + "1" // ULID 定长 26（手写字面量差一位即帧界错位——实锤过）

	// open → 会话起。
	a.dispatch(ctx, capability.AgentFrame{
		Kind:      capability.AgentFrameOpen,
		SessionID: sid,
		Payload: capability.EncodeAgentJSON(capability.AgentSessionOpen{
			SessionID: sid, WorkloadID: "wl-1", Instance: "web-a",
			Argv: []string{"true"}, TTY: false,
		}),
	})
	// stdin + EOF → 执行器收到 "ping"。
	a.dispatch(ctx, capability.AgentFrame{Kind: capability.AgentFrameStdin, SessionID: sid, Payload: []byte("ping")})
	a.dispatch(ctx, capability.AgentFrame{Kind: capability.AgentFrameStdinEOF, SessionID: sid})

	// 收帧：ack → stdout("pong") → exit(7)。
	deadline := time.After(5 * time.Second)
	var frames []capability.AgentFrame
	wantExit := false
	for !wantExit {
		select {
		case b := <-a.send:
			f, err := capability.ParseAgentFrame(b)
			require.NoError(t, err)
			frames = append(frames, f)
			if f.Kind == capability.AgentFrameExit {
				wantExit = true
			}
		case <-deadline:
			t.Fatal("session did not reach exit frame")
		}
	}
	require.GreaterOrEqual(t, len(frames), 3)
	assert.Equal(t, capability.AgentFrameAck, frames[0].Kind)
	ack, err := capability.DecodeAgentJSON[capability.AgentSessionAck](frames[0].Payload)
	require.NoError(t, err)
	assert.Equal(t, "web-a", ack.Instance)
	var sawPong bool
	for _, f := range frames {
		if f.Kind == capability.AgentFrameStdout {
			assert.Equal(t, "pong", string(f.Payload))
			sawPong = true
		}
	}
	assert.True(t, sawPong, "stdout frame must carry executor output")
	exit, err := capability.DecodeAgentJSON[capability.AgentSessionExit](frames[len(frames)-1].Payload)
	require.NoError(t, err)
	assert.Equal(t, 7, exit.Code)
}

// TestAgentSessionErrorFrame：执行失败 → error 帧（exit 缺席即会话失败）。
func TestAgentSessionErrorFrame(t *testing.T) {
	p, _ := newFakeProvider(execPod("fleetly-shop", "web-a", "node-1", corev1.PodRunning))
	p.execFn = func(ctx context.Context, req capability.ExecWorkloadRequest, ns, name string) (int, error) {
		return 0, assert.AnError
	}
	a := &agentConn{provider: p, send: make(chan []byte, 16), sessions: map[string]*agentSession{}}
	defer a.shutdown()
	ctx := context.Background()
	sid := "01H" + strings.Repeat("Z", 22) + "2"
	a.dispatch(ctx, capability.AgentFrame{
		Kind:      capability.AgentFrameOpen,
		SessionID: sid,
		Payload: capability.EncodeAgentJSON(capability.AgentSessionOpen{
			SessionID: sid, WorkloadID: "wl-1", Instance: "web-a", Argv: []string{"true"},
		}),
	})
	select {
	case b := <-a.send:
		f, err := capability.ParseAgentFrame(b)
		require.NoError(t, err)
		require.Equal(t, capability.AgentFrameAck, f.Kind) // 首帧 ack
	case <-time.After(5 * time.Second):
		t.Fatal("no ack frame")
	}
	select {
	case b := <-a.send:
		f, err := capability.ParseAgentFrame(b)
		require.NoError(t, err)
		require.Equal(t, capability.AgentFrameError, f.Kind)
		pl, err := capability.DecodeAgentJSON[capability.AgentSessionError](f.Payload)
		require.NoError(t, err)
		assert.NotEmpty(t, pl.Message)
	case <-time.After(5 * time.Second):
		t.Fatal("no error frame")
	}
}

// TestResizeQueueNext：订阅通道关闭即 Next 停（nil）。
func TestResizeQueueNext(t *testing.T) {
	ch := make(chan capability.ExecSize, 1)
	ch <- capability.ExecSize{Cols: 10, Rows: 5}
	q := &resizeQueue{ch: ch}
	sz := q.Next()
	require.NotNil(t, sz)
	assert.Equal(t, uint16(10), sz.Width)
	close(ch)
	assert.Nil(t, q.Next())
}

// TestAdvertiseServerURL：控制面节点 InternalIP 解析（端口沿用 kubeconfig
// server）+ 角色优先 + 无节点回退 kubeconfig 原文（挂账 5 的 worker 可达
// 地址面，ADR-0053 决策 5）。
func TestAdvertiseServerURL(t *testing.T) {
	node := func(name, ip string, cp bool) runtime.Object {
		labels := map[string]string{}
		if cp {
			labels["node-role.kubernetes.io/control-plane"] = ""
		}
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: ip},
			}},
		}
	}
	p, _ := newFakeProvider(node("k3s-e2e-0", "10.0.0.3", true), node("k3s-e2e-1", "10.0.0.4", false))
	assert.Equal(t, "https://10.0.0.3:6443", p.advertiseServerURL(context.Background()))

	// 无 control-plane 角色:回退首个有 InternalIP 的节点。
	p2, _ := newFakeProvider(node("a", "10.0.0.9", false))
	assert.Equal(t, "https://10.0.0.9:6443", p2.advertiseServerURL(context.Background()))

	// 无节点:回退 kubeconfig 原文(单节点同机形态)。
	p3, _ := newFakeProvider()
	assert.Equal(t, "https://k3s-lab:6443", p3.advertiseServerURL(context.Background()))
}

// TestEnrollmentCommandShape：join 命令形态锚(advertise server + token;
// AgentCommand 恒空 = 集中形态无节点侧代理面)。
func TestEnrollmentCommandShape(t *testing.T) {
	tokFile := t.TempDir() + "/node-token"
	require.NoError(t, os.WriteFile(tokFile, []byte("K10abc::worker:secret\n"), 0o600))
	p, _ := newFakeProvider(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "cp", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
		Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
			{Type: corev1.NodeInternalIP, Address: "10.0.0.3"},
		}},
	})
	p.nodeTokenPath = tokFile
	kit, err := p.Enrollment(context.Background(), false, capability.EnrollmentOptions{})
	require.NoError(t, err)
	assert.Contains(t, kit.Command, "k3s agent --server https://10.0.0.3:6443 --token K10abc::worker:secret")
	assert.Empty(t, kit.AgentCommand, "central exec form carries no node-side agent face")

	_, err = p.Enrollment(context.Background(), true, capability.EnrollmentOptions{})
	require.Error(t, err, "rotate must fail honestly (k3s server-side operation)")
}

// TestExecClusterToken：node token 对照面（同源文件;不匹配即拒）。
func TestExecClusterToken(t *testing.T) {
	tokFile := t.TempDir() + "/node-token"
	require.NoError(t, os.WriteFile(tokFile, []byte("K10abc\n"), 0o600))
	p, _ := newFakeProvider()
	p.nodeTokenPath = tokFile
	require.NoError(t, p.ExecClusterToken(context.Background(), "K10abc"))
	require.Error(t, p.ExecClusterToken(context.Background(), "K10other"))
	require.Error(t, p.ExecClusterToken(context.Background(), ""))
}

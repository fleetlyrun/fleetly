package main

// S17-D3 机制测试：一元 RPC 缺省 deadline（拦截器层——fake conn 挂起 +
// 短父 deadline → 限时失败 + 超时引导渲染）、流式/等待动词的取消干净退
// 出（exit 0、不渲染错误）、Unavailable 引导渲染。

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lynx-go/commands"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUnaryTimeoutInterceptor 缺省 deadline 拦截器：每次一元调用挂 30s
// deadline；父 ctx 自带更早 deadline 时不放宽（min 语义）——fake conn 挂
// 起（invoker 永不主动返回，即 daemon 假死的调用面投影）在父 deadline
// 处限时失败，错误为 DeadlineExceeded 且渲染带可行动提示。
func TestUnaryTimeoutInterceptor(t *testing.T) {
	// 常规调用：deadline 已挂载。
	var sawDeadline bool
	probe := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		_, sawDeadline = ctx.Deadline()
		return nil
	}
	if err := defaultUnaryTimeout(context.Background(), "/x", nil, nil, nil, probe); err != nil || !sawDeadline {
		t.Fatalf("缺省 deadline 未挂载: err=%v saw=%v", err, sawDeadline)
	}

	// 挂起调用 + 更早的父 deadline → 限时失败（不等待 30s 缺省值）。
	parent, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	hang := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		<-ctx.Done()
		return status.FromContextError(ctx.Err()).Err() // gRPC 面同型投影
	}
	start := time.Now()
	err := defaultUnaryTimeout(parent, "/x", nil, nil, nil, hang)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("挂起调用未按父 deadline 限时失败: %v", elapsed)
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("挂起调用错误非 DeadlineExceeded: %v", err)
	}
	for _, want := range []string{"请求超时", "--timeout"} {
		if !strings.Contains(renderCLIError(err), want) {
			t.Errorf("DeadlineExceeded 渲染缺 %q:\n%s", want, renderCLIError(err))
		}
	}
}

// TestIsCleanCancel 干净取消判定的边界：Canceled 本尊与 gRPC 投影（根 ctx
// 已取消）为干净；根 ctx 未取消的服务端 Canceled、超时、无错误均不干净。
func TestIsCleanCancel(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !isCleanCancel(canceled, context.Canceled) {
		t.Error("Canceled 本尊应判干净")
	}
	if !isCleanCancel(canceled, status.FromContextError(context.Canceled).Err()) {
		t.Error("gRPC 投影（codes.Canceled）应判干净")
	}
	if isCleanCancel(context.Background(), status.Error(codes.Canceled, "server canceled")) {
		t.Error("根 ctx 未取消（服务端取消）不得判干净")
	}
	if isCleanCancel(canceled, context.DeadlineExceeded) {
		t.Error("DeadlineExceeded 不得判干净")
	}
	if isCleanCancel(canceled, nil) {
		t.Error("无错误不得判干净")
	}
}

// TestStreamCancelCleanExit 流式动词（logs follow / events watch）在 ctx
// 取消（Ctrl-C 的信号投影）时干净退出：exit 0、stderr 无错误渲染。预先
// 取消的 ctx 使首个流式 RPC 立即失败——无需真实服务面，不起子进程。
func TestStreamCancelCleanExit(t *testing.T) {
	for _, args := range [][]string{
		{"logs", "follow", "my-api"},
		{"events", "watch"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
		code := newApp().Run(ctx, env, args)
		if code != 0 {
			t.Fatalf("%v: code=%d, 期望 0（干净退出）\nstderr=%s", args, code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("%v: 干净退出不得渲染错误: %q", args, stderr.String())
		}
	}
}

// TestWaitVerbCancelCleanExit 轮询等待动词（rollback / deployments cancel
// ——deploy/build 同骨架）在 ctx 取消时同样干净退出（exit 0）。
func TestWaitVerbCancelCleanExit(t *testing.T) {
	for _, args := range [][]string{
		{"rollback", "my-api"},
		{"deployments", "cancel", "01TEST"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
		code := newApp().Run(ctx, env, args)
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("%v: code=%d stderr=%q, 期望 0/空（干净退出）", args, code, stderr.String())
		}
	}
}

// TestTokenFlagHelpNoEnvEcho H1 回归：--token 的 flag 默认值必须为空串
// ——std flag 的 -h/help 会把非空默认值（PrintDefaults 的 "(default …)"）
// 明文打进 stdout，帮助输出常被贴进工单/CI 日志/AI 会话。设 FLEETLY_
// TOKEN 后打印动词帮助（动词级 -h 与顶层 help <verb> 两个帮助面），断言
// 输出不含 token 本体——env 回落挪到消费点 dial()（golden 测试的 env
// 链路覆盖行为等价）。同时钉住帮助文案口径：指路 env 与 bootstrap-token
// 文件（B5 后 token 不进日志，旧"首启日志"指引自 B1 起废除）。
func TestTokenFlagHelpNoEnvEcho(t *testing.T) {
	const secret = "flt_h1_no_env_echo_regression"
	t.Setenv("FLEETLY_TOKEN", secret)
	// 帮助面 ①：动词级 -h（apps list 带 conn flags）。
	code, out, _ := runCLI(t, "apps", "list", "-h")
	if code != 0 {
		t.Fatalf("apps list -h: code=%d, 期望 0（-h 是帮助退出）", code)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("动词级 -h 回显了 FLEETLY_TOKEN 本体:\n%s", out)
	}
	// 帮助面 ②：顶层 help <verb>（deploy 带 conn flags）。
	code, out, _ = runCLI(t, "help", "deploy")
	if code != 0 {
		t.Fatalf("help deploy: code=%d, 期望 0", code)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("help <verb> 回显了 FLEETLY_TOKEN 本体:\n%s", out)
	}
	// 文案口径：指路 env 与 bootstrap-token 文件，不出现已废除的日志通道。
	for _, want := range []string{"FLEETLY_TOKEN", "bootstrap-token"} {
		if !strings.Contains(out, want) {
			t.Errorf("token 帮助文案缺 %q 指引:\n%s", want, out)
		}
	}
	if strings.Contains(out, "first-start log") || strings.Contains(out, "首启日志") {
		t.Errorf("token 帮助文案仍指向已废除的日志通道:\n%s", out)
	}
}

// TestRenderUnavailableHint Unavailable（fleetlyd 未起/addr 错）的引导
// 渲染：与 401 hint 同风格的可行动提示（地址/环境变量/守护进程状态）。
func TestRenderUnavailableHint(t *testing.T) {
	got := renderCLIError(status.Error(codes.Unavailable, "connection error: dial 127.0.0.1:8421: connect: connection refused"))
	for _, want := range []string{"fleetlyd 不可达", "--addr", "127.0.0.1:8421", "FLEETLY_ADDR", "systemctl status fleetlyd"} {
		if !strings.Contains(got, want) {
			t.Errorf("Unavailable 渲染缺 %q:\n%s", want, got)
		}
	}
}

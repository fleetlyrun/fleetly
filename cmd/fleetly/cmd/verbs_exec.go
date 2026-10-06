package cmd

// exec 子面动词（F3.2，ADR-0049）：`fleetly exec`（one-shot 命令，退出码
// 透传——机器编排锚）与 `fleetly shell`（交互 TTY，缺省 /bin/sh）。双形态
// 纪律照旧：--json 输出 NDJSON 帧（stdout/stderr 帧 base64、exit 帧带码）。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lynx-go/commands"
	"golang.org/x/term"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
)

// exitCodeError 携带载体进程退出码（exit 动词退出码透传面）。
type exitCodeError struct{ code int32 }

func (e exitCodeError) Error() string { return fmt.Sprintf("process exited with code %d", e.code) }

// parseExecTarget 解析 <app-id>/<process> 目标形态。
func parseExecTarget(v string) (appID, process string, err error) {
	parts := strings.SplitN(v, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		return "", "", fmt.Errorf("invalid target %q: expected <app-id>/<process>", v)
	}
	return parts[0], parts[1], nil
}

// splitExecArgs 拆位置参数：首参是 <app-id>/<process> 目标，其余是 argv
// （可选 "--" 分隔符——flag 包停在首参，分隔符原样保留在此）。
func splitExecArgs(args []string) (string, []string, error) {
	if len(args) == 0 || args[0] == "" || args[0] == "--" || strings.HasPrefix(args[0], "-") {
		return "", nil, fmt.Errorf("usage: <app-id>/<process> [-- command args...]")
	}
	target, argv := args[0], args[1:]
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	return target, argv, nil
}

// execFrameJSONLine 是 --json 形态的帧行（type/data 或 type/code）。
type execFrameJSONLine struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Code *int32 `json:"code,omitempty"`
}

// runExecSession 驱动一次会话：受理 → attach →（stdin 转发/CloseSend 或
// TTY 原始模式 + resize 传播）→ 帧收口。返回退出码透传错误。
func runExecSession(ctx context.Context, env *commands.Environment, c runtimev1.ExecServiceClient, target string, argv []string, tty, jsonOut bool) error {
	appID, process, err := parseExecTarget(target)
	if err != nil {
		return err
	}
	resp, err := c.CreateExecSession(ctx, &runtimev1.CreateExecSessionRequest{
		AppId: appID, Process: process, Command: argv, Tty: tty,
	})
	if err != nil {
		return err
	}
	sess := resp.GetSession()
	if !jsonOut {
		_, _ = fmt.Fprintf(env.Stderr, "# session %s on %s (instance %s, node %s)\n",
			sess.GetId(), target, sess.GetInstance(), sess.GetNodeId())
	}
	stream, err := c.StreamExecSession(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&runtimev1.StreamExecSessionRequest{
		Frame: &runtimev1.StreamExecSessionRequest_AttachSessionId{AttachSessionId: sess.GetId()},
	}); err != nil {
		return err
	}

	// 上行泵：stdin 转发（TTY 形态含原始模式与 resize 传播）。
	upErr := make(chan error, 1)
	go func() {
		upErr <- pumpExecUpstream(stream, tty)
	}()

	// 下行泵：帧收口（meta 首帧后 stdout/stderr/exit/error）。
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch f := frame.GetFrame().(type) {
		case *runtimev1.StreamExecSessionResponse_Meta:
			if jsonOut {
				writeExecJSONLine(env, execFrameJSONLine{Type: "meta", Data: f.Meta.GetInstance() + "@" + f.Meta.GetNodeId()})
			}
		case *runtimev1.StreamExecSessionResponse_Stdout:
			if jsonOut {
				writeExecJSONLine(env, execFrameJSONLine{Type: "stdout", Data: base64.StdEncoding.EncodeToString(f.Stdout)})
			} else {
				_, _ = env.Stdout.Write(f.Stdout)
			}
		case *runtimev1.StreamExecSessionResponse_Stderr:
			if jsonOut {
				writeExecJSONLine(env, execFrameJSONLine{Type: "stderr", Data: base64.StdEncoding.EncodeToString(f.Stderr)})
			} else {
				_, _ = env.Stderr.Write(f.Stderr)
			}
		case *runtimev1.StreamExecSessionResponse_Exit:
			if jsonOut {
				code := f.Exit.GetCode()
				writeExecJSONLine(env, execFrameJSONLine{Type: "exit", Code: &code})
			}
			// 收口上行（客户端 EOF 后 stream.Send 已无害）。退出码 0 = 成功
			// 路径（无错误文案；非 0 透传给 exitCodeFor）。
			if code := f.Exit.GetCode(); code != 0 {
				return exitCodeError{code: code}
			}
			return nil
		case *runtimev1.StreamExecSessionResponse_Error:
			if jsonOut {
				writeExecJSONLine(env, execFrameJSONLine{Type: "error", Data: f.Error.GetCode() + ": " + f.Error.GetMessage()})
			} else {
				_, _ = fmt.Fprintf(env.Stderr, "exec: %s: %s\n", f.Error.GetCode(), f.Error.GetMessage())
			}
			return exitCodeError{code: 1}
		}
	}
}

// pumpExecUpstream 转发本机 stdin 到会话流；EOF 即 CloseSend（服务端转
// StdinEOF——`fleetly exec app -- cat` 的管道形态依赖）。TTY 形态进入
// 原始模式并传播 SIGWINCH resize。
func pumpExecUpstream(stream interface {
	Send(*runtimev1.StreamExecSessionRequest) error
	CloseSend() error
}, tty bool) error {
	stdin := os.Stdin
	if tty && term.IsTerminal(int(stdin.Fd())) {
		oldState, err := term.MakeRaw(int(stdin.Fd()))
		if err == nil {
			defer func() { _ = term.Restore(int(stdin.Fd()), oldState) }()
			// resize 传播（SIGWINCH → 当前终端尺寸）。
			winch := make(chan os.Signal, 1)
			signal.Notify(winch, syscall.SIGWINCH)
			defer signal.Stop(winch)
			go func() {
				for range winch {
					if w, h, err := term.GetSize(int(stdin.Fd())); err == nil {
						_ = stream.Send(&runtimev1.StreamExecSessionRequest{
							Frame: &runtimev1.StreamExecSessionRequest_Resize{
								Resize: &runtimev1.ExecResize{Cols: dimToI32(w), Rows: dimToI32(h)},
							},
						})
					}
				}
			}()
			if w, h, err := term.GetSize(int(stdin.Fd())); err == nil {
				_ = stream.Send(&runtimev1.StreamExecSessionRequest{
					Frame: &runtimev1.StreamExecSessionRequest_Resize{
						Resize: &runtimev1.ExecResize{Cols: dimToI32(w), Rows: dimToI32(h)},
					},
				})
			}
		}
	}
	buf := make([]byte, 4096)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if serr := stream.Send(&runtimev1.StreamExecSessionRequest{
				Frame: &runtimev1.StreamExecSessionRequest_Stdin{Stdin: data},
			}); serr != nil {
				return serr
			}
		}
		if err != nil {
			_ = stream.CloseSend()
			return nil
		}
	}
}

func writeExecJSONLine(env *commands.Environment, line execFrameJSONLine) {
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(env.Stdout, string(b))
}

// newExecVerb 构造 exec（one-shot 命令；退出码透传）。
func newExecVerb() commands.Command {
	const name = "exec"
	return &flaggedVerb{
		name:     name,
		synopsis: "Run a one-shot command inside a running process (exit code is passed through)",
		usage:    "exec <app-id>/<process> -- <command> [args...]",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			target, argv, err := splitExecArgs(args)
			if err != nil {
				return usageErr(name, err.Error())
			}
			if len(argv) == 0 {
				return usageErr(name, "a command is required (non-tty sessions have no default)")
			}
			ctx, cancel, c, derr := dialFromEnv(ctx, noDeadline())
			if derr != nil {
				return derr
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			return runExecSession(ctx, env, c.Exec, target, argv, false, jsonOut)
		},
	}
}

// newShellVerb 构造 shell（交互 TTY；缺省 /bin/sh）。
func newShellVerb() commands.Command {
	const name = "shell"
	return &flaggedVerb{
		name:     name,
		synopsis: "Open an interactive shell inside a running process (default command /bin/sh)",
		usage:    "shell <app-id>/<process> [-- <command> [args...]]",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			target, argv, err := splitExecArgs(args)
			if err != nil {
				return usageErr(name, err.Error())
			}
			if len(argv) == 0 {
				argv = []string{"/bin/sh"}
			}
			ctx, cancel, c, derr := dialFromEnv(ctx, noDeadline())
			if derr != nil {
				return derr
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			return runExecSession(ctx, env, c.Exec, target, argv, true, jsonOut)
		},
	}
}

// dimToI32 钳制终端维度（有界值域 < 2^15；G115 转换收口）。
func dimToI32(v int) int32 {
	if v < 0 {
		return 0
	}
	if v > 1<<30 {
		return 1 << 30
	}
	return int32(v)
}

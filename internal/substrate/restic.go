package substrate

// restic 一次性容器执行器（E3-3，D-S3-3）：statebackup.ResticRunner 端口
// 的 moby/client 实现——钉版镜像 create→start→wait→logs→rm，容器清理在
// finally（defer）兜底；备份目录只读 bind；env 注入 secret 值不落本包
// 任何日志/错误文本。镜像钉定形态由载荷（ResticSpec.Image）携带，本包
// 不持镜像策略——钉版单一事实源在 internal/statebackup.DefaultResticImage
// （与 docs/runbooks/image-prepull.md 台账行双锚）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/statebackup"
)

// 编译期断言：Client 隐式实现 statebackup.ResticRunner 端口（第三方类型
// 不出包的结构性证明，与 engine/build 端口同型）。
var _ statebackup.ResticRunner = (*Client)(nil)

// resticStderrTailBytes 是失败错误里携带的 stderr 尾部上限（restic 把
// 进度/错误写 stderr，错误摘要取尾部即可定位；4KiB 兼顾归因与台账卫生
// ——截断与 secret 擦除的最终防线在 statebackup 落笔点）。
const resticStderrTailBytes = 4096

// resticMaxLogFrame 是多路复用流的单帧长度上限（防损坏流把内存打爆；
// restic 输出帧远小于此——16MiB 是防御面不是工作面）。
const resticMaxLogFrame = 16 << 20

// RunRestic 实现 statebackup.ResticRunner 端口：以钉版镜像一次性执行
// restic 命令。生命周期 = EnsureImagePresent（缺失时拉取，流式进度不设
// per-call 预算）→ ContainerCreate（只读 bind + env）→ ContainerStart →
// ContainerWait（NextExit；随调用方 ctx 取消——上传预算超时会在这里
// 切断）→ 读日志（退出后仍可读）→ defer ContainerRemove(Force) 清理。
// 返回值 output 是 stdout 全文（restic --json 的结构化消息来源）；err
// 非 nil 时携带 stderr 尾部摘要（不含 env 值——本包不拼接任何环境变量
// 进错误文本）。
func (c *Client) RunRestic(ctx context.Context, spec statebackup.ResticSpec) (string, error) {
	if err := c.EnsureImagePresent(ctx, spec.Image); err != nil {
		return "", fmt.Errorf("substrate: restic image: %w", err)
	}
	res, err := c.cli.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Config: &container.Config{
			Image: spec.Image,
			Cmd:   spec.Args,
			Env:   flattenEnv(spec.Env),
			Labels: map[string]string{
				// 平台受管标记（运维辨识锚：进程崩溃/超时残留的一次性
				// 容器可按此 label 清点——defer rm 兜底失败的显性化面）。
				"fleetly.managed": "restic-upload",
			},
			User: "0", // restic 只读消费 bind 挂载，root 缺省形态即可（与镜像缺省一致，显式写出防镜像变更漂移）
		},
		HostConfig: &container.HostConfig{
			// 只读 bind：上传轨对本地备份目录零写权（本地信任闭环不被
			// 远端路径意外改写——设计 §2.3「备份核一字不动」的容器面）。
			Mounts: []mount.Mount{{
				Type:     mount.TypeBind,
				Source:   spec.HostMountDir,
				Target:   spec.ContainerMountDir,
				ReadOnly: true,
			}},
			// 网络缺省 = bridge（external 模式出网即可；rustfs 模式由
			// E3-5 接线平台内网形态——设计 §2.3）。
			AutoRemove: false, // 日志需在退出后读取；清理走下方 defer
		},
	})
	if err != nil {
		return "", fmt.Errorf("substrate: restic container create: %w", err)
	}
	id := res.ID
	// finally 清理：成功/失败/超时/panic 全路径删容器（Force 兼顾仍在
	// 运行的超时态）。清理失败只进错误注记——一次性容器残留不掩盖主错。
	defer func() {
		rctx, rcancel := withCallTimeout(context.WithoutCancel(ctx))
		defer rcancel()
		// D2：Remove 走 per-call 预算（30s）；失败静默——残留容器带
		// resticContainerLabel 可辨识，不遮蔽主流程错误/结果。
		_, _ = c.cli.ContainerRemove(rctx, id, mobyclient.ContainerRemoveOptions{Force: true})
	}()

	if _, err := c.cli.ContainerStart(ctx, id, mobyclient.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("substrate: restic container start: %w", err)
	}
	wait := c.cli.ContainerWait(ctx, id, mobyclient.ContainerWaitOptions{
		Condition: container.WaitConditionNextExit,
	})
	var code int64
	select {
	case wresp := <-wait.Result:
		code = wresp.StatusCode
	case werr := <-wait.Error:
		// 底座错误或调用方 ctx 到期（上传预算/Stop 取消）：容器由 defer rm 强删。
		return "", fmt.Errorf("substrate: restic container wait: %w", werr)
	}

	// 退出后读日志（多路复用流 → stdout/stderr 分离；stdout 是 --json
	// 消息源，stderr 是进度与错误原文）。
	logs, err := c.cli.ContainerLogs(ctx, id, mobyclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("substrate: restic container logs: %w", err)
	}
	stdout, stderr, derr := demuxContainerStream(logs)
	_ = logs.Close()
	if derr != nil {
		return "", fmt.Errorf("substrate: restic log stream: %w", derr)
	}
	if code != 0 {
		return stdout, fmt.Errorf("substrate: restic %s failed (exit %d): %s",
			firstToken(spec.Args), code, tailText(stderr, resticStderrTailBytes))
	}
	return stdout, nil
}

// demuxContainerStream 拆解 Docker 多路复用输出流（非 TTY 形态：每帧
// 8 字节头 [fd, 0, 0, 0, len(uint32 BE)]，fd 1=stdout 2=stderr）。格式
// 稳定且极小，依赖面仅 encoding/binary——不为一次性容器引入
// moby/go/stdcopy 新模块（供应链面最小化）。
func demuxContainerStream(r io.Reader) (stdout, stderr string, err error) {
	var outBuf, errBuf bytes.Buffer
	hdr := make([]byte, 8)
	for {
		if _, rerr := io.ReadFull(r, hdr); rerr != nil {
			if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
				break
			}
			return "", "", rerr
		}
		n := binary.BigEndian.Uint32(hdr[4:8])
		if n > resticMaxLogFrame {
			return "", "", fmt.Errorf("substrate: restic log frame too large (%d bytes)", n)
		}
		payload := make([]byte, n)
		if _, rerr := io.ReadFull(r, payload); rerr != nil {
			return "", "", rerr
		}
		switch hdr[0] {
		case 1:
			outBuf.Write(payload)
		case 2:
			errBuf.Write(payload)
		}
	}
	return outBuf.String(), errBuf.String(), nil
}

// flattenEnv 把 env map 展平为容器 env 形态（KEY=VALUE；按 map 迭代序，
// restic 对顺序无约束）。值含 secret——本函数只组装容器 spec，绝不打印。
func flattenEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// firstToken 取 args 首个非全局选项词（跳过 `-o`/`--flag` 形态的扩展
// 选项对），用于错误文本标注 restic 子命令（backup/snapshots/forget）。
func firstToken(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			if args[i] == "-o" || args[i] == "--option" {
				i++ // 扩展选项带值，跳过值
			}
			continue
		}
		return args[i]
	}
	return "restic"
}

// tailText 返回文本尾部至多 n 字节（按行边界截齐——错误摘要不留半行）。
func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

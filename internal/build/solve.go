package build

// buildkit solve 层：BuildKit solve 选项的纯构造（单测断言面）+ 连接与
// 执行。moby/buildkit 类型只出现在本文件与 railpack.go（架构 §2.8：第三
// 方概念不出核心；出口一律本包核心类型）。
//
// provenance/sbom 关闭的机制（Spike A #9 硬约束，双保险）：
//  1. 结构性：导出走 docker 导出器 + 客户端侧 Output 管道——产物是
//     docker-format tar，结构上不可能携带 attestation manifest list，
//     流经 ImageLoad 装入本机 daemon（railpack CLI `docker load` 的等价
//     API 形态）；
//  2. 显式性：frontend attrs 不请求任何 attest:* 键（buildkit dockerfile
//     前端的 attestation 只在被请求时产生）。buildopt_test.go 断言两路
//     solve 选项恒无 attest 键且导出恒为 docker 导出器。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	bkclient "github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer" // docker-container:// connhelper
	_ "github.com/moby/buildkit/client/connhelper/npipe"           // npipe://（外部 buildkitd on Windows）
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	secretsprovider "github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/tonistiigi/fsutil"
)

// exporterDocker 是 buildkit docker 导出器类型串。
const exporterDocker = bkclient.ExporterDocker

// secretStampBuildArg 是 Dockerfile 兜底路径的 secrets-hash 缓存戳 ARG 名
// （Spike A E2c：纯 BuildKit 对 `RUN --mount=type=secret` 不失效——hash 戳
// ARG 是必要补偿；Dockerfile 作者声明 `ARG FLEETLY_SECRETS_HASH` 并在后续
// RUN 引用即获得凭证变化失效语义。v0.1 构建无凭证输入，戳值恒为空集哈希，
// 无行为影响；契约先行，未来接入构建凭证零迁移）。
const secretStampBuildArg = "FLEETLY_SECRETS_HASH" //nolint:gosec // G101 误报：这是公开 ARG 名约定，不是凭证

// dockerExportEntry 构造 docker 导出器条目（纯函数）。imageConfigJSON 非空
// 时随导出携带镜像配置（railpack 路径已知 config；dockerfile 前端路径留空
// 由前端计算）。Output 管道在执行期接入（wireDockerExport）。
func dockerExportEntry(tag, imageConfigJSON string) bkclient.ExportEntry {
	attrs := map[string]string{"name": tag}
	if imageConfigJSON != "" {
		attrs["containerimage.config"] = imageConfigJSON
	}
	return bkclient.ExportEntry{Type: exporterDocker, Attrs: attrs}
}

// localCacheEntries 构造本地层缓存的导入/导出条目（buildkit local cache：
// cachePath 为 buildkitd 容器内路径 /cache——平台自管容器持久卷挂载点，
// Spike A「缓存是默认能力」）。cachePath 为空 = 缓存关闭的显式形态。
func localCacheEntries(cachePath string) (imports, exports []bkclient.CacheOptionsEntry) {
	if cachePath == "" {
		return nil, nil
	}
	imports = []bkclient.CacheOptionsEntry{{Type: "local", Attrs: map[string]string{"src": cachePath}}}
	exports = []bkclient.CacheOptionsEntry{{Type: "local", Attrs: map[string]string{"dest": cachePath}}}
	return imports, exports
}

// secretsSessionAttachable 构造 buildkit session secrets（构建期凭证经
// `RUN --mount=type=secret` 挂载，永不进镜像层与缓存键——Spike A E3）。
// 空集返回 nil（不出 attachable）。
func secretsSessionAttachable(secrets map[string]string) (session.Attachable, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	values := make(map[string][]byte, len(secrets))
	for k, v := range secrets {
		values[k] = []byte(v)
	}
	return secretsprovider.FromMap(values), nil
}

// dockerfileFrontendAttrs 构造 dockerfile.v0 前端 attrs（纯函数）：filename
// 指向受控子集声明的 dockerfile；build-arg 注入 secrets-hash 戳（E2c 补偿）；
// 不请求任何 attest:*（provenance/sbom 关闭的显式面）。
func dockerfileFrontendAttrs(req Request) map[string]string {
	filename := req.Dockerfile
	if filename == "" {
		filename = "Dockerfile"
	}
	attrs := map[string]string{
		"filename": filename,
		// build-arg:<NAME> 前缀形态（buildkit dockerui 的 build args 通道）。
		"build-arg:" + secretStampBuildArg: SecretsHash(req.Secrets),
	}
	if req.Target != "" {
		attrs["target"] = req.Target
	}
	return attrs
}

// baseSolveOptions 是两路共用的骨架：本地上下文挂载 + secrets session +
// 本地层缓存 + docker 导出条目。
func baseSolveOptions(req Request, tag, imageConfigJSON, cachePath string) (bkclient.SolveOpt, error) {
	appFS, err := fsutil.NewFS(req.ContextDir)
	if err != nil {
		return bkclient.SolveOpt{}, fmtErr("open build context %s: %w", req.ContextDir, err)
	}
	imports, exports := localCacheEntries(cachePath)
	opts := bkclient.SolveOpt{
		LocalMounts:  map[string]fsutil.FS{"context": appFS},
		CacheImports: imports,
		CacheExports: exports,
		Exports:      []bkclient.ExportEntry{dockerExportEntry(tag, imageConfigJSON)},
	}
	attachable, err := secretsSessionAttachable(req.Secrets)
	if err != nil {
		return bkclient.SolveOpt{}, fmtErr("prepare build secrets: %w", err)
	}
	if attachable != nil {
		opts.Session = []session.Attachable{attachable}
	}
	return opts, nil
}

// railpackSolveOptions 构造 railpack LLB solve 选项（纯函数）。LLB 定义
// 由 railpack.go 产出（ConvertPlanToLLB），执行经 solveRunner.solveLLB。
func railpackSolveOptions(req Request, tag, imageConfigJSON, cachePath string) (bkclient.SolveOpt, error) {
	return baseSolveOptions(req, tag, imageConfigJSON, cachePath)
}

// dockerfileSolveOptions 构造 dockerfile.v0 solve 选项（纯函数）：本地上下
// 文 + dockerfile 前端 + 本地层缓存 + docker 导出（config 由前端计算）。
// context 与 dockerfile 两个本地名挂同一目录（dockerfile.v0 从 "dockerfile"
// local 读构建定义；缺省会回退 context——显式双挂载是 buildctl 等价形态，
// 且避免前端 allowedLocalDirs 对未挂载目录的拒绝）。
func dockerfileSolveOptions(req Request, tag, cachePath string) (bkclient.SolveOpt, error) {
	opts, err := baseSolveOptions(req, tag, "", cachePath)
	if err != nil {
		return bkclient.SolveOpt{}, err
	}
	opts.Frontend = "dockerfile.v0"
	opts.FrontendAttrs = dockerfileFrontendAttrs(req)
	if ctxFS, ok := opts.LocalMounts["context"]; ok {
		opts.LocalMounts["dockerfile"] = ctxFS
	}
	return opts, nil
}

// solveRunner 是一次 solve 的执行器：buildkit 连接（connhelper 注册表解析
// host）+ docker 导出管道接入 ImageLoader（本机 daemon）+ SolveStatus →
// 构建日志。
type solveRunner struct {
	host   string
	loader ImageSource
}

// newSolveRunner 构造执行器。
func newSolveRunner(host string, loader ImageSource) *solveRunner {
	return &solveRunner{host: host, loader: loader}
}

// defaultDaemonProbe 构造就绪探测的缺省实现（M2-8）：对 buildkit_host 做
// 一次 Info 拨号——连接即达成 connhelper/gRPC 通道，Info 即服务面。每次
// 拨号独立建立/释放（无长连接复用，探测与 solve 的连接互不共享）；拨号
// 预算由 probeDaemonReady 的 attempt ctx 给定。
func defaultDaemonProbe(host string) func(context.Context) error {
	return func(ctx context.Context) error {
		c, err := bkclient.New(ctx, host)
		if err != nil {
			return fmtErr("probe buildkit at %s: %w", host, err)
		}
		defer func() { _ = c.Close() }()
		if _, err := c.Info(ctx); err != nil {
			return fmtErr("probe buildkit at %s: %w", host, err)
		}
		return nil
	}
}

// solveLLB 执行 LLB 直驱 solve（railpack 路径）。
func (r *solveRunner) solveLLB(ctx context.Context, opts bkclient.SolveOpt, def *llb.Definition, logw io.Writer) error {
	return r.run(ctx, opts, def, logw)
}

// solveFrontend 执行前端 solve（dockerfile.v0 路径；opts.Frontend 必须已设）。
func (r *solveRunner) solveFrontend(ctx context.Context, opts bkclient.SolveOpt, logw io.Writer) error {
	return r.run(ctx, opts, nil, logw)
}

// run 连接 buildkit、执行 solve、渲染日志、装载导出流。
func (r *solveRunner) run(ctx context.Context, opts bkclient.SolveOpt, def *llb.Definition, logw io.Writer) error {
	c, err := bkclient.New(ctx, r.host)
	if err != nil {
		return fmtErr("connect buildkit at %s: %w", r.host, err)
	}
	defer func() { _ = c.Close() }()
	// 连接门（connhelper 的 docker exec 在容器缺失/未运行时才在此暴露）：
	// fail-fast 给出可行动错误。自管容器形态下冷启动就绪窗口已由
	// ensureDaemonReady 的就绪探测（M2-8）吸收，此处到达即应可服务。
	if _, err := c.Info(ctx); err != nil {
		return fmtErr("buildkit unreachable at %s（自管容器应经 fleetlyd EnsureRunning 拉起；外部端点检查配置 build.buildkit_host）: %w", r.host, err)
	}

	// docker 导出管道：导出流（docker-format tar）→ 本机 daemon。管道在
	// Solve 启动前接好（导出器在 solve 中途开始产流）；LoadImage 消费端
	// 并发运行，Solve 返回后关写端等待装载完成。
	pr, pw := io.Pipe()
	loadDone := make(chan error, 1)
	exportWired := wireDockerExport(&opts, pw)
	if exportWired {
		go func() {
			err := r.loader.LoadImage(ctx, pr)
			if err != nil {
				err = fmtErr("load image into local daemon: %w", err)
			}
			loadDone <- err
		}()
	}

	statusCh := make(chan *bkclient.SolveStatus, 8)
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		writeSolveLog(ctx, statusCh, logw)
	}()

	_, err = c.Solve(ctx, def, opts, statusCh)

	<-logDone // Solve 返回（含错误路径）后 ch 关闭，日志渲染收尾
	if exportWired {
		_ = pw.Close() // 导出流结束；错误路径同样关管道唤醒装载端
		if loadErr := <-loadDone; err == nil {
			err = loadErr
		}
	}
	if err != nil {
		return fmtErr("solve: %w", err)
	}
	return nil
}

// wireDockerExport 在 opts.Exports 里找到 docker 导出条目并接入输出管道；
// 返回是否接入（缺 docker 导出条目 = 无需本机装载）。
func wireDockerExport(opts *bkclient.SolveOpt, pw io.WriteCloser) bool {
	for i := range opts.Exports {
		if opts.Exports[i].Type == exporterDocker {
			opts.Exports[i].Output = filesync.FileOutputFunc(func(map[string]string) (io.WriteCloser, error) {
				return pw, nil
			})
			return true
		}
	}
	return false
}

// writeSolveLog 把 SolveStatus 流渲染为确定性文本日志（时间偏移 + 顶点
// 名称 + 步骤输出流；纯文本无 tty 控制序列，面向人与 diff）。写目标为
// 内存 bufio（错误在 Flush 汇聚，写函数无返回错误的必要）。
//
// 消费契约（H5，MG-1）：ctx 取消只标记截断，不得弃读 statusCh——buildkit
// 状态泵是无 ctx 保护的阻塞发送，弃读即泵死锁、Solve 永不返回（并发槽
// 永久占用）。截断后丢弃式排空到 ch 关闭才返回（见循环内注释）。
func writeSolveLog(ctx context.Context, ch <-chan *bkclient.SolveStatus, w io.Writer) {
	start := time.Now()
	bw := bufio.NewWriter(w)
	defer func() { _ = bw.Flush() }()
	seenStart := map[string]bool{}
	seenDone := map[string]bool{}
	stamp := func() string {
		return fmt.Sprintf("[%8.3fs] ", time.Since(start).Seconds())
	}
	printf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(bw, format, args...) //nolint:errcheck // 内存 bufio，错误在 defer Flush 处汇聚
	}
	for {
		select {
		case <-ctx.Done():
			printf("%slog truncated (context done)\n", stamp())
			// H5（MG-1 消费契约）：buildkit 客户端的状态泵是无 ctx 保护的阻塞
			// 发送（moby/buildkit v0.32.2 client/solve.go:393-394 直接
			// `statusChan <- …`），且泵跑在 Solve 的 errgroup 里、
			// `defer close(statusCh)` 要等 Solve 返回才执行。消费端在 ctx
			// 取消即弃读 → 泵永久阻塞在发送上 → eg.Wait 不返回 → Solve 不
			// 返回 → 并发槽永久占用。契约：标记截断后必须丢弃式排空（读到
			// ch 关闭为止）——ctx 已取消时 gRPC 流随之断开，泵很快经 Recv
			// 错误退出、Solve 返回并关闭 ch，本函数随即返回。
			for range ch {
			}
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			for _, v := range st.Vertexes {
				if v.Started != nil && !seenStart[string(v.Digest)] {
					seenStart[string(v.Digest)] = true
					printf("%s--> %s\n", stamp(), v.Name)
				}
				if v.Completed != nil && !seenDone[string(v.Digest)] {
					seenDone[string(v.Digest)] = true
					switch {
					case v.Error != "":
						printf("%sERROR %s: %s\n", stamp(), v.Name, v.Error)
					case v.Started != nil:
						printf("%s<-- %s (%.3fs)%s\n", stamp(), v.Name,
							v.Completed.Sub(*v.Started).Seconds(), cachedMark(v.Cached))
					}
				}
			}
			for _, lg := range st.Logs {
				printf("%s", stamp())
				_, _ = bw.Write(lg.Data)
				if len(lg.Data) == 0 || lg.Data[len(lg.Data)-1] != '\n' {
					_, _ = bw.Write([]byte("\n"))
				}
			}
		}
	}
}

// cachedMark 返回缓存命中标注（二次构建加速对照在日志层可见）。
func cachedMark(cached bool) string {
	if cached {
		return " [CACHED]"
	}
	return ""
}

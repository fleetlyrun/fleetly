// Package builders 实现 Builder Capability 的三 Provider（ADR-0032）：
// dockerfile（本地 Dockerfile 构建）、railpack（钉版 plan 生成型构建）、
// static（产物目录 + 钉版 Caddy 包装）。单包三 Provider：daemon 内嵌
// buildkit 的 Solve（session exporter → moby 导入）与 ImagePush 推送是
// 共享机械，收编于本文件为唯一真源（包内共享不触"providers 互不
// import"——那是包间纪律）。
//
// 连接路径与 buildx docker-driver 同源（ADR-0019：Build 恒在控制面节点，
// BuildKit + 本机 daemon，缓存只在本机，无独立 buildkitd）：经 daemon
// /session 端点 h2c hijack 建立 gRPC，buildkit client 在其上 Solve。
package builders

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/exporter"
	"github.com/moby/buildkit/session/exporter/exporterprovider"
	"github.com/moby/moby/client"
	"github.com/tonistiigi/fsutil"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// daemonClients 是本机 daemon 的双通道客户端（moby API + 内嵌 buildkit）。
type daemonClients struct {
	cli *client.Client
	bk  *bkclient.Client

	// 推送面函数值 seam（2026-10-03 架构评审候选 6）：ImagePush/ImageInspect
	// 是 moby 具体客户端调用，transport 级假面够不到（buildkit session
	// hijack 太深）——digest 回退与推送错误映射此前只有 FLEETLY_TEST_DOCKER=1
	// 真机可测。seam 形态同 swarm Provider provider.go 的函数值位；零值回退
	// 生产实现（生产构造不注入，测试直接注入假面）。
	imagePush    func(ctx context.Context, target, registryAuth string) (client.ImagePushResponse, error)
	imageInspect func(ctx context.Context, target string) (client.ImageInspectResult, error)
}

// pushImage 推送镜像（seam 零值 = 生产实现）。
func (d *daemonClients) pushImage(ctx context.Context, target, registryAuth string) (client.ImagePushResponse, error) {
	if d.imagePush != nil {
		return d.imagePush(ctx, target, registryAuth)
	}
	return d.cli.ImagePush(ctx, target, client.ImagePushOptions{RegistryAuth: registryAuth})
}

// inspectImage 读镜像元数据（seam 零值 = 生产实现）。
func (d *daemonClients) inspectImage(ctx context.Context, target string) (client.ImageInspectResult, error) {
	if d.imageInspect != nil {
		return d.imageInspect(ctx, target)
	}
	return d.cli.ImageInspect(ctx, target)
}

// newDaemonClients 构造双通道客户端：host 为 daemon 端点（空 = DOCKER_HOST
// / 默认套接字）。双通道：
//   - /grpc：主 gRPC（Solve 等），WithContextDialer；
//   - /session：session attach（local context 上传经此），WithSessionDialer。
//
// URL 需绝对形态——DialHijack 直接构造 http.Request，相对路径会写出空
// Host 被拒绝；scheme/host 仅占位，实际经 client 的 dialer 落到 daemon
// 端点。
func newDaemonClients(ctx context.Context, host string) (*daemonClients, error) {
	opts := []client.Opt{}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("builders: docker client: %w", err)
	}
	dial := func(path string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, _ string) (net.Conn, error) {
			return cli.DialHijack(ctx, "http://docker"+path, "h2c", nil)
		}
	}
	bk, err := bkclient.New(ctx, "",
		bkclient.WithContextDialer(dial("/grpc")),
		bkclient.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return cli.DialHijack(ctx, "http://docker/session", proto, meta)
		}),
	)
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("builders: buildkit client: %w", err)
	}
	return &daemonClients{cli: cli, bk: bk}, nil
}

// Close 释放底层连接。
func (d *daemonClients) Close() error {
	_ = d.bk.Close()
	return d.cli.Close()
}

// ping 是 Health 面共用探测（daemon 可达性）。
func (d *daemonClients) ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return err
	}
	return nil
}

// solveRequest 是一次 daemon buildkit Solve 的共享输入（三 Provider 的
// 差异全部在此面上表达：前端选择、前端属性、挂载布局）。
type solveRequest struct {
	// ContextDir 是用户上下文目录（context mount）。
	ContextDir string
	// DockerfileDir 是 dockerfile mount 的目录（dockerfile=用户上下文内
	// 路径；railpack=plan 目录；static=overlay 目录）。
	DockerfileDir string
	// Filename 是 dockerfile mount 内的前端入口文件名（filename attr）。
	Filename string
	// Frontend 是前端名（dockerfile.v0 | gateway.v0）。
	Frontend string
	// FrontendAttrs 是前端属性（dockerfile.v0 的 build-arg:*；gateway.v0
	// 的 source/cache-key 等）。
	FrontendAttrs map[string]string
}

// vcsExcludePatterns 是构建上下文的默认排除面（收尾批 E27）：顶层 VCS
// 元数据目录。缺陷=COPY . . 把 .git 烙进镜像层（git 历史泄入产物，公开
// push 即泄源）且每次构建全量搬运 VCS 目录（上下文上传随历史增长放大）。
// 裁决=只排 VCS 元数据（.git/.svn/.hg），只排顶层——嵌套的 .git 与
// node_modules 等用户域内容属 .dockerignore/spec 裁决域，平台不越权
// （buildx 缺省行为同口径：VCS 排除只在上下文根）。
//
// 收口点=solveAndPush 的 context mount 组装（三 Builder Provider 共享的
// 唯一上下文入口；dockerfile mount 是平台自产目录，无用户内容不过滤）。
// 排除机制=fsutil.NewFilterFS（buildx .dockerignore 过滤同一机制，
// patternmatcher 语义：顶层 ".git" 命中该目录及其全部后代，无 glob 不
// 误伤同名子路径）。
var vcsExcludePatterns = []string{".git", ".svn", ".hg"}

// newContextFS 组装构建上下文 mount（VCS 排除后的 fsutil.FS）。
func newContextFS(dir string) (fsutil.FS, error) {
	f, err := fsutil.NewFS(dir)
	if err != nil {
		return nil, err
	}
	filtered, err := fsutil.NewFilterFS(f, &fsutil.FilterOpt{ExcludePatterns: vcsExcludePatterns})
	if err != nil {
		return nil, err
	}
	return filtered, nil
}

// solveAndPush 执行 Solve（docker exporter 经 session 由 client 侧声明，
// 产物导入本机 daemon 镜像库，--load 等价）→ daemon ImagePush 推送 Target
// → digest 从推送流 aux 回填（RepoDigests 兜底）。进度流与推送流文本行
// 实时写 w（F0.9 构建日志实时流）；ctx 取消即中止（cancelled 由 engine
// 落状态）。推送机制 = docker-driver 不支持 buildkit image exporter push
// （ADR-0019 附录 B.6）。
func (d *daemonClients) solveAndPush(ctx context.Context, req capability.BuildRequest, s solveRequest, w capability.LogWriter) (string, error) {
	if req.ContextDir == "" {
		return "", fmt.Errorf("builders: build context directory is required")
	}
	if req.Target == "" {
		return "", fmt.Errorf("builders: build target image name is required")
	}
	if w == nil {
		return "", fmt.Errorf("builders: a log writer is required")
	}

	statusCh := make(chan *bkclient.SolveStatus, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- streamProgress(ctx, req.BuildID, w, statusCh)
	}()

	contextFS, err := newContextFS(req.ContextDir)
	if err != nil {
		return "", fmt.Errorf("builders: context dir: %w", err)
	}
	dockerfileFS, err := fsutil.NewFS(s.DockerfileDir)
	if err != nil {
		return "", fmt.Errorf("builders: frontend input dir: %w", err)
	}
	// docker exporter 经 session 由 client 侧声明（buildx docker-driver 同
	// 模式：daemon 内嵌 buildkit 回调 FindExporters，产物导入本机 daemon
	// 镜像库；多节点分发经随后推送承担，附录 B.6）。
	sessionExporter := exporterprovider.New(func(ctx context.Context, md map[string][]byte, refs []string) ([]*exporter.ExporterRequest, error) {
		return []*exporter.ExporterRequest{{
			// "moby" 是 dockerd 内嵌 buildkit 的镜像导出器名（写 daemon
			// 镜像库；独立 buildkitd 无此导出器）。
			Type:  "moby",
			Attrs: map[string]string{"name": req.Target},
		}}, nil
	})

	_, err = d.bk.Solve(ctx, nil, bkclient.SolveOpt{
		Frontend:      s.Frontend,
		FrontendAttrs: s.FrontendAttrs,
		// 前端入口文件经独立 "dockerfile" mount 授权读取（buildx
		// docker-driver 同款：filename attr 指向该 mount 内路径）。
		LocalMounts:           map[string]fsutil.FS{"context": contextFS, "dockerfile": dockerfileFS},
		EnableSessionExporter: true,
		Session:               []session.Attachable{sessionExporter},
	}, statusCh)
	if perr := <-errCh; perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return "", fmt.Errorf("builders: solve: %w", err)
	}

	return d.pushBuiltImage(ctx, req, req.Target, w)
}

// pushBuiltImage 推送本机镜像到 Target 仓库并回填 manifest digest（推送流
// aux 优先；RepoDigests 兜底——旧 daemon 可能不回 aux）。推送流文本行进
// 构建日志（与 buildkit 步骤日志同一条实时流）。
//
// 带界裁决（B15-3）：ImagePush 不在本层另设空闲上限——ctx 由调用链上游
// 统一带界：executeBuild 以 BuildTimeout 硬超时包裹整条构建链
// （internal/engine/builder.go:230 context.WithTimeout(buildRootCtx(),
// buildOpts.Timeout)，Options.BuildTimeout 缺省 15m 见 engine.go:75，
// 超时=expired 看门狗落行），Stop 取消经 buildRootCtx 传导（排水有界，
// 回 queued 重放）。推送大镜像的长尾不该被第二重超时误杀，取消语义已在
// 上游钉死；若未来出现不经 executeBuild 的调用方，须自带同等硬界再进本
// 函数。
func (d *daemonClients) pushBuiltImage(ctx context.Context, req capability.BuildRequest, target string, w capability.LogWriter) (string, error) {
	auth := ""
	if req.PushCred != nil {
		var err error
		auth, err = capability.EncodeRegistryAuth(*req.PushCred)
		if err != nil {
			return "", fmt.Errorf("builders: encode push credentials: %w", err)
		}
	}
	push, err := d.pushImage(ctx, target, auth)
	if err != nil {
		return "", fmt.Errorf("builders: push %s: %w", target, err)
	}
	digest := ""
	for msg, err := range push.JSONMessages(ctx) {
		if err != nil {
			return "", fmt.Errorf("builders: push %s: %w", target, err)
		}
		if msg.Error != nil {
			return "", fmt.Errorf("builders: push %s: %s", target, msg.Error.Message)
		}
		if dg := digestFromAux(msg.Aux); dg != "" {
			digest = dg
		}
		if s := strings.TrimSpace(msg.Stream); s != "" {
			_ = w.WriteLog(ctx, capability.LogFrame{WorkloadID: req.BuildID, Container: "push", Line: []byte(s)})
		}
	}
	if digest == "" {
		// RepoDigests 兜底：推送后 daemon 记录 <repo>@sha256:<digest>。
		inspect, ierr := d.inspectImage(ctx, target)
		if ierr != nil {
			return "", fmt.Errorf("builders: inspect pushed image: %w", ierr)
		}
		repo := targetRepoPrefix(target)
		for _, rd := range inspect.RepoDigests {
			if at := strings.LastIndex(rd, "@"); at > 0 && strings.HasPrefix(rd, repo) {
				digest = rd[at+1:]
				break
			}
		}
	}
	if digest == "" {
		return "", fmt.Errorf("builders: push %s: no manifest digest reported", target)
	}
	return digest, nil
}

// pushAuxJSON 是推送流 aux 载荷的 digest 面。
type pushAuxJSON struct {
	Digest string `json:"digest"`
}

// digestFromAux 从 aux 载荷提取 manifest digest（非 digest 载荷返回空）。
func digestFromAux(aux *json.RawMessage) string {
	if aux == nil || len(*aux) == 0 {
		return ""
	}
	var a pushAuxJSON
	if err := json.Unmarshal(*aux, &a); err != nil || a.Digest == "" {
		return ""
	}
	return a.Digest
}

// targetRepoPrefix 返回推送目标去掉 tag 的 repo 前缀（RepoDigests 匹配
// 用；tag 分隔=最后一个斜杠之后的冒号——host:port 的冒号必在斜杠前）。
func targetRepoPrefix(target string) string {
	slash := strings.LastIndex(target, "/")
	if c := strings.LastIndex(target, ":"); c > slash {
		return target[:c]
	}
	return target
}

// encodeRegistryAuth 已单源化至 capability.EncodeRegistryAuth（推送凭证
// 的 X-Registry-Auth 形态三面共用；2026-10-03 架构评审候选 5 收口——
// 此前 swarm/builders 各持一份逐字副本，map 形态注释随迁 capability）。

// streamProgress 把 buildkit SolveStatus 流翻译为日志帧（Vertex = 步骤名、
// VertexLog = 步骤日志行；错误返回给 Solve 调用方合并上抛）。
func streamProgress(ctx context.Context, buildID string, w capability.LogWriter, ch <-chan *bkclient.SolveStatus) error {
	names := map[string]string{} // digest → 步骤名（日志行归属）
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case st, ok := <-ch:
			if !ok {
				return nil
			}
			if st == nil {
				continue
			}
			for _, v := range st.Vertexes {
				if v.Name != "" {
					names[v.Digest.String()] = shortStep(v.Name)
				}
			}
			for _, lg := range st.Logs {
				if err := w.WriteLog(ctx, capability.LogFrame{
					WorkloadID: buildID,
					Container:  names[lg.Vertex.String()],
					Time:       lg.Timestamp,
					Line:       append([]byte(nil), lg.Data...),
				}); err != nil {
					return err
				}
			}
		}
	}
}

// shortStep 把 buildkit 步骤名压成人读短形态（完整命令保持在前缀）。
func shortStep(name string) string {
	if len(name) > 48 {
		return name[:48] + "…"
	}
	return name
}

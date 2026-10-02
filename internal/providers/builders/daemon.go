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
	"encoding/base64"
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

	contextFS, err := fsutil.NewFS(req.ContextDir)
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
func (d *daemonClients) pushBuiltImage(ctx context.Context, req capability.BuildRequest, target string, w capability.LogWriter) (string, error) {
	auth := ""
	if req.PushCred != nil {
		var err error
		auth, err = encodeRegistryAuth(*req.PushCred)
		if err != nil {
			return "", fmt.Errorf("builders: encode push credentials: %w", err)
		}
	}
	push, err := d.cli.ImagePush(ctx, target, client.ImagePushOptions{RegistryAuth: auth})
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
		inspect, ierr := d.cli.ImageInspect(ctx, target)
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

// encodeRegistryAuth 把平台凭证编码为 X-Registry-Auth 头值（base64 JSON；
// swarm Provider 同款编码与 map 形态，providers 互不 import 各持一份——
// map 键不触发 gosec G117 的结构体字段模式）。
func encodeRegistryAuth(c capability.RegistryCredential) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"username":      c.Username,
		"password":      c.Secret,
		"serveraddress": c.Server,
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}

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

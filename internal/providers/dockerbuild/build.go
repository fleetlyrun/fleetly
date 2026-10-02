package dockerbuild

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/exporter"
	"github.com/moby/buildkit/session/exporter/exporterprovider"
	"github.com/moby/moby/client"
	"github.com/tonistiigi/fsutil"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Build 执行一次构建：context 本地目录 → dockerfile.v0 前端 → docker
// exporter 导入本机 daemon（镜像以 Target 命名，构建缓存与本地调试保留）
// → daemon ImagePush 推送到 Target 仓库（docker-driver 不支持 buildkit
// image exporter push，推送走 `docker push` 同路径，ADR-0019 附录 B.6）；
// digest=推送产物的 manifest digest（Revision 冻结锚）。进度流实时写 w
// （F0.9 构建日志实时流）；ctx 取消即中止（cancelled 由 engine 落状态）。
func (p *Provider) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	if req.ContextDir == "" {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: build context directory is required")
	}
	dockerfile := req.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	attrs := map[string]string{"filename": dockerfile}
	for k, v := range req.Args {
		attrs["build-arg:"+k] = v
	}
	target := req.Target
	if target == "" {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: build target image name is required")
	}

	statusCh := make(chan *bkclient.SolveStatus, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		errCh <- p.streamProgress(ctx, req.BuildID, w, statusCh)
	}()

	// 本地 context 目录 → fsutil 挂载（buildkit local mount 协议）。
	contextFS, err := fsutil.NewFS(req.ContextDir)
	if err != nil {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: context dir: %w", err)
	}
	// docker exporter 经 session 由 client 侧声明（buildx docker-driver 同
	// 模式：daemon 内嵌 buildkit 回调 FindExporters，产物导入本机 daemon
	// 镜像库，--load 等价；多节点分发经随后推送承担，附录 B.6）。
	sessionExporter := exporterprovider.New(func(ctx context.Context, md map[string][]byte, refs []string) ([]*exporter.ExporterRequest, error) {
		return []*exporter.ExporterRequest{{
			// "moby" 是 dockerd 内嵌 buildkit 的镜像导出器名（写 daemon
			// 镜像库；独立 buildkitd 无此导出器）。
			Type:  "moby",
			Attrs: map[string]string{"name": target},
		}}, nil
	})

	resp, err := p.bk.Solve(ctx, nil, bkclient.SolveOpt{
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
		// frontend 要求 Dockerfile 经独立 "dockerfile" mount 授权读取
		//（buildx docker-driver 同款：filename attr 指向该 mount 内路径）。
		LocalMounts:           map[string]fsutil.FS{"context": contextFS, "dockerfile": contextFS},
		EnableSessionExporter: true,
		Session:               []session.Attachable{sessionExporter},
	}, statusCh)
	if perr := <-errCh; perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: solve: %w", err)
	}
	_ = resp

	digest, err := p.pushBuiltImage(ctx, req, target, w)
	if err != nil {
		return capability.BuildResult{}, err
	}
	return capability.BuildResult{Digest: digest}, nil
}

// pushBuiltImage 推送本机镜像到 Target 仓库并回填 manifest digest（推送流
// aux 优先；RepoDigests 兜底——旧 daemon 可能不回 aux）。推送流文本行进
// 构建日志（与 buildkit 步骤日志同一条实时流）。
func (p *Provider) pushBuiltImage(ctx context.Context, req capability.BuildRequest, target string, w capability.LogWriter) (string, error) {
	auth := ""
	if req.PushCred != nil {
		var err error
		auth, err = encodeRegistryAuth(*req.PushCred)
		if err != nil {
			return "", fmt.Errorf("dockerbuild: encode push credentials: %w", err)
		}
	}
	push, err := p.cli.ImagePush(ctx, target, client.ImagePushOptions{RegistryAuth: auth})
	if err != nil {
		return "", fmt.Errorf("dockerbuild: push %s: %w", target, err)
	}
	digest := ""
	for msg, err := range push.JSONMessages(ctx) {
		if err != nil {
			return "", fmt.Errorf("dockerbuild: push %s: %w", target, err)
		}
		if msg.Error != nil {
			return "", fmt.Errorf("dockerbuild: push %s: %s", target, msg.Error.Message)
		}
		if d := digestFromAux(msg.Aux); d != "" {
			digest = d
		}
		if s := strings.TrimSpace(msg.Stream); s != "" && w != nil {
			_ = w.WriteLog(ctx, capability.LogFrame{WorkloadID: req.BuildID, Container: "push", Line: []byte(s)})
		}
	}
	if digest == "" {
		// RepoDigests 兜底：推送后 daemon 记录 <repo>@sha256:<digest>。
		inspect, err := p.cli.ImageInspect(ctx, target)
		if err != nil {
			return "", fmt.Errorf("dockerbuild: inspect pushed image: %w", err)
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
		return "", fmt.Errorf("dockerbuild: push %s: no manifest digest reported", target)
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

// targetRepoPrefix 返回推送目标去掉 tag 的 repo 前缀（RepoDigests 匹配用；
// tag 分隔=最后一个斜杠之后的冒号——host:port 的冒号必在斜杠前）。
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
func (p *Provider) streamProgress(ctx context.Context, buildID string, w capability.LogWriter, ch <-chan *bkclient.SolveStatus) error {
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

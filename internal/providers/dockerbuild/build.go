package dockerbuild

import (
	"context"
	"fmt"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/exporter"
	"github.com/moby/buildkit/session/exporter/exporterprovider"
	"github.com/tonistiigi/fsutil"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// Build 执行一次构建：context 本地目录 → dockerfile.v0 前端 → docker
// exporter 导入本机 daemon（镜像以 Target 命名；多节点镜像分发随 zot
// Registry 批次 N1 接入）。进度流实时写 w（F0.9 构建日志实时流）；
// ctx 取消即中止（cancelled 由 engine 落状态）。
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
	// 镜像库，--load 等价；N0 单节点分发形态，zot Registry 批次转 push）。
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

	// digest 取本机镜像 ID（N0 本地不可变标识；Registry 批次接入后转为
	// 仓库 digest）。
	inspect, err := p.cli.ImageInspect(ctx, target)
	if err != nil {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: inspect built image: %w", err)
	}
	digest := inspect.ID
	if digest == "" {
		return capability.BuildResult{}, fmt.Errorf("dockerbuild: built image %s has no id", target)
	}
	_ = resp // exporter 响应 attrs 在 Registry 批次消费（push URL/真正的 digest）
	return capability.BuildResult{Digest: digest}, nil
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

//go:build manual

package substrate_test

// IMPL-T1-2/DT-2 的本地单节点真机探针（默认不跑；对本地 swarm 建一只
// restart=none 的一次性服务，真拉一次公共 ghcr 镜像后删除——零预拉部署
// 的同构最小实证）：
//
//	FLEETLY_MANUAL_SWARM=1 go test -tags manual ./internal/substrate -run TestManualPublicImageZeroPrePull -v
//
// 严格语义：创建前移除同名 tag 与 digest 引用（本机镜像缓存清场），再经
// substrate.ImageDigest 的 registry-first 解析取 digest、以 digest 钉定
// 引用建服务——任务完成 = swarm 按 digest 从 ghcr 真拉成功（单节点）。
// staging 多节点「ghcr 公共镜像零预拉部署」与「私有镜像 + 凭证逐节点拉取」
// 仍归 T2-0②（本探针不替代）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

func TestManualPublicImageZeroPrePull(t *testing.T) {
	if os.Getenv("FLEETLY_MANUAL_SWARM") != "1" {
		t.Skip("set FLEETLY_MANUAL_SWARM=1 to probe the local swarm")
	}
	image := "ghcr.io/astral-sh/uv:latest"
	if custom := os.Getenv("FLEETLY_MANUAL_IMAGE"); custom != "" {
		image = custom
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	c, err := substrate.NewClient("")
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}

	const serviceName = "fleetly-probe-t2-pull"
	removeService := func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer rcancel()
		_ = c.ServiceRemove(rctx, serviceName)
	}
	removeService()
	t.Cleanup(removeService)

	// 清场：移除本机同名 tag（零预拉前提）。
	if _, err := c.InspectImage(ctx, image); err == nil {
		if err := c.RemoveImage(ctx, image); err != nil {
			t.Fatalf("remove local tag before probe: %v", err)
		}
	}

	digest, err := c.ImageDigest(ctx, image)
	if err != nil {
		t.Fatalf("resolve %s via registry-first: %v", image, err)
	}
	t.Logf("resolved %s -> %s (registry-first, no local inspect)", image, digest)
	pinned := image + "@" + digest
	t.Cleanup(func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer rcancel()
		_ = c.RemoveImage(rctx, pinned)
	})

	if _, err := c.InspectImage(ctx, pinned); err == nil {
		if err := c.RemoveImage(ctx, pinned); err != nil {
			t.Fatalf("remove local digest reference before probe: %v", err)
		}
	}

	spec := engine.ServiceSpec{
		Name: serviceName,
		Image: pinned,
		// swarmkit 语义：ContainerSpec.Command = ENTRYPOINT（args 无表达面
		// ——本探针直接给出完整可执行引用）。
		Command:       []string{"/uv", "--version"},
		Replicas:      1,
		RestartPolicy: &engine.RestartPolicySpec{Condition: "none"},
	}
	if err := c.ServiceCreate(ctx, spec); err != nil {
		t.Fatalf("service create with digest-pinned image: %v", err)
	}

	deadline := time.Now().Add(6 * time.Minute)
	for {
		tasks, err := c.TaskList(ctx, serviceName)
		if err != nil {
			t.Fatalf("task list: %v", err)
		}
		if len(tasks) > 0 {
			task := tasks[0]
			t.Logf("task %s state=%s desired=%s err=%q", task.ID, task.State, task.DesiredState, task.Err)
			switch task.State {
			case "complete":
				if task.Image != pinned {
					t.Fatalf("task image = %q, want digest-pinned %q", task.Image, pinned)
				}
				t.Logf("zero-pre-pull digest deploy succeeded: %s -> %s", image, digest)
				return
			case "failed", "rejected":
				t.Fatalf("task %s failed: %s", task.State, task.Err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not complete within the budget (last: %v)", tasks)
		}
		time.Sleep(3 * time.Second)
	}
}

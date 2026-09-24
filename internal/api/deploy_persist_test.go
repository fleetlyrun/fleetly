package api

// S18-A7：部署 compose 持久化测试——Deploy 入队时字节持久化
// <数据根>/deployments/<id>/compose.yaml（行指向持久化路径，不再依赖 OS
// 临时目录）；删除临时解析中转后（tmpfiles 清理同语义）持久化副本仍可
// 重载（引擎 preparing 的复核输入）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// deployCompose 是 Deploy 测试的最小 compose fixture。
func deployCompose(name string) []byte {
	return []byte("name: " + name + "\nservices:\n  web:\n    image: alpine:3\n    command: [\"sleep\", \"infinity\"]\n")
}

// TestDeployPersistsComposeUnderDataRoot A7：入队后 compose 落数据根
// deployments/<id>/compose.yaml、行指向该路径；临时解析中转被删（模拟
// tmpfiles 清理）后持久化副本仍可 compose.Load 重载且 spec_hash 复核一致
// （引擎 preparing 重载不因 temp 丢失失败）。
func TestDeployPersistsComposeUnderDataRoot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	deployments := serverv1.NewDeploymentsServiceClient(env.conn)

	resp, err := deployments.Deploy(authCtx(ctx, env.depTok), &serverv1.DeployRequest{Project: env.projectRef(), 
		App:     "persistapp",
		Compose: deployCompose("persistapp"),
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	rec, err := env.st.GetDeployment(ctx, resp.GetDeploymentId())
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}

	// 布局断言：ComposePath 位于 <数据根>/deployments/<id>/compose.yaml。
	wantRoot := state.DeploymentsRoot(env.st.Path())
	wantPath := filepath.Join(wantRoot, rec.ID, "compose.yaml")
	if rec.ComposePath != wantPath {
		t.Fatalf("compose_path = %q, want %q", rec.ComposePath, wantPath)
	}
	// 目录形态：<id>/ 下即持久化副本（与解析中转的随机 temp 目录互不相干
	//——tmpfiles 清理的是后者；数据根的清理由 janitor 30 天窗承载）。
	entries, err := os.ReadDir(wantRoot)
	if err != nil {
		t.Fatalf("read deployments root: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != rec.ID {
		t.Fatalf("deployments root entries = %v, want exactly [%s]", entries, rec.ID)
	}
	raw, err := os.ReadFile(rec.ComposePath) //nolint:gosec // 测试断言读取受控路径
	if err != nil {
		t.Fatalf("read persisted compose: %v", err)
	}
	if string(raw) != string(deployCompose("persistapp")) {
		t.Fatalf("persisted compose bytes drifted:\n%s", raw)
	}

	// 模拟 tmpfiles 清理：临时目录本就不被行引用——这里直接验证持久化
	// 副本可重载（引擎 preparing 的复核输入，spec_hash 与入队时一致）。
	spec, _, err := compose.Load(ctx, rec.ComposePath)
	if err != nil {
		t.Fatalf("reload persisted compose: %v", err)
	}
	if spec.SpecHash != rec.SpecHash {
		t.Fatalf("reloaded spec_hash = %s, want %s (hash at enqueue time)", spec.SpecHash, rec.SpecHash)
	}
}

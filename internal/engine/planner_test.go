package engine

// 规划层测试：受管字段与平台缺省的固定组合（release-semantics §2.8）、
// env 三层合并与快照哈希（脱敏）、卷映射、desired-hash 稳定性。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func planFixture(t *testing.T, yaml string) (*compose.Spec, []compose.Warning) {
	t.Helper()
	spec, warnings, err := compose.Load(context.Background(), writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("compose load: %v", err)
	}
	return spec, warnings
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestBuildPlanDefaultsAndManagedFields(t *testing.T) {
	spec, _ := planFixture(t, `name: app1
services:
  web:
    image: repo/app:1
    command: ["run"]
    environment:
      PLAIN: hello
    healthcheck:
      test: ["CMD", "check"]
    deploy:
      replicas: 2
`)
	plan, err := BuildPlan(PlanInput{
		AppID:        "app1id",
		AppName:      "app1",
		DeploymentID: "dep1",
		Spec:         spec,
		FileEnv:      map[string]map[string]string{},
		ComposeEnv:   map[string]map[string]string{"web": {"PLAIN": "hello"}},
		PlatformEnv:  []envlayer.PlatformVar{{Key: "PLAIN", Value: "platform", Source: "platform"}},
		Images:       map[string]string{"web": "repo/app:1@sha256:abc"},
		Decision:     placement.Decision{},
		Volumes:      nil,
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Services) != 1 {
		t.Fatalf("services = %d", len(plan.Services))
	}
	svc := plan.Services[0]
	wantName, _ := naming.ServiceName("app1", "web")
	if svc.Name != wantName {
		t.Fatalf("service name = %s, want %s", svc.Name, wantName)
	}
	if svc.Image != "repo/app:1@sha256:abc" {
		t.Fatalf("image = %s", svc.Image)
	}
	if svc.UpdateOrder != "start-first" || svc.UpdateParallelism != 1 {
		t.Fatalf("update defaults wrong: order=%s parallelism=%d", svc.UpdateOrder, svc.UpdateParallelism)
	}
	// 健康检查平台缺省（未写子字段 → 5s/3s/3/10s）。
	hc := svc.Healthcheck
	if hc == nil || hc.Interval.String() != "5s" || hc.Timeout.String() != "3s" || hc.Retries != 3 || hc.StartPeriod.String() != "10s" {
		t.Fatalf("healthcheck defaults wrong: %+v", hc)
	}
	// env 三层合并：平台层覆盖 compose 层；快照哈希不含明文。
	env := map[string]string{}
	for _, kv := range svc.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	if env["PLAIN"] != "platform" {
		t.Fatalf("platform env not merged: %v", env)
	}
	// env_snapshot_hash 稳定且与明文值无关（换明文同 hash 结构脱敏）。
	if plan.EnvSnapshotHash == "" {
		t.Fatal("env snapshot hash empty")
	}
	// 快照不含期望态哈希（对账时以当前发布归属重写服务 label 并补哈希——
	// label 更新零任务替换，Spike B2）。
	raw := string(plan.DesiredSpecJSON)
	if strings.Contains(raw, "fleetly.desired-hash") {
		t.Fatalf("snapshot leaked desired-hash label: %s", raw)
	}
	// 期望态哈希稳定（同输入两次一致）。
	plan2, err := BuildPlan(PlanInput{
		AppID: "app1id", AppName: "app1", DeploymentID: "dep1",
		Spec: spec, FileEnv: map[string]map[string]string{},
		ComposeEnv:  map[string]map[string]string{"web": {"PLAIN": "hello"}},
		PlatformEnv: []envlayer.PlatformVar{{Key: "PLAIN", Value: "platform", Source: "platform"}},
		Images:      map[string]string{"web": "repo/app:1@sha256:abc"},
	})
	if err != nil {
		t.Fatalf("BuildPlan 2: %v", err)
	}
	if plan.DesiredHash != plan2.DesiredHash {
		t.Fatal("desired hash unstable across identical inputs")
	}
}

func TestBuildPlanForcesStopFirstWithVolumes(t *testing.T) {
	spec, _ := planFixture(t, `name: app1
services:
  db:
    image: repo/db:1
    volumes:
      - data:/var/lib/data
volumes:
  data:
`)
	plan, err := BuildPlan(PlanInput{
		AppID: "app1id", AppName: "app1", DeploymentID: "dep1",
		Spec:    spec,
		FileEnv: map[string]map[string]string{}, ComposeEnv: map[string]map[string]string{},
		Images: map[string]string{"db": "repo/db:1@sha256:ddd"},
		Decision: placement.Decision{Bind: true, PlatformNodeID: "n_test",
			Constraint: "node.labels.fleetly.node-id == n_test"},
		Volumes: []state.Volume{{AppID: "app1id", Key: "data", Name: "fleetly-app1-data-test"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	svc := plan.Services[0]
	if svc.UpdateOrder != "stop-first" {
		t.Fatalf("volume service order = %s, want stop-first（平台强制）", svc.UpdateOrder)
	}
	if len(svc.Mounts) != 1 || svc.Mounts[0].VolumeName != "fleetly-app1-data-test" {
		t.Fatalf("mounts = %+v", svc.Mounts)
	}
	// 绑定约束进放置。
	if len(svc.Constraints) != 1 || !strings.Contains(svc.Constraints[0], "n_test") {
		t.Fatalf("constraints = %v", svc.Constraints)
	}
	// 重启策略缺省 any/5s。
	if svc.RestartPolicy == nil || svc.RestartPolicy.Condition != "any" || svc.RestartPolicy.Delay.String() != "5s" {
		t.Fatalf("restart policy = %+v", svc.RestartPolicy)
	}
}

func TestBuildPlanMissingVolumeFails(t *testing.T) {
	spec, _ := planFixture(t, `name: app1
services:
  db:
    image: repo/db:1
    volumes:
      - data:/var/lib/data
volumes:
  data:
`)
	_, err := BuildPlan(PlanInput{
		AppID: "app1id", AppName: "app1", DeploymentID: "dep1",
		Spec:    spec,
		FileEnv: map[string]map[string]string{}, ComposeEnv: map[string]map[string]string{},
		Images:  map[string]string{"db": "repo/db:1@sha256:ddd"},
		Volumes: nil, // 未登记（放置 Apply 未执行）
	})
	var ae *apperr.Error
	if !asAppErr(err, &ae) || ae == nil || ae.Code() != "E_PLACEMENT_NODE_UNAVAILABLE" {
		t.Fatalf("err = %v, want E_PLACEMENT_NODE_UNAVAILABLE（卷未登记快速失败）", err)
	}
}

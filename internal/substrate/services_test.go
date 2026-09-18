package substrate

// engine.Substrate 适配层的 spec 翻译测试（纯函数，无 daemon）：受管字段
// 固定（failure_action=pause、monitor=5s、ForceUpdate 不出现——归位零成本
// 纪律）、字段映射与缺省补齐。

import (
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine"
)

func TestBuildSwarmSpecManagedFields(t *testing.T) {
	spec := engine.ServiceSpec{
		Name:              "fleetly-demo-web",
		Image:             "repo/app@sha256:abc",
		Command:           []string{"run"},
		Env:               []string{"A=1", "B=2"},
		ContainerLabels:   map[string]string{"fleetly.app": "demo"},
		ServiceLabels:     map[string]string{"fleetly.managed": "true", "fleetly.app": "demo"},
		Replicas:          2,
		UpdateOrder:       "start-first",
		UpdateParallelism: 1,
		Healthcheck: &engine.HealthcheckSpec{
			Test: []string{"CMD", "true"}, Interval: 5 * time.Second,
			Timeout: 3 * time.Second, Retries: 3, StartPeriod: 10 * time.Second,
		},
		RestartPolicy: &engine.RestartPolicySpec{Condition: "any", Delay: 5 * time.Second},
	}
	sw := buildSwarmSpec(spec)

	uc := sw.UpdateConfig
	if uc == nil {
		t.Fatal("update config missing")
	}
	if string(uc.FailureAction) != "pause" {
		t.Fatalf("failure_action = %s, want pause（平台固定，D-REL-1）", uc.FailureAction)
	}
	if uc.Monitor != 5*time.Second {
		t.Fatalf("monitor = %s, want 5s（平台固定，不放大）", uc.Monitor)
	}
	if uc.Parallelism != 1 || string(uc.Order) != "start-first" {
		t.Fatalf("parallelism/order = %d/%s", uc.Parallelism, uc.Order)
	}
	if sw.Name != "fleetly-demo-web" || sw.Labels["fleetly.managed"] != "true" {
		t.Fatalf("annotations = %+v", sw.Annotations)
	}
	cs := sw.TaskTemplate.ContainerSpec
	if cs == nil || cs.Image != "repo/app@sha256:abc" || len(cs.Env) != 2 {
		t.Fatalf("container spec wrong: %+v", cs)
	}
	if cs.Healthcheck == nil || cs.Healthcheck.Interval != 5*time.Second || cs.Healthcheck.Retries != 3 {
		t.Fatalf("healthcheck = %+v", cs.Healthcheck)
	}
	if sw.TaskTemplate.RestartPolicy == nil || string(sw.TaskTemplate.RestartPolicy.Condition) != "any" {
		t.Fatal("restart policy condition missing")
	}
	if sw.Mode.Replicated == nil || sw.Mode.Replicated.Replicas == nil || *sw.Mode.Replicated.Replicas != 2 {
		t.Fatal("replicated mode wrong")
	}
}

func TestBuildSwarmSpecDefaultsAndGlobal(t *testing.T) {
	// 无 restart_policy → 平台缺省 condition=any/delay=5s（architecture §2.5）。
	sw := buildSwarmSpec(engine.ServiceSpec{Name: "s", Image: "img", Replicas: 1})
	rp := sw.TaskTemplate.RestartPolicy
	if rp == nil || string(rp.Condition) != "any" || rp.Delay == nil || *rp.Delay != 5*time.Second {
		t.Fatalf("default restart policy = %+v", rp)
	}

	g := buildSwarmSpec(engine.ServiceSpec{Name: "g", Image: "img", Global: true, UpdateOrder: "stop-first"})
	if g.Mode.Global == nil {
		t.Fatal("global mode missing")
	}
	if string(g.UpdateConfig.Order) != "stop-first" {
		t.Fatalf("global order = %s, want stop-first", g.UpdateConfig.Order)
	}
}

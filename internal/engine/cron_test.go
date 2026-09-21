package engine

// E5 Cron 引擎面测试：cron 服务「只声明不部署」——BuildPlan 产出 Job 模板
// （快照照记）但 plan.Services 不含（长驻对账集）；decodeSpecs 对外投影过
// 滤 Job；对账删除扫描不误删在途 job 服务；全 cron 应用发布直达观察窗。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/envlayer"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// cronPlanFixture 规划一个 web + cron(task) 双服务应用。
func cronPlanFixture(t *testing.T, yaml string) (*compose.Spec, *Plan) {
	t.Helper()
	spec, _ := planFixture(t, yaml)
	plan, err := BuildPlan(PlanInput{
		AppID:        "app1id",
		AppName:      "app1",
		DeploymentID: "dep1",
		Spec:         spec,
		FileEnv:      map[string]map[string]string{},
		ComposeEnv:   map[string]map[string]string{},
		PlatformEnv:  []envlayer.PlatformVar{},
		Images: map[string]string{
			"web":  "repo/app:1@sha256:abc",
			"task": "repo/task:1@sha256:def",
		},
		Decision: placement.Decision{},
		Volumes:  nil,
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	return spec, plan
}

// TestBuildPlanCronDeclaredOnly cron 服务不进长驻对账集；快照保留 Job 模板
// （digest 钉定镜像 + Job 标记）；plan 以 Kind 警告如实披露。
func TestBuildPlanCronDeclaredOnly(t *testing.T) {
	_, plan := cronPlanFixture(t, `name: app1
services:
  web:
    image: repo/app:1
  task:
    image: repo/task:1
    labels:
      fleetly.cron: "*/5 * * * *"
`)
	names := []string{}
	for _, s := range plan.Services {
		names = append(names, s.Name)
	}
	want, _ := naming.ServiceName("app1", "web")
	if len(plan.Services) != 1 || plan.Services[0].Name != want {
		t.Fatalf("long-running set = %v, want only %s", names, want)
	}
	// 快照：web（长驻）+ task（Job 模板）都在；Job 模板镜像 digest 钉定。
	var specs []ServiceSpec
	if err := json.Unmarshal(plan.DesiredSpecJSON, &specs); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	jobFound := false
	for _, s := range specs {
		if s.Name == "fleetly-app1-task" {
			jobFound = true
			if !s.Job || s.Image != "repo/task:1@sha256:def" {
				t.Fatalf("job template wrong: job=%v image=%s", s.Job, s.Image)
			}
		}
	}
	if !jobFound {
		t.Fatal("job template missing from the desired-spec snapshot")
	}
	// 警告面：cron 服务以 Kind 披露（无注册码 → 不产 deployment.warning 事件）。
	found := false
	for _, w := range plan.Warnings {
		if w.Service == "task" && w.Kind == compose.WarningKindCronServiceScheduled && w.Code == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cron plan warning missing: %+v", plan.Warnings)
	}
}

// TestBuildPlanAllCronApp 全 cron 应用：plan.Services 为空（没有长驻服务可
// 对账），快照仍携带 Job 模板——发布语义 = 只声明不部署。
func TestBuildPlanAllCronApp(t *testing.T) {
	_, plan := cronPlanFixture(t, `name: app1
services:
  task:
    image: repo/task:1
    labels:
      fleetly.cron: "* * * * *"
`)
	if len(plan.Services) != 0 {
		t.Fatalf("long-running set = %d, want 0", len(plan.Services))
	}
	if !strings.Contains(string(plan.DesiredSpecJSON), `"job":true`) {
		t.Fatalf("snapshot lacks the job template: %s", plan.DesiredSpecJSON)
	}
}

// TestDecodeSpecsFiltersJobTemplates decodeSpecs 的对外投影（发布对账/回滚
// 重放/漂移/存在性对账的期望集）过滤 Job 模板——长驻面只见长驻服务。
func TestDecodeSpecsFiltersJobTemplates(t *testing.T) {
	h := newHarness(t)
	ctx := h.t.Context()
	app, err := h.store.CreateApp(ctx, "id1", "app1")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	template := ServiceSpec{Name: "fleetly-app1-task", Image: "repo/task:1@sha256:def", Job: true, Replicas: 1}
	raw, err := json.Marshal([]ServiceSpec{template, {Name: "fleetly-app1-web", Image: "repo/app:1@sha256:abc"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cipher, err := h.box.Encrypt(raw)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	rec, err := h.store.CreateDeployment(ctx, state.DeployRecord{
		AppID: app.ID, AppName: "app1", Kind: "deploy", SpecHash: "h", DesiredSpec: string(cipher),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	row, err := h.store.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	specs, err := h.eng.decodeSpecs(row)
	if err != nil {
		t.Fatalf("decodeSpecs: %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "fleetly-app1-web" {
		t.Fatalf("projected specs = %+v, want only the long-running service", specs)
	}
}

// TestAllCronAppDeploysSuccessfully 全 cron 应用端到端：发布无长驻服务可等
// 健康门——不滞留 releasing 超看门狗假失败，直达 observing → succeeded；
// 全程不创建任何 Swarm 服务（只声明不部署的装配面断言）。
func TestAllCronAppDeploysSuccessfully(t *testing.T) {
	h := newHarness(t)
	composeYAML := `name: demo
services:
  task:
    image: repo/task:1
    labels:
      fleetly.cron: "* * * * *"
`
	rec := h.enqueue(h.writeCompose(composeYAML))
	final := h.runToTerminal(rec)
	if final.Status != state.DeploySucceeded {
		t.Fatalf("all-cron app deploy ended %s (%s), want succeeded", final.Status, final.ErrorCode)
	}
	if len(h.sub.services) != 0 {
		t.Fatalf("long-running services created: %v", h.sub.services)
	}
	for _, name := range h.events() {
		if strings.HasPrefix(name, "cron.") {
			t.Fatalf("cron event %s must not leak into the release pipeline", name)
		}
	}
}

// TestApplyDesiredSparesCronJobServices 对账删除扫描（省略=删除）不得删除
// fleetly-cron- 前缀的瞬时 job 服务；普通多余服务照删。
func TestApplyDesiredSparesCronJobServices(t *testing.T) {
	h := newHarness(t)
	ctx := h.t.Context()
	// 预置两个多余服务：一个 fleetly-cron- 前缀的瞬时 job（必须豁免）、一个
	// 普通长驻残留（必须照删）。两者都带受管 label（对账 ServiceList 的选择
	// 器 = managed + app）。
	cronLabels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     "app1",
		state.LabelProcess: "task",
	}
	staleLabels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     "app1",
		state.LabelProcess: "stale",
	}
	h.sub.services["fleetly-cron-app1-task-abc"] = &fakeService{
		spec: ServiceSpec{Name: "fleetly-cron-app1-task-abc", Replicas: 1, ServiceLabels: cronLabels},
	}
	h.sub.services["fleetly-app1-stale"] = &fakeService{
		spec: ServiceSpec{Name: "fleetly-app1-stale", Replicas: 1, ServiceLabels: staleLabels},
	}
	desired := []ServiceSpec{{
		Name:     "fleetly-app1-web",
		Image:    "repo/app:1@sha256:abc",
		Replicas: 1,
		ServiceLabels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     "app1",
			state.LabelProcess: "web",
		},
		UpdateOrder: "start-first",
	}}
	rec := state.DeployRecord{ID: "dep1", AppID: "id1", AppName: "app1"}
	if err := h.eng.applyDesired(ctx, rec, desired, false); err != nil {
		t.Fatalf("applyDesired: %v", err)
	}
	if _, ok := h.sub.services["fleetly-cron-app1-task-abc"]; !ok {
		t.Fatal("in-flight cron job service was removed by the deploy reconcile (must be spared)")
	}
	if _, ok := h.sub.services["fleetly-app1-stale"]; ok {
		t.Fatal("stale long-running service was not removed (omit=delete broken)")
	}
	if _, ok := h.sub.services["fleetly-app1-web"]; !ok {
		t.Fatal("desired service not created")
	}
}

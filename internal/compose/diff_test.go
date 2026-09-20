package compose

import (
	"encoding/json"
	"strings"
	"testing"
)

// planOf 加载两个 compose 并 Diff（base 可空 = 首部署）。
func planOf(t *testing.T, baseContent, targetContent string) *Plan {
	t.Helper()
	var base *Spec
	if baseContent != "" {
		base = loadOK(t, writeCompose(t, baseContent))
	}
	target := loadOK(t, writeCompose(t, targetContent))
	plan, err := Diff(base, target)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return plan
}

const diffBase = `
name: my-api
services:
  web:
    image: nginx:1.26
    expose: ["8080"]
    environment:
      FOO: old-secret-value
      KEEP: same
    healthcheck:
      test: ["CMD", "hc"]
`

const diffTargetChanged = `
name: my-api
services:
  web:
    image: nginx:1.27
    expose: ["8080"]
    environment:
      FOO: new-secret-value
      KEEP: same
    healthcheck:
      test: ["CMD", "hc"]
`

const diffTargetNewService = diffTargetChanged + `
  worker:
    image: my/worker
    command: run
`

const diffTargetRemovedService = `
name: my-api
services:
  worker:
    image: my/worker
`

// TestDiffFirstDeploy 空基线（首部署）：一切服务新增、destructive=false。
func TestDiffFirstDeploy(t *testing.T) {
	plan := planOf(t, "", diffTargetChanged)
	if !plan.HasChanges {
		t.Fatal("first deploy should have changes")
	}
	if len(plan.Services.Added) != 1 || plan.Services.Added[0] != "web" {
		t.Errorf("Added = %v, want [web]", plan.Services.Added)
	}
	if plan.Destructive || plan.RequiresConfirmDestructive {
		t.Error("first deploy is not destructive")
	}
	if plan.BaseSpecHash != "" {
		t.Errorf("BaseSpecHash should be empty for an empty baseline, got %q", plan.BaseSpecHash)
	}
	if plan.SpecHash == "" {
		t.Error("SpecHash not populated")
	}
}

// TestDiffFieldLevel 字段级差分：镜像/env 变更逐字段上报；env 只出
// key+hash（值不明文——验收 4 脱敏）。
func TestDiffFieldLevel(t *testing.T) {
	plan := planOf(t, diffBase, diffTargetChanged)
	if !plan.HasChanges {
		t.Fatal("expected changes")
	}
	if len(plan.Services.Updated) != 1 {
		t.Fatalf("Updated = %+v", plan.Services.Updated)
	}
	u := plan.Services.Updated[0]
	if u.Name != "web" {
		t.Fatalf("updated service = %q", u.Name)
	}
	paths := map[string]FieldChange{}
	for _, f := range u.Fields {
		paths[f.Path] = f
	}
	img, ok := paths["image"]
	if !ok {
		t.Fatalf("missing image field diff: %+v", u.Fields)
	}
	if img.From != "nginx:1.26" || img.To != "nginx:1.27" {
		t.Errorf("image diff = %+v", img)
	}
	envPath, ok := paths["environment.FOO"]
	if !ok {
		t.Fatalf("missing environment.FOO field diff: %+v", u.Fields)
	}
	// env 差分值为 hash 对象（含 hash/source），不含明文。
	for _, side := range []any{envPath.From, envPath.To} {
		raw, _ := json.Marshal(side)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if _, has := m["hash"]; !has {
			t.Errorf("env diff side missing hash field: %s", raw)
		}
		if s := string(raw); strings.Contains(s, "secret-value") {
			t.Errorf("env diff leaks plaintext value: %s", s)
		}
	}
	if _, ok := paths["environment.KEEP"]; ok {
		t.Error("unchanged KEEP should not appear in the diff")
	}
	// 完整 artifact JSON 同样不含明文（负面测试：脱敏结构性成立）。
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), "secret-value") {
		t.Errorf("plan artifact leaks env plaintext: %s", raw)
	}
}

// TestDiffDestructive 验收 4：破坏性标记在服务删除场景出现；卷移除亦为
// 破坏性（解绑、数据保留的语义由执行层兑现）。
func TestDiffDestructive(t *testing.T) {
	plan := planOf(t, diffTargetNewService, diffTargetRemovedService)
	if len(plan.Services.Removed) != 1 || plan.Services.Removed[0] != "web" {
		t.Fatalf("Removed = %v, want [web]", plan.Services.Removed)
	}
	if !plan.Destructive || !plan.RequiresConfirmDestructive {
		t.Errorf("service removal must be flagged destructive: destructive=%v requires=%v", plan.Destructive, plan.RequiresConfirmDestructive)
	}

	volBase := `
name: my-api
services:
  web: { image: nginx, volumes: [data:/d] }
volumes:
  data:
`
	volTarget := `
name: my-api
services:
  web: { image: nginx }
`
	plan = planOf(t, volBase, volTarget)
	if len(plan.Volumes.Removed) != 1 || plan.Volumes.Removed[0] != "data" {
		t.Fatalf("Volumes.Removed = %v", plan.Volumes.Removed)
	}
	if !plan.Destructive {
		t.Error("volume unbind must be flagged destructive")
	}
	if !strings.Contains(plan.RenderText(), "--confirm-destructive") {
		t.Error("human-readable report should mention --confirm-destructive")
	}
}

// TestDestructiveChanges 判定单源函数（MG-C3）的机制单测：服务删除 =
// destructive；仅新增/修改 = 非；卷解绑 = 是；空基线（首部署/nil）= 非。
// Diff 的 plan.destructive 与本函数同口径（上方 TestDiffDestructive 锁定）。
func TestDestructiveChanges(t *testing.T) {
	base := loadOK(t, writeCompose(t, diffTargetNewService))        // web + worker
	removed := loadOK(t, writeCompose(t, diffTargetRemovedService)) // 仅 worker（删 web）
	if !DestructiveChanges(base, removed) {
		t.Error("service removal must be a destructive change")
	}
	orig := loadOK(t, writeCompose(t, diffBase))             // web（旧镜像）
	changed := loadOK(t, writeCompose(t, diffTargetChanged)) // 仅 web（镜像修改）
	if DestructiveChanges(orig, changed) {
		t.Error("service update only is not a destructive change")
	}
	if DestructiveChanges(changed, base) {
		t.Error("service addition only (web already present, worker added) is not a destructive change")
	}
	if DestructiveChanges(nil, changed) {
		t.Error("empty baseline (first deploy) is never destructive")
	}
	if !DestructiveChanges(changed, nil) {
		t.Error("nil target treated as an empty Spec (full removal) should be judged destructive")
	}
	volBase := loadOK(t, writeCompose(t, `
name: my-api
services:
  web: { image: nginx, volumes: [data:/d] }
volumes:
  data:
`))
	volTarget := loadOK(t, writeCompose(t, `
name: my-api
services:
  web: { image: nginx }
`))
	if !DestructiveChanges(volBase, volTarget) {
		t.Error("volume unbind must be a destructive change")
	}
	if DestructiveChanges(volBase, volBase) {
		t.Error("identical specs must not be judged destructive")
	}
}

// TestDiffNoChanges 同内容两份 → 无变更（退出码 0 的依据）。
func TestDiffNoChanges(t *testing.T) {
	plan := planOf(t, diffBase, diffBase)
	if plan.HasChanges {
		t.Fatalf("identical content should yield no changes: %+v", plan)
	}
	if plan.Destructive {
		t.Error("no changes must not be flagged destructive")
	}
	if !strings.Contains(plan.RenderText(), "no changes") {
		t.Errorf("human-readable report should output no changes: %s", plan.RenderText())
	}
}

// TestDiffNilBase Diff(nil, x) 与空基线等价（接口化：DB 基线随引擎票接入，
// nil = 首部署语义）。
func TestDiffNilBase(t *testing.T) {
	target := loadOK(t, writeCompose(t, diffTargetChanged))
	plan, err := Diff(nil, target)
	if err != nil {
		t.Fatalf("Diff(nil, x): %v", err)
	}
	if !plan.HasChanges || len(plan.Services.Added) != 1 {
		t.Fatalf("nil baseline should be treated as a first deploy: %+v", plan)
	}
}

// TestPlanArtifactETag plan artifact 的 spec_hash 即 etag：与目标 Spec
// hash 一致；且同一内容重算 plan 保持稳定。
func TestPlanArtifactETag(t *testing.T) {
	target := loadOK(t, writeCompose(t, diffTargetChanged))
	plan, err := Diff(nil, target)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if plan.SpecHash != target.SpecHash {
		t.Errorf("etag = %s, target spec_hash = %s", plan.SpecHash, target.SpecHash)
	}
	plan2, _ := Diff(nil, target)
	if plan.SpecHash != plan2.SpecHash {
		t.Error("plan etag is unstable")
	}
}

// TestPlanWarningsStatelessPin 计划期警告：无卷应用显式 pin 节点 →
// W_PLACEMENT_STATELESS_PIN（注册码）；有卷应用不提示。
func TestPlanWarningsStatelessPin(t *testing.T) {
	pinned := `
name: my-api
services:
  web:
    image: nginx
    labels:
      fleetly.placement.node: srv-01
`
	plan := planOf(t, "", pinned)
	found := false
	for _, w := range plan.Warnings {
		if w.Code == "W_PLACEMENT_STATELESS_PIN" && w.Service == "web" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing W_PLACEMENT_STATELESS_PIN: %+v", plan.Warnings)
	}

	volumed := `
name: my-api
services:
  web:
    image: nginx
    volumes: [data:/d]
    labels:
      fleetly.placement.node: srv-01
volumes:
  data:
`
	plan = planOf(t, "", volumed)
	for _, w := range plan.Warnings {
		if w.Code == "W_PLACEMENT_STATELESS_PIN" {
			t.Errorf("app with volumes should not get the stateless pin hint: %+v", plan.Warnings)
		}
	}
}

// TestRenderTextStable 人读渲染覆盖增删改三类（顺序确定）。
func TestRenderTextStable(t *testing.T) {
	// base: web+worker；target: web(变更)+worker2 → 增 worker2 / 删 worker / 改 web。
	target := `
name: my-api
services:
  web:
    image: nginx:1.27
    expose: ["8080"]
    environment:
      FOO: rotated-secret-value
      KEEP: same
    healthcheck:
      test: ["CMD", "hc"]
  worker2: { image: x }
`
	plan := planOf(t, diffTargetNewService, target)
	text := plan.RenderText()
	for _, want := range []string{"+ service worker2", "- service worker", "~ service web", "environment.FOO"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendering missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-value") {
		t.Error("human-readable rendering leaks env plaintext")
	}
}

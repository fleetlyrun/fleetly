package ingress

// 访问日志采集的入口侧输入面（E6 观测专项设计 §3.2，W5-S2）：
//   - buildTraefikSpec 钉住 --accesslog=true --accesslog.format=json
//     （JSON 行含 RouterName——hub 平台采集反解 app/service 的输入面）；
//   - 存量部署的收敛：args 漂移（缺 accesslog 参数）经 traefikSpecEqual
//     比对触发一次 service update，提交载荷携带全部期望参数。

import (
	"context"
	"testing"
)

// TestTraefikSpecPinsAccessLogArgs 期望 spec 精确携带两个 accesslog 参数
//（各恰好一次——重复参数会让漂移比对在每次 ensure 后仍报差异）。
func TestTraefikSpecPinsAccessLogArgs(t *testing.T) {
	m, _, _ := newTestManager(t)
	spec := m.buildTraefikSpec("http://127.0.0.1:8422", "tok")
	var accesslog, accesslogFormat int
	for _, a := range spec.TaskTemplate.ContainerSpec.Args {
		switch a {
		case "--accesslog=true":
			accesslog++
		case "--accesslog.format=json":
			accesslogFormat++
		}
	}
	if accesslog != 1 || accesslogFormat != 1 {
		t.Fatalf("accesslog args = (%d, %d), want exactly one of each in %v",
			accesslog, accesslogFormat, spec.TaskTemplate.ContainerSpec.Args)
	}
}

// TestEnsureTraefikConvergesAccessLogArgsDrift 存量部署升级收敛（W5-S1 之前
// 创建的服务实况无 accesslog 参数）：args 漂移触发更新，提交载荷携带两个
// accesslog 参数——下次 ensure 幂等（实况已等，不再产生更新调用）。
func TestEnsureTraefikConvergesAccessLogArgsDrift(t *testing.T) {
	m, dc, _ := newTestManager(t)
	ctx := context.Background()
	// 首次创建（当前期望 spec 已含 accesslog）。
	if err := m.EnsureTraefik(ctx); err != nil {
		t.Fatalf("create traefik: %v", err)
	}
	// 制造存量漂移：模拟 W5-S1 之前创建的服务（实况 args 缺 accesslog 参数）。
	svc := dc.services[IngressServiceName]
	var kept []string
	for _, a := range svc.Args {
		if a == "--accesslog=true" || a == "--accesslog.format=json" {
			continue
		}
		kept = append(kept, a)
	}
	svc.Args = kept
	dc.services[IngressServiceName] = svc

	if traefikSpecEqual(dc.serviceState(IngressServiceName), m.buildTraefikSpec("http://127.0.0.1:8422", "tok")) {
		t.Fatal("precondition: drifted args must compare unequal (missing accesslog args)")
	}
	if err := m.EnsureTraefik(ctx); err != nil {
		t.Fatalf("converge traefik: %v", err)
	}
	if len(dc.updateSpecs) == 0 {
		t.Fatal("drifted args must trigger exactly one convergence update, got none")
	}
	last := dc.updateSpecs[len(dc.updateSpecs)-1]
	var accesslog, accesslogFormat bool
	for _, a := range last.TaskTemplate.ContainerSpec.Args {
		if a == "--accesslog=true" {
			accesslog = true
		}
		if a == "--accesslog.format=json" {
			accesslogFormat = true
		}
	}
	if !accesslog || !accesslogFormat {
		t.Fatalf("convergence payload missing accesslog args: %v", last.TaskTemplate.ContainerSpec.Args)
	}
}

package api

// 域名资源面测试（IMPL-T1-1）：CRUD 回执与归一化、平台缺省（http/http01）、
// 形态校验、守卫④（host 冲突 409 / 超限 400 点名）、更新局部语义（空字段 =
// 保持现值）、删除与审计、scope 登记。服务直连单测（mgr = nil：无 ingress
// 装配形态——收敛面缺席不影响资源面语义）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newDomainsTestService 起独立 store + 域名服务（nil manager）。
func newDomainsTestService(t *testing.T) (*DomainsService, *state.Store, state.App) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	app, err := testsupport.SeedAppE(t, st, "domapp")
	if err != nil {
		t.Fatalf("SeedAppE: %v", err)
	}
	return NewDomainsService(st, nil), st, app
}

// TestDomainsServiceScopeRegistration scope 登记（新增 RPC 必须登记，改登记
// = 改测试）：写面 = deploy（路由声明属应用运行面写语义），读面 = read。
func TestDomainsServiceScopeRegistration(t *testing.T) {
	for method, want := range map[string]string{
		"/fleetly.server.v1.DomainsService/ListAppDomains":   ScopeRead,
		"/fleetly.server.v1.DomainsService/VerifyAppDomains": ScopeRead,
		"/fleetly.server.v1.DomainsService/CreateAppDomain":  ScopeDeploy,
		"/fleetly.server.v1.DomainsService/UpdateAppDomain":  ScopeDeploy,
		"/fleetly.server.v1.DomainsService/RemoveAppDomain":  ScopeDeploy,
	} {
		if got, ok := RequiredScope(method); !ok || got != want {
			t.Fatalf("scope(%s) = %q (registered %v), want %q", method, got, ok, want)
		}
	}
}

// TestDomainsServiceCreateNormalizesAndDefaults 创建面：host 归一化（小写/
// trim）+ 平台缺省（protocol=http / cert_mode=http01）+ List 投影新字段。
func TestDomainsServiceCreateNormalizesAndDefaults(t *testing.T) {
	svc, _, app := newDomainsTestService(t)
	ctx := directCtx(context.Background())

	resp, err := svc.CreateAppDomain(ctx, &serverv1.CreateAppDomainRequest{
		App: app.Name, Domain: " API.Example.Test ", Service: "web", Port: "9090",
		Protocol: "h2c", CertMode: "wildcard",
	})
	if err != nil {
		t.Fatalf("CreateAppDomain: %v", err)
	}
	if got := resp.GetDomain().GetDomain(); got != "api.example.test" {
		t.Fatalf("host not normalized: %q", got)
	}
	if resp.GetDomain().GetProtocol() != "h2c" || resp.GetDomain().GetCertMode() != "wildcard" {
		t.Fatalf("protocol/cert_mode not echoed: %+v", resp.GetDomain())
	}

	if _, err := svc.CreateAppDomain(ctx, &serverv1.CreateAppDomainRequest{
		App: app.Name, Domain: "plain.example.test", Service: "web", Port: "8080",
	}); err != nil {
		t.Fatalf("CreateAppDomain (defaults): %v", err)
	}
	list, err := svc.ListAppDomains(ctx, &serverv1.ListAppDomainsRequest{App: app.Name})
	if err != nil {
		t.Fatalf("ListAppDomains: %v", err)
	}
	if len(list.GetDomains()) != 2 {
		t.Fatalf("list size = %d, want 2", len(list.GetDomains()))
	}
	for _, d := range list.GetDomains() {
		if d.GetDomain() == "plain.example.test" {
			if d.GetProtocol() != "http" || d.GetCertMode() != "http01" {
				t.Fatalf("platform defaults missing: %+v", d)
			}
		}
	}
}

// TestDomainsServiceValidationAndLimits 守卫④（host 冲突/超限 4xx 点名）与
// 形态校验：冲突 409 E_DOMAIN_CONFLICT；通配主机 400 E_DOMAIN_UNSUPPORTED
// （reason=wildcard）；超限 400 E_DOMAIN_UNSUPPORTED（reason 区分 per-service/
// per-app）；port/protocol/cert_mode/service 形态违规 400 InvalidArgument。
func TestDomainsServiceValidationAndLimits(t *testing.T) {
	svc, _, app := newDomainsTestService(t)
	ctx := directCtx(context.Background())

	mk := func(domain, service, port, protocol, certMode string) error {
		_, err := svc.CreateAppDomain(ctx, &serverv1.CreateAppDomainRequest{
			App: app.Name, Domain: domain, Service: service, Port: port,
			Protocol: protocol, CertMode: certMode,
		})
		return err
	}
	if err := mk("one.example.test", "web", "8080", "", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// host 冲突：409 E_DOMAIN_CONFLICT（点名 host）。
	err := mk("one.example.test", "api", "9090", "", "")
	if env, ok := apperr.FromError(err); !ok || env.Code() != "E_DOMAIN_CONFLICT" || !strings.Contains(env.Message(), "one.example.test") {
		t.Fatalf("duplicate host err = %v, want E_DOMAIN_CONFLICT naming the host", err)
	}
	// 通配主机：E_DOMAIN_UNSUPPORTED reason=wildcard（app 级 DNS-01 沿 W5）。
	err = mk("*.wild.example.test", "web", "8080", "", "")
	if env, ok := apperr.FromError(err); !ok || env.Code() != "E_DOMAIN_UNSUPPORTED" || env.Context()["reason"] != "wildcard" {
		t.Fatalf("wildcard host err = %v, want E_DOMAIN_UNSUPPORTED reason=wildcard", err)
	}
	// 形态违规：400。
	for _, tc := range []struct {
		name                             string
		domain, service, port, proto, cm string
	}{
		{"bad protocol", "p.example.test", "web", "8080", "grpc", ""},
		{"bad cert mode", "c.example.test", "web", "8080", "", "acme"},
		{"bad port", "n.example.test", "web", "70000", "", ""},
		{"non numeric port", "n2.example.test", "web", "http", "", ""},
		{"bad service", "s.example.test", "-web", "8080", "", ""},
	} {
		err := mk(tc.domain, tc.service, tc.port, tc.proto, tc.cm)
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%s: err = %v (code %v), want InvalidArgument", tc.name, err, status.Code(err))
		}
	}
	// 每服务 ≤5：web 已有 one = 1，补 4 后第 6 个拒绝。
	for i := 0; i < 4; i++ {
		if err := mk("w"+string(rune('a'+i))+".example.test", "web", "8080", "", ""); err != nil {
			t.Fatalf("fill web (%d): %v", i, err)
		}
	}
	err = mk("wover.example.test", "web", "8080", "", "")
	if env, ok := apperr.FromError(err); !ok || env.Code() != "E_DOMAIN_UNSUPPORTED" || env.Context()["reason"] != "per_service_limit" {
		t.Fatalf("service limit err = %v, want E_DOMAIN_UNSUPPORTED reason=per_service_limit", err)
	}
	// 每 app ≤10：web 5 + api 5 后第 11 个拒绝（第三服务上触发 app 门）。
	for i := 0; i < 5; i++ {
		if err := mk("a"+string(rune('a'+i))+".example.test", "api", "9090", "", ""); err != nil {
			t.Fatalf("fill api (%d): %v", i, err)
		}
	}
	err = mk("aover.example.test", "worker", "8080", "", "")
	if env, ok := apperr.FromError(err); !ok || env.Code() != "E_DOMAIN_UNSUPPORTED" || env.Context()["reason"] != "per_app_limit" {
		t.Fatalf("app limit err = %v, want E_DOMAIN_UNSUPPORTED reason=per_app_limit", err)
	}
}

// TestDomainsServiceUpdateKeepsOmittedFieldsAndRemove 更新面：空字段保持现值
// （CLI 局部更新形态）；host 不改名；不存在 404；删除后 List 空、再删 404；
// 审计动作 domain.created/updated/removed 齐备。
func TestDomainsServiceUpdateKeepsOmittedFieldsAndRemove(t *testing.T) {
	svc, st, app := newDomainsTestService(t)
	ctx := directCtx(context.Background())
	if _, err := svc.CreateAppDomain(ctx, &serverv1.CreateAppDomainRequest{
		App: app.Name, Domain: "svc.example.test", Service: "web", Port: "8080",
		Protocol: "http", CertMode: "http01",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// 只给 port：service/protocol/cert_mode 保持现值。
	resp, err := svc.UpdateAppDomain(ctx, &serverv1.UpdateAppDomainRequest{
		App: app.Name, Domain: "svc.example.test", Port: "9090",
	})
	if err != nil {
		t.Fatalf("update port only: %v", err)
	}
	d := resp.GetDomain()
	if d.GetPort() != "9090" || d.GetService() != "web" || d.GetProtocol() != "http" || d.GetCertMode() != "http01" {
		t.Fatalf("update must keep omitted fields: %+v", d)
	}
	// 全量更新（含协议切换）。
	resp, err = svc.UpdateAppDomain(ctx, &serverv1.UpdateAppDomainRequest{
		App: app.Name, Domain: "svc.example.test", Service: "api", Port: "9091",
		Protocol: "h2c", CertMode: "wildcard",
	})
	if err != nil {
		t.Fatalf("update full: %v", err)
	}
	if got := resp.GetDomain(); got.GetService() != "api" || got.GetProtocol() != "h2c" || got.GetCertMode() != "wildcard" {
		t.Fatalf("full update wrong: %+v", got)
	}
	if _, err := svc.UpdateAppDomain(ctx, &serverv1.UpdateAppDomainRequest{App: app.Name, Domain: "missing.example.test", Port: "80"}); status.Code(err) != codes.NotFound {
		t.Fatalf("update missing domain err = %v, want NotFound", err)
	}
	if _, err := svc.RemoveAppDomain(ctx, &serverv1.RemoveAppDomainRequest{App: app.Name, Domain: "svc.example.test"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	list, err := svc.ListAppDomains(ctx, &serverv1.ListAppDomainsRequest{App: app.Name})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.GetDomains()) != 0 {
		t.Fatalf("domains remain after remove: %+v", list.GetDomains())
	}
	if _, err := svc.RemoveAppDomain(ctx, &serverv1.RemoveAppDomainRequest{App: app.Name, Domain: "svc.example.test"}); status.Code(err) != codes.NotFound {
		t.Fatalf("second remove err = %v, want NotFound", err)
	}
	// 审计：domain.created/updated/removed 三动作齐备（target = domain:<app>/<host>）。
	audits, err := st.RecentAudits(ctx, 50)
	if err != nil {
		t.Fatalf("recent audits: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range audits {
		if a.Target == "domain:"+app.Name+"/svc.example.test" {
			seen[a.Action] = true
		}
	}
	for _, action := range []string{"domain.created", "domain.updated", "domain.removed"} {
		if !seen[action] {
			t.Fatalf("audit action %s missing (seen: %v)", action, seen)
		}
	}
}

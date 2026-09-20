package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

// domains 台账读写测试（T2.15/T2.16）：对账 upsert/迁移归属/省略删除、
// 证书材料登记（sha256 + 到期）与未命中哨兵。

func TestReplaceAppDomainsLedgerReconcile(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// 首次声明：web 服务两域名。
	err = st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "web", Port: "8080", Domains: []string{"a.example.test", "b.example.test"}},
	})
	if err != nil {
		t.Fatalf("replace domains: %v", err)
	}
	rows, err := st.ListAppDomains(ctx, app.ID)
	if err != nil {
		t.Fatalf("list domains: %v", err)
	}
	if len(rows) != 2 || rows[0].Domain != "a.example.test" || rows[1].Domain != "b.example.test" {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if rows[0].Service != "web" || rows[0].Port != "8080" {
		t.Fatalf("row fields not persisted: %+v", rows[0])
	}

	// 二次声明：b 归属迁移到 api 服务 + 新增 c + 省略 a（删除）。
	err = st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "api", Port: "9000", Domains: []string{"b.example.test", "c.example.test"}},
	})
	if err != nil {
		t.Fatalf("replace domains 2: %v", err)
	}
	rows, err = st.ListAppDomains(ctx, app.ID)
	if err != nil {
		t.Fatalf("list domains: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after reconcile, got %d: %+v", len(rows), rows)
	}
	byDomain := map[string]Domain{}
	for _, r := range rows {
		byDomain[r.Domain] = r
	}
	if got := byDomain["b.example.test"]; got.Service != "api" || got.Port != "9000" {
		// 全列断言（MG-T2）：迁移后 service 与 port 必须一起切到新服务
		// （web:8080 → api:9000 后行必须是 api/9000），防止 port 残留。
		t.Fatalf("domain b should move to api/9000, got %+v", got)
	}
	if got := byDomain["c.example.test"]; got.Port != "9000" {
		t.Fatalf("domain c port not recorded: %+v", got)
	}
	if _, ok := byDomain["a.example.test"]; ok {
		t.Fatalf("omitted domain a must be deleted (omission = deletion), still present")
	}

	// 幂等：同声明重放零变化。
	err = st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "api", Port: "9000", Domains: []string{"b.example.test", "c.example.test"}},
	})
	if err != nil {
		t.Fatalf("idempotent replace: %v", err)
	}
	rows2, _ := st.ListAppDomains(ctx, app.ID)
	if len(rows2) != 2 {
		t.Fatalf("idempotent replay changed row count: %d", len(rows2))
	}
}

func TestReplaceAppDomainsPortOnlyChange(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "portchange")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// 首次声明：web:8080。
	if err := st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "web", Port: "8080", Domains: []string{"p.example.test"}},
	}); err != nil {
		t.Fatalf("replace domains: %v", err)
	}

	// 二次声明：service 不变、port 变化（web:8080 → web:9090）——
	// 跳过条件必须同时比对 service 与 port，否则台账残留旧端口。
	if err := st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "web", Port: "9090", Domains: []string{"p.example.test"}},
	}); err != nil {
		t.Fatalf("replace domains 2: %v", err)
	}
	rows, err := st.ListAppDomains(ctx, app.ID)
	if err != nil {
		t.Fatalf("list domains: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0].Service != "web" || rows[0].Port != "9090" {
		t.Fatalf("port-only change must rewrite port to 9090, got %+v", rows[0])
	}
}

func TestSetDomainCertLedger(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app, _ := st.CreateApp(ctx, "", "shop")
	if err := st.ReplaceAppDomains(ctx, app.ID, []DomainServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"shop.example.test"}},
	}); err != nil {
		t.Fatalf("replace domains: %v", err)
	}

	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := st.SetDomainCert(ctx, app.ID, "shop.example.test", "abc123", notAfter); err != nil {
		t.Fatalf("set cert: %v", err)
	}
	rows, _ := st.ListAppDomains(ctx, app.ID)
	if rows[0].CertSHA256 != "abc123" {
		t.Fatalf("cert sha not persisted: %+v", rows[0])
	}
	if !rows[0].CertNotAfter.Equal(notAfter) {
		t.Fatalf("cert not_after not persisted: %+v", rows[0])
	}
	if rows[0].CertUpdatedAt.IsZero() {
		t.Fatalf("cert_updated_at not stamped: %+v", rows[0])
	}

	// 未命中（域名已撤销后的签发竞态）→ ErrDomainNotFound。
	if err := st.SetDomainCert(ctx, app.ID, "gone.example.test", "abc123", notAfter); !errors.Is(err, ErrDomainNotFound) {
		t.Fatalf("missing domain should return ErrDomainNotFound, got %v", err)
	}

	// 台账删除（应用移除路径）。
	if err := st.DeleteAppDomains(ctx, app.ID); err != nil {
		t.Fatalf("delete app domains: %v", err)
	}
	rows, _ = st.ListAppDomains(ctx, app.ID)
	if len(rows) != 0 {
		t.Fatalf("rows remain after delete: %+v", rows)
	}
}

func TestListAllDomainsAcrossApps(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app1, _ := st.CreateApp(ctx, "", "one")
	app2, _ := st.CreateApp(ctx, "", "two")
	_ = st.ReplaceAppDomains(ctx, app1.ID, []DomainServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"one.example.test"}},
	})
	_ = st.ReplaceAppDomains(ctx, app2.ID, []DomainServiceRoutes{
		{Service: "web", Port: "80", Domains: []string{"two.example.test"}},
	})
	all, err := st.ListAllDomains(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 global rows, got %d", len(all))
	}
	if all[0].Domain >= all[1].Domain {
		t.Fatalf("rows not domain-ordered: %+v", all)
	}
}

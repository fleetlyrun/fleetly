package apptemplate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/spec"
)

// renderString 从渲染产物取嵌套字符串（测试探针）。
func renderString(t *testing.T, compose spec.ComposeDoc, path ...string) string {
	t.Helper()
	var node any = compose
	for _, key := range path[:len(path)-1] {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("path %v: node at %q is not a mapping", path, key)
		}
		node = m[key]
	}
	m, ok := node.(map[string]any)
	if !ok {
		t.Fatalf("path %v: terminal parent is not a mapping", path)
	}
	s, _ := m[path[len(path)-1]].(string)
	return s
}

// renderList 从渲染产物取字符串列表（测试探针）。
func renderList(t *testing.T, compose spec.ComposeDoc, path ...string) []string {
	t.Helper()
	node := renderAny(t, compose, path...)
	list, ok := node.([]any)
	if !ok {
		t.Fatalf("path %v: not a list", path)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func renderAny(t *testing.T, compose spec.ComposeDoc, path ...string) any {
	t.Helper()
	var node any = compose
	for _, key := range path {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("path %v: node at %q is not a mapping", path, key)
		}
		node = m[key]
	}
	return node
}

func TestParseRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown top-level", "x-fleetly-template:\n  name: a\n  version: 1.0.0\nnetworks: {}\n", "unsupported top-level field"},
		{"unknown meta field", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  author: bob\nservices:\n  web:\n    image: nginx:1\n", "unsupported field"},
		{"unknown variable field", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  variables:\n    - name: x\n      type: string\n      secret: true\nservices:\n  web:\n    image: nginx:1\n", "unsupported variable field"},
		{"unknown database field", "x-fleetly-template:\n  name: a\n  version: 1.0.0\nx-fleetly-databases:\n  - name: db\n    engine: postgres\n    size: big\nservices:\n  web:\n    image: nginx:1\n", "unsupported database field"},
		{"bad name", "x-fleetly-template:\n  name: A_1\n  version: 1.0.0\nservices:\n  web:\n    image: nginx:1\n", "must match"},
		{"missing meta block", "services:\n  web:\n    image: nginx:1\n", "must carry"},
		{"bad variable type", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  variables:\n    - name: x\n      type: int\nservices:\n  web:\n    image: nginx:1\n", "string, secret or domain"},
		{"secret with default", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  variables:\n    - name: x\n      type: secret\n      default: pw\nservices:\n  web:\n    image: nginx:1\n", "platform-generated"},
		{"duplicate variable", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  variables:\n    - name: x\n      type: string\n    - name: x\n      type: string\nservices:\n  web:\n    image: nginx:1\n", "duplicate variable"},
		{"bad engine", "x-fleetly-template:\n  name: a\n  version: 1.0.0\nx-fleetly-databases:\n  - name: db\n    engine: oracle\nservices:\n  web:\n    image: nginx:1\n", "not a registered database template"},
		{"route unknown protocol", "x-fleetly-template:\n  name: a\n  version: 1.0.0\n  variables:\n    - name: h\n      type: domain\n      required: true\n  routes:\n    - var: h\n      process: web\n      port: 80\n      protocol: grpc\nservices:\n  web:\n    image: nginx:1\n    ports: [\"80\"]\n", "http, h2c or tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("Parse accepted a bad document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRenderThreeVariableTypes(t *testing.T) {
	body := `
x-fleetly-template:
  name: demo
  version: 1.0.0
  variables:
    - name: title
      type: string
      description: page title
    - name: admin_password
      type: secret
    - name: host
      type: domain
      required: true
  routes:
    - var: host
      process: web
      port: 8080
x-fleetly-databases:
  - name: db
    engine: postgres
services:
  web:
    image: demo:${title}
    environment:
      TITLE: ${title}
      ADMIN_PASSWORD_FILE: ${admin_password}
    secrets: [admin_password, "database:db"]
    ports: ["8080"]
`
	doc, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := doc.Render(map[string]string{"title": "hello", "host": "demo.example.org"}, "myapp")
	if err != nil {
		t.Fatal(err)
	}
	// string 插值（镜像名与环境值）。
	if got := renderString(t, rendered.Compose, "services", "web", "image"); got != "demo:hello" {
		t.Fatalf("image interpolation: got %q", got)
	}
	if got := renderString(t, rendered.Compose, "services", "web", "environment", "TITLE"); got != "hello" {
		t.Fatalf("env interpolation: got %q", got)
	}
	// secret 插值 = 文件路径；值永不出现（反扫）。
	if got := renderString(t, rendered.Compose, "services", "web", "environment", "ADMIN_PASSWORD_FILE"); got != "/run/secrets/template:myapp:admin_password" {
		t.Fatalf("secret path interpolation: got %q", got)
	}
	// secrets 列表：变量名重写真名；字面引用（database:db）原样。
	secrets := renderList(t, rendered.Compose, "services", "web", "secrets")
	if len(secrets) != 2 || secrets[0] != "template:myapp:admin_password" || secrets[1] != "database:db" {
		t.Fatalf("secrets rewriting: got %v", secrets)
	}
	// domain 变量驱动 Route。
	if len(rendered.Routes) != 1 || rendered.Routes[0].Host != "demo.example.org" {
		t.Fatalf("route resolution: got %+v", rendered.Routes)
	}
	// 渲染产物经 NormalizeCompose 喉（白名单 + 校验叶子单源）。
	appSpec, err := spec.NormalizeCompose(rendered.Compose, "app-1", "proj-1")
	if err != nil {
		t.Fatal(err)
	}
	// secret 值零出现于 AppSpec（marshaled 反扫；路径/真名是引用非值）。
	if s := appSpec.String(); strings.Contains(s, "hex") || strings.Contains(s, "password value") {
		t.Fatalf("appspec suspiciously contains secret material")
	}
	if len(appSpec.Processes) != 1 || len(appSpec.Processes[0].SecretRefs) != 2 ||
		appSpec.Processes[0].SecretRefs[0] != "template:myapp:admin_password" {
		t.Fatalf("secret refs through normalize: %+v", appSpec.Processes[0].SecretRefs)
	}
	// 数据库声明在 Doc 上（实例化 create-or-reuse 消费）。
	if len(doc.Databases) != 1 || doc.Databases[0].Engine != "postgres" {
		t.Fatalf("database declarations: %+v", doc.Databases)
	}
}

func TestRenderFailClosed(t *testing.T) {
	base := `
x-fleetly-template:
  name: demo
  version: 1.0.0
  variables:
    - name: title
      type: string
      required: true
    - name: pw
      type: secret
    - name: host
      type: domain
      required: true
  routes:
    - var: host
      process: web
      port: 8080
services:
  web:
    image: demo:1
    environment:
      T: ${title}
      P: ${pw}
    secrets: [pw]
    ports: ["8080"]
`
	doc, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]string{"title": "t", "host": "h.example.org"}
	if _, err := doc.Render(good, "myapp"); err != nil {
		t.Fatalf("baseline render failed: %v", err)
	}
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"unknown value key", map[string]string{"title": "t", "host": "h.example.org", "extra": "x"}, "unknown variable"},
		{"secret value supplied", map[string]string{"title": "t", "host": "h.example.org", "pw": "hunter2"}, "platform-generated"},
		{"missing required", map[string]string{"host": "h.example.org"}, "is required"},
		{"missing domain (route)", map[string]string{"title": "t"}, "is required"},
		{"empty value", map[string]string{"title": "", "host": "h.example.org"}, "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := doc.Render(tc.values, "myapp")
			if err == nil {
				t.Fatalf("Render accepted bad values")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
	// 未声明变量的插值孔：模板作者笔误即拒（刷新预校验与实例化同一执法）。
	badHole := strings.Replace(base, "T: ${title}", "T: ${typo}", 1)
	badDoc, err := Parse([]byte(badHole))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badDoc.Render(good, "myapp"); err == nil || !strings.Contains(err.Error(), "undeclared variable") {
		t.Fatalf("undeclared interpolation hole accepted: %v", err)
	}
	// 插值孔出现在 secrets 条目内：与重写语义分立，即拒。
	holeSecret := strings.Replace(base, "secrets: [pw]", "secrets: [\"${pw}\"]", 1)
	holeDoc, err := Parse([]byte(holeSecret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holeDoc.Render(good, "myapp"); err == nil || !strings.Contains(err.Error(), "interpolation is not supported here") {
		t.Fatalf("interpolated secrets entry accepted: %v", err)
	}
	// route 指向未声明端口/未知服务。
	for _, mutate := range []struct {
		old, new string
		want     string
	}{
		{"port: 8080\nservices:", "port: 9999\nservices:", "not declared by service"},
		{"process: web", "process: worker", "not a compose service"},
	} {
		mutated, err := Parse([]byte(strings.Replace(base, mutate.old, mutate.new, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mutated.Render(good, "myapp"); err == nil || !strings.Contains(err.Error(), mutate.want) {
			t.Fatalf("route validation accepted %q: %v", mutate.new, err)
		}
	}
}

func TestBuiltinCatalogHealthy(t *testing.T) {
	entries, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("builtin catalog unexpectedly small: %d entries", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Name] {
			t.Fatalf("duplicate builtin template %s", e.Name)
		}
		seen[e.Name] = true
		if e.Digest != DigestOf(e.Body) {
			t.Fatalf("builtin %s digest mismatch", e.Name)
		}
		if _, err := Parse([]byte(e.Body)); err != nil {
			t.Fatalf("builtin %s failed to parse: %v", e.Name, err)
		}
	}
	if !seen["nginx"] || !seen["grafana"] {
		t.Fatalf("builtin catalog missing nginx/grafana: %v", seen)
	}
	// 内嵌模板渲染路径可端到端通（dry 链在 Builtin 内已走；此处显式钉 grafana
	// 的 secret 文件路径形态——ADR-0050 决策 2 的展示锚）。
	for _, e := range entries {
		doc, err := Parse([]byte(e.Body))
		if err != nil {
			t.Fatal(err)
		}
		values := map[string]string{}
		for _, v := range doc.Variables {
			if v.Type == VarDomain {
				values[v.Name] = "preview.invalid"
			}
		}
		rendered, err := doc.Render(values, "preview-app")
		if err != nil {
			t.Fatalf("builtin %s render: %v", e.Name, err)
		}
		if _, err := spec.NormalizeCompose(rendered.Compose, "preview-app", "preview-project"); err != nil {
			t.Fatalf("builtin %s normalize: %v", e.Name, err)
		}
	}
}

func TestFetchCatalogVerify(t *testing.T) {
	good, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	serve := func(mutate func(m *Manifest)) *httptest.Server {
		t.Helper()
		m := &Manifest{Version: 1, Templates: append([]ManifestEntry{}, good...)}
		mutate(m)
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/catalog.json" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(m)
		}))
	}
	t.Run("good manifest round-trips", func(t *testing.T) {
		srv := serve(func(m *Manifest) {})
		defer srv.Close()
		got, err := FetchCatalog(context.Background(), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(good) {
			t.Fatalf("entry count %d != %d", len(got), len(good))
		}
	})
	t.Run("digest mismatch rejected", func(t *testing.T) {
		srv := serve(func(m *Manifest) { m.Templates[0].Digest = "sha256:" + strings.Repeat("0", 64) })
		defer srv.Close()
		if _, err := FetchCatalog(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("tampered digest accepted: %v", err)
		}
	})
	t.Run("broken body rejected whole", func(t *testing.T) {
		srv := serve(func(m *Manifest) { m.Templates[len(m.Templates)-1].Body = "x-fleetly-template:\n  name: broken\n" })
		defer srv.Close()
		if _, err := FetchCatalog(context.Background(), srv.URL); err == nil {
			t.Fatal("broken entry accepted")
		}
	})
	t.Run("empty manifest rejected", func(t *testing.T) {
		srv := serve(func(m *Manifest) { m.Templates = nil })
		defer srv.Close()
		if _, err := FetchCatalog(context.Background(), srv.URL); !strings.Contains(err.Error(), "no templates") {
			t.Fatalf("empty manifest error: %v", err)
		}
	})
	t.Run("unsupported manifest version", func(t *testing.T) {
		srv := serve(func(m *Manifest) { m.Version = 2 })
		defer srv.Close()
		if _, err := FetchCatalog(context.Background(), srv.URL); !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("manifest version error: %v", err)
		}
	})
	t.Run("http failure surfaces", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()
		if _, err := FetchCatalog(context.Background(), srv.URL); !strings.Contains(err.Error(), "403") {
			t.Fatalf("http error: %v", err)
		}
	})
}

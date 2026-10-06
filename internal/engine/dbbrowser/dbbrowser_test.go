package dbbrowser

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
)

// digestRe 是钉定形态（ADR-0045 口径）：tag@sha256:<index digest> 双段。
var digestRefRe = regexp.MustCompile(`^[^@]+@sha256:[0-9a-f]{64}$`)

// TestBrowserDigestsPinned 是 digest 钉定门禁（ADR-0051 决策 4；
// TestImageDigestsPinned 同款）：全值域 Image() 恰为 tag@digest 形态、
// ImageDigest() 与之一致、非空。
func TestBrowserDigestsPinned(t *testing.T) {
	for _, engine := range Engines() {
		b, ok := For(engine)
		if !ok {
			t.Fatalf("engine %q listed but For() misses", engine)
		}
		img := b.Image()
		if !digestRefRe.MatchString(img) {
			t.Errorf("engine %q: Image() %q is not tag@sha256 form", engine, img)
		}
		dig := b.ImageDigest()
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(dig) {
			t.Errorf("engine %q: ImageDigest() %q malformed", engine, dig)
		}
		if want := img[len(img)-len(dig):]; want != dig {
			t.Errorf("engine %q: Image() digest segment %q != ImageDigest() %q", engine, want, dig)
		}
	}
}

// TestRegistryCoversDbtemplateEngines 钉死注册表 totality（ADR-0051 决策 3：
// E_BROWSER_UNSUPPORTED 只在新增引擎漏配 adapter 时可达——本测试先红）。
func TestRegistryCoversDbtemplateEngines(t *testing.T) {
	dbset := map[string]bool{}
	for _, e := range dbtemplate.Engines() {
		dbset[e] = true
		if _, ok := For(e); !ok {
			t.Errorf("dbtemplate engine %q has no browse browser mapped (E_BROWSER_UNSUPPORTED path is reachable)", e)
		}
	}
	// 反向：dbbrowser 不引入 dbtemplate 值域外的引擎（射程对齐）。
	for _, engine := range Engines() {
		if !dbset[engine] {
			t.Errorf("browse registry engine %q is outside dbtemplate value set", engine)
		}
	}
}

// TestRenderingsAreClean 是渲染面的密码卫生矩阵：文件方言（pgweb/adminer）
// 的 Command/Env 全域无密码；env 方言（redis-commander/mongoku）的密码只
// 在声明的连接 env 键内（方言边界白名单，ADR-0051 后果节记档——双向保鲜）。
func TestRenderingsAreClean(t *testing.T) {
	conn := Conn{Host: "db-01H", Port: 5432, User: "fleetly", Password: "s3cretPW123", DB: "fleetly"}
	// env 方言的连接串键（密码唯一合法落点）。
	connEnvKeys := map[string]bool{"REDIS_HOSTS": true, "MONGOKU_DEFAULT_HOST": true}
	for _, engine := range Engines() {
		b, _ := For(engine)
		readonly := b.ReadOnlyEnforcement() != EnforcementNone
		r, err := b.Render(conn, readonly)
		if err != nil {
			t.Fatalf("engine %q render: %v", engine, err)
		}
		envDialect := b.Name() == "redis-commander" || b.Name() == "mongoku"
		for k, v := range r.Env {
			if envDialect && connEnvKeys[k] {
				if !strings.Contains(v, conn.Password) {
					t.Errorf("engine %q: env %s expected to carry the connection password (dialect contract)", engine, k)
				}
				continue
			}
			if strings.Contains(v, conn.Password) {
				t.Errorf("engine %q: env %s leaks password", engine, k)
			}
		}
		for _, a := range r.Command {
			if strings.Contains(a, conn.Password) {
				t.Errorf("engine %q: argv leaks password: %q", engine, a)
			}
		}
		// 文件方言的材料必须在场（pgpass / db-conn.json+router.php）。
		switch b.Name() {
		case "pgweb":
			if _, ok := r.Files["pgpass"]; !ok {
				t.Errorf("engine %q: pgpass material missing", engine)
			}
		case "adminer":
			if _, ok := r.Files["db-conn.json"]; !ok {
				t.Errorf("engine %q: db-conn.json material missing", engine)
			}
			if _, ok := r.Files["router.php"]; !ok {
				t.Errorf("engine %q: router.php material missing", engine)
			}
		}
	}
}

// TestAdminerRouterPHPPinned 钉 vendored 胶水的 sha256（改动 = 刻意动作，
// 与 ADR 实证记录同 commit 更新）。
func TestAdminerRouterPHPPinned(t *testing.T) {
	sum := sha256.Sum256(AdminerRouterPHP())
	got := hex.EncodeToString(sum[:])
	const want = "9fb0bb60ace1ba024a9c00aa363f6f80c5569991040fcd1502717d99b3c96e01"
	if got != want {
		t.Errorf("vendored router.php sha256 drift: got %s want %s (deliberate change? update pin + ADR evidence)", got, want)
	}
}

// TestAdminerRejectsReadonly：EnforcementNone 的方言对 readonly 渲染
// fail-closed（受理面漏洞的双保险）。
func TestAdminerRejectsReadonly(t *testing.T) {
	conn := Conn{Host: "db-01H", Port: 3306, User: "fleetly", Password: "s3cretPW123", DB: "fleetly"}
	b, _ := For("mysql")
	if _, err := b.Render(conn, true); err == nil {
		t.Error("adminer Render(readonly=true) must fail closed (no read-only dialect)")
	}
}

// TestPgwebReadOnlyURLOptions 钉 pgweb 只读 URL 的服务端执法参数（空格
// %20 转义——libpq URI 不认 + 为空格）。
func TestPgwebReadOnlyURLOptions(t *testing.T) {
	conn := Conn{Host: "db-01H", Port: 5432, User: "fleetly", Password: "s3cretPW123", DB: "fleetly"}
	r, err := pgwebTemplate{}.Render(conn, true)
	if err != nil {
		t.Fatal(err)
	}
	var urlArg string
	for _, a := range r.Command {
		if strings.HasPrefix(a, "--url=") {
			urlArg = strings.TrimPrefix(a, "--url=")
		}
	}
	if urlArg == "" {
		t.Fatal("pgweb command carries no --url")
	}
	if !strings.Contains(urlArg, "options=-c%20default_transaction_read_only%3Don") {
		t.Errorf("readonly URL misses server-enforced options param: %s", urlArg)
	}
	if strings.Contains(urlArg, "+") {
		t.Errorf("URL carries + encoding (libpq does not decode + as space): %s", urlArg)
	}
	if strings.Contains(urlArg, conn.Password) {
		t.Errorf("URL leaks password: %s", urlArg)
	}
	// 写档形态：无 options 参数、无 --readonly。
	rw, err := pgwebTemplate{}.Render(conn, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rw.Command {
		if a == "--readonly" {
			t.Error("read-write render carries --readonly")
		}
		if strings.Contains(a, "options=") {
			t.Errorf("read-write URL carries read-only options: %s", a)
		}
	}
}

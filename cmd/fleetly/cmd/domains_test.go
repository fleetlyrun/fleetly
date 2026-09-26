package cmd

// 域名资源写面 CLI 测试（IMPL-T1-1）：add → set → list → rm 全链经 RPC
// （apitest 装配 DomainsService，mgr=nil——收敛面属夹具空缺，资源面语义
// 是对象）。

import (
	"strings"
	"testing"
)

func TestDomainsCRUDSurface(t *testing.T) {
	env := startCLI(t)
	env.CreateApp(t, "my-api")

	code, out, errOut := runCLIConn(t,
		"domains", "add", "--service", "web", "--port", "9090", "--protocol", "h2c",
		"my-api", "grpc.example.com")
	if code != 0 {
		t.Fatalf("domains add: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "grpc.example.com") || !strings.Contains(out, "h2c") {
		t.Fatalf("add output = %q", out)
	}

	// 局部更新：只改端口，协议保持 h2c。
	code, out, errOut = runCLIConn(t, "domains", "set", "--port", "9091", "my-api", "grpc.example.com")
	if code != 0 {
		t.Fatalf("domains set: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "9091") || !strings.Contains(out, "h2c") {
		t.Fatalf("set output = %q (omitted flags must keep current values)", out)
	}

	// 列表投影：新字段随 --json 输出。
	code, out, errOut = runCLIConn(t, "domains", "list", "--json", "my-api")
	if code != 0 {
		t.Fatalf("domains list: code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{`"protocol": "h2c"`, `"cert_mode": "http01"`, `"port": "9091"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output missing %s: %s", want, out)
		}
	}

	// host 冲突：同 host 二次创建点名拒绝（E_DOMAIN_CONFLICT）。
	code, _, errOut = runCLIConn(t,
		"domains", "add", "--service", "api", "--port", "8080", "my-api", "grpc.example.com")
	if code == 0 || !strings.Contains(errOut, "E_DOMAIN_CONFLICT") {
		t.Fatalf("duplicate add: code=%d stderr=%s, want E_DOMAIN_CONFLICT", code, errOut)
	}

	code, out, errOut = runCLIConn(t, "domains", "rm", "my-api", "grpc.example.com")
	if code != 0 {
		t.Fatalf("domains rm: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("rm output = %q", out)
	}
	code, out, _ = runCLIConn(t, "domains", "list", "--json", "my-api")
	if code != 0 || !strings.Contains(out, `"domains": []`) {
		t.Fatalf("list after rm = %q (code=%d)", out, code)
	}
}
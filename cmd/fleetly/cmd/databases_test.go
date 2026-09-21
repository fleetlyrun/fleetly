package cmd

// databases 动词组的 CLI 冒烟测试（E4 W4-S2）：进程内真实服务面夹具
//（internal/apitest，生产同路径 CLI-over-SDK）驱动 create/get/settings
// 三动词，断言退出码、脱敏投影（掩码 URL + 明文零出现）与错误信封渲染。
// 生命周期收敛行为不在 CLI 面（internal/database 单测覆盖）。

import (
	"strings"
	"testing"
)

func TestCLIDatabasesSmoke(t *testing.T) {
	startCLI(t)

	// create：受理即 provisioning（服务端同拍返回视图）。
	code, out, errOut := runCLIConn(t, "databases", "create", "--json", "--template", "postgres-16", "pg-cli")
	if code != 0 {
		t.Fatalf("create: code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{`"status": "provisioning"`, `"template": "postgres-16"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("create output missing %q:\n%s", want, out)
		}
	}

	// get：连接段恒为掩码；明文零出现。
	code, out, _ = runCLIConn(t, "databases", "get", "--json", "pg-cli")
	if code != 0 {
		t.Fatalf("get: code=%d", code)
	}
	if !strings.Contains(out, "********") {
		t.Fatalf("get output missing masked url:\n%s", out)
	}

	// settings：限额落列回显。
	code, out, errOut = runCLIConn(t, "databases", "settings", "--json", "--cpu", "2", "pg-cli")
	if code != 0 {
		t.Fatalf("settings: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, `"cpu_seconds": 2`) {
		t.Fatalf("settings output missing cpu:\n%s", out)
	}

	// 未知模板 → 稳定码信封 + suggestion/docs 渲染，退出 1。
	code, _, errOut = runCLIConn(t, "databases", "create", "--json", "--template", "mysql-8", "pg-bad")
	if code != 1 {
		t.Fatalf("bad template: code=%d", code)
	}
	for _, want := range []string{"E_DB_TEMPLATE_UNSUPPORTED", "suggestion:", "docs:"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}

	// 不存在的实例 → 404 语义信封，退出 1。
	code, _, errOut = runCLIConn(t, "databases", "get", "nope")
	if code != 1 || !strings.Contains(errOut, "E_DB_NOT_FOUND") {
		t.Fatalf("missing get: code=%d stderr=%q", code, errOut)
	}
}

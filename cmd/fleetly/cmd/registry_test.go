package cmd

// 平台 registry 凭证面 CLI 测试（IMPL-T1-2/DT-2）：show/set/clear 全链经
// RPC（apitest 装配 SystemService + box），并做明文泄露负面扫描（--json
// 输出不得含密码明文）。

import (
	"strings"
	"testing"
)

func TestRegistrySettingsCLI(t *testing.T) {
	startCLI(t)

	// 未配置缺省态。
	code, out, errOut := runCLIConn(t, "registry", "show")
	if code != 0 {
		t.Fatalf("registry show: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "not configured") {
		t.Fatalf("show output = %q, want the not-configured note", out)
	}

	// 保存（含密码）：输出 host 与指纹，不出现密码明文。
	code, out, errOut = runCLIConn(t, "registry", "set",
		"--host", "https://GHCR.io/", "--username", "robot", "--password", "cli-PASSWORD-MARKER")
	if code != 0 {
		t.Fatalf("registry set: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "host=ghcr.io") || !strings.Contains(out, "username=robot") ||
		!strings.Contains(out, "password fingerprint=") {
		t.Fatalf("set output = %q", out)
	}
	if strings.Contains(out, "cli-PASSWORD-MARKER") {
		t.Fatalf("set output leaks the password: %q", out)
	}

	// 读面：指纹回读；--json 同样无明文。
	code, out, errOut = runCLIConn(t, "registry", "show", "--json")
	if code != 0 {
		t.Fatalf("registry show --json: code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{`"host": "ghcr.io"`, `"username": "robot"`, `"password_fingerprint"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("show --json missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, "cli-PASSWORD-MARKER") {
		t.Fatalf("show --json leaks the password: %q", out)
	}

	// 留空保留语义：改用户名不重录密码——指纹不变。
	code, out, errOut = runCLIConn(t, "registry", "set", "--host", "ghcr.io", "--username", "robot2")
	if code != 0 {
		t.Fatalf("registry set (keep password): code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "username=robot2") || !strings.Contains(out, "password fingerprint=") {
		t.Fatalf("keep-password output = %q", out)
	}

	// 非法 host：服务端 400 点名（CLI 退出码 1 + stderr 文案）。
	code, _, errOut = runCLIConn(t, "registry", "set", "--host", "ghcr.io/owner")
	if code == 0 || !strings.Contains(errOut, "bare host") {
		t.Fatalf("invalid host: code=%d stderr=%s, want explicit rejection", code, errOut)
	}

	// 清除：show 回落未配置；set 缺 --host 提示用 clear。
	code, out, errOut = runCLIConn(t, "registry", "clear")
	if code != 0 {
		t.Fatalf("registry clear: code=%d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "cleared") {
		t.Fatalf("clear output = %q", out)
	}
	code, out, _ = runCLIConn(t, "registry", "show")
	if code != 0 || !strings.Contains(out, "not configured") {
		t.Fatalf("show after clear = %q (code=%d), want the not-configured note", out, code)
	}
	code, _, errOut = runCLIConn(t, "registry", "set", "--username", "robot3")
	if code == 0 || !strings.Contains(errOut, "--host is required") {
		t.Fatalf("set without host: code=%d stderr=%s, want usage error", code, errOut)
	}
}

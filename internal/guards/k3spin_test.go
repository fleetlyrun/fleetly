package guards

import (
	"regexp"
	"strings"
	"testing"
)

// TestK3sPinConstantAndE2EAgree：k3s 钉版三链一致（ADR-0052 决策 8：
// 平台常量单源 + e2e 下载段 + 守卫同 commit 纪律）。k3s 版本无 Go 侧消费
// （Provider 面版本无关），常量本体住 e2e 脚本——守卫断言变量在位、形态
// 合法（v<semver>+k3s<N> / sha256 64hex），并与 ADR 文本中的钉版陈述
// 双向保鲜（ADR 换版不同步 e2e 即红）。
func TestK3sPinConstantAndE2EAgree(t *testing.T) {
	const e2eFile = "e2e/dind-k3s.sh"
	src := readFileLF(t, e2eFile)

	verDecl := regexp.MustCompile(`K3S_VERSION="(v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+)"`).FindStringSubmatch(src)
	if len(verDecl) != 2 {
		t.Fatalf("%s no longer declares the pinned K3S_VERSION constant — the pin moved; update this guard with it", e2eFile)
	}
	pin := verDecl[1]

	sumDecl := regexp.MustCompile(`K3S_SHA256="([0-9a-f]{64})"`).FindStringSubmatch(src)
	if len(sumDecl) != 2 {
		t.Fatalf("%s no longer declares the K3S_SHA256 verification constant — binary pinning without checksum is drift; restore it (ADR-0052 decision 8)", e2eFile)
	}

	// 下载段模板在位（版本变量被消费——防常量成死量；sh 无 bash 参数
	// 展开，+ 转义经 sed 通道）。
	if !strings.Contains(src, `sed 's/+/%2B/'`) {
		t.Errorf("%s no longer escapes the k3s asset + sign via sed — the pinned download URL template changed; keep it in sync (ADR-0052 decision 8)", e2eFile)
	}
	if !strings.Contains(src, `releases/download/$K3S_ASSET/k3s"`) {
		t.Errorf("%s no longer downloads the pinned k3s asset by K3S_ASSET — keep the pinned download step in sync (ADR-0052 decision 8)", e2eFile)
	}

	// ADR 双向保鲜：决策 8 的钉版陈述与脚本常量一致。
	adr := readFileLF(t, "docs/adr/0052-k3s-second-runtime-pilot.md")
	if !strings.Contains(adr, pin) {
		t.Errorf("ADR-0052 decision 8 no longer names the pinned k3s version %q — bump the ADR text with the e2e constant in the same commit (ADR-0052 decision 8)", pin)
	}
}

// TestPostgresDigestPinAgreement：e2e 两腿 airgap 预载的 postgres digest 与
// dbtemplate.postgresImageDigest 同源双向保鲜——k3s 侧 containerd 经代理
// 在线拉 digest 不稳（12 分钟窗超时实证，2026-10-07），预载是 db 段的
// 确定性前提；模板换版不同步 e2e 即红（同 commit 纪律）。
func TestPostgresDigestPinAgreement(t *testing.T) {
	tplSrc := readFileLF(t, "internal/engine/dbtemplate/postgres.go")
	tpl := regexp.MustCompile(`postgresImageDigest = "(sha256:[0-9a-f]{64})"`).FindStringSubmatch(tplSrc)
	if len(tpl) != 2 {
		t.Fatal("internal/engine/dbtemplate/postgres.go no longer declares the postgresImageDigest constant — the pin moved; update this guard with it")
	}
	for _, f := range []string{"e2e/dind-k3s.sh", "e2e/dind-runtimeswitch.sh"} {
		if !strings.Contains(readFileLF(t, f), tpl[1]) {
			t.Errorf("%s no longer stages the pinned postgres digest %s — dbtemplate bumped without syncing the e2e airgap preload (same-commit discipline)", f, tpl[1])
		}
	}
}

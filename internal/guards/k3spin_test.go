package guards

import (
	"regexp"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
)

// TestK3sPinConstantAndE2EAgree：k3s 钉版三链一致（ADR-0052 决策 8：
// 平台常量单源 + e2e 下载段 + 契约测试同 commit 纪律）。k3s 版本无 Go 侧消费
// （Provider 面版本无关），常量本体住 e2e 脚本——契约测试断言变量在位、形态
// 合法（v<semver>+k3s<N> / sha256 64hex），并与 ADR 文本中的钉版陈述
// 双向保鲜（ADR 换版不同步 e2e 即红）。
func TestK3sPinConstantAndE2EAgree(t *testing.T) {
	// 三条 k3s e2e 腿同款钉版（two-node 随 ADR-0053、HA 随 ADR-0056 落地）
	// ——逐脚本断言且互相一致（单脚本换版不同步另一腿即红）。
	e2eFiles := []string{"e2e/dind-k3s.sh", "e2e/dind-k3s-two-node.sh", "e2e/dind-k3s-ha.sh"}
	var firstPin, firstSum string
	for _, e2eFile := range e2eFiles {
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
		if firstPin == "" {
			firstPin, firstSum = pin, sumDecl[1]
			continue
		}
		if pin != firstPin || sumDecl[1] != firstSum {
			t.Errorf("%s pins k3s %s but %s pins %s — the two e2e legs must carry the same pin in the same commit", e2eFile, pin, e2eFiles[0], firstPin)
		}
	}

	// ADR 双向保鲜：决策 8 的钉版陈述与脚本常量一致。
	adr := readFileLF(t, "docs/adr/0052-k3s-second-runtime-pilot.md")
	if !strings.Contains(adr, firstPin) {
		t.Errorf("ADR-0052 decision 8 no longer names the pinned k3s version %q — bump the ADR text with the e2e constant in the same commit (ADR-0052 decision 8)", firstPin)
	}
}

// TestPostgresDigestPinAgreement：e2e 三腿 airgap 预载的 postgres digest 与
// databasetemplate 注册表**默认版本**同源双向保鲜——k3s 侧 containerd 经代理
// 在线拉 digest 不稳（12 分钟窗超时实证，2026-10-07），预载是 db 段的
// 确定性前提；模板换版（含 B2a 起 Default 翻转/版本矩阵 digest bump）不同步
// e2e 即红（同 commit 纪律）。B2a 起注册表经 For("postgres","") 程序化解析
// （绑版实例 = 部署引用真源），不再 regex 源码常量形态。
func TestPostgresDigestPinAgreement(t *testing.T) {
	tpl, err := databasetemplate.For("postgres", "")
	if err != nil {
		t.Fatalf("databasetemplate.For(postgres default) failed: %v", err)
	}
	ref, digest, _ := strings.Cut(tpl.Image(), "@") // tag@digest 双段律：首切即唯一切
	for _, f := range []string{"e2e/dind-k3s.sh", "e2e/dind-runtimeswitch.sh", "e2e/dind-upgrade.sh"} {
		src := readFileLF(t, f)
		if !strings.Contains(src, digest) {
			t.Errorf("%s no longer stages the pinned postgres digest %s — databasetemplate bumped without syncing the e2e airgap preload (same-commit discipline)", f, digest)
		}
		if !strings.Contains(src, ref) {
			t.Errorf("%s no longer names the default postgres reference %q — the default version flipped without syncing the e2e preload (same-commit discipline)", f, ref)
		}
	}
}

package guards

// F1.14（ADR-0032 决策 4）同 commit 纪律的静态执法：railpack 钉版常量与
// install.sh 下载 URL 的版本段必须一致——改代码常量不改安装脚本（或反之）
// 即红。钉版可被漂移等于没钉。

import (
	"regexp"
	"strings"
	"testing"
)

// TestRailpackPinConstantAndInstallerAgree：install.sh 含
// railpack-v<常量> 资产下载；常量本体在 providers/builders 单源在位。
func TestRailpackPinConstantAndInstallerAgree(t *testing.T) {
	const providerFile = "internal/providers/builders/railpack.go"
	src := readFileLF(t, providerFile)
	decl := regexp.MustCompile(`const railpackPinnedVersion = "([0-9]+\.[0-9]+\.[0-9]+)"`).FindStringSubmatch(src)
	if len(decl) != 2 {
		t.Fatalf("%s no longer declares the railpackPinnedVersion constant — the pin moved; update this guard with it", providerFile)
	}
	pin := decl[1]

	install := readFileLF(t, "install.sh")
	// URL 模板段（变量展开形态）+ 版本变量字面量双断言：模板缺失=安装段
	// 被重构掉；变量漂移=版本不同步。
	if !strings.Contains(install, `railpack-v${RAILPACK_VERSION}-`) {
		t.Errorf("install.sh no longer downloads the versioned railpack asset (asset literal railpack-v${RAILPACK_VERSION}- is gone) — keep the pinned install step in sync (ADR-0032 decision 4)")
	}
	if !strings.Contains(install, `RAILPACK_VERSION="`+pin+`"`) {
		t.Errorf("install.sh RAILPACK_VERSION does not match the platform pin %q — bump the installer with the constant in the same commit (ADR-0032 decision 4)", pin)
	}
}

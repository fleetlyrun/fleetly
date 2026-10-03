package guards

// F2.2（ADR-0039 决策 9）同 commit 纪律的静态执法：restic 钉版常量与
// install.sh 下载段的版本变量必须一致——railpack 钉版守卫同款范式
//（ADR-0032 决策 4）。钉版可被漂移等于没钉。

import (
	"regexp"
	"strings"
	"testing"
)

// TestResticPinConstantAndInstallerAgree：install.sh 含
// restic_<常量> 资产下载；常量本体在 engine 单源在位。
func TestResticPinConstantAndInstallerAgree(t *testing.T) {
	const engineFile = "internal/engine/platformbackup.go"
	src := readFileLF(t, engineFile)
	decl := regexp.MustCompile(`const PinnedResticVersion = "([0-9]+\.[0-9]+\.[0-9]+)"`).FindStringSubmatch(src)
	if len(decl) != 2 {
		t.Fatalf("%s no longer declares the PinnedResticVersion constant — the pin moved; update this guard with it", engineFile)
	}
	pin := decl[1]

	install := readFileLF(t, "install.sh")
	if !strings.Contains(install, `restic_${RESTIC_VERSION}_linux_`) {
		t.Errorf("install.sh no longer downloads the versioned restic asset (asset literal restic_${RESTIC_VERSION}_linux_ is gone) — keep the pinned install step in sync (ADR-0039 decision 9)")
	}
	if !strings.Contains(install, `RESTIC_VERSION="`+pin+`"`) {
		t.Errorf("install.sh RESTIC_VERSION does not match the platform pin %q — bump the installer with the constant in the same commit (ADR-0039 decision 9)", pin)
	}
}

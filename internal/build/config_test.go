package build

// Config 归一化测试：构建超时预算（config 键 build.timeout_seconds 的核心
// 形态）——零值/负值回落缺省 30min，显式配置原样保留（缺省值单一事实源
// 在 Normalize）；受管根集合（H14）归一——系统 temp 根恒在、显式根
// Abs+Clean、去重。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigTimeoutNormalize 超时预算缺省回落与显式覆盖。
func TestConfigTimeoutNormalize(t *testing.T) {
	if got := (Config{}).Normalize().Timeout; got != DefaultTimeout {
		t.Fatalf("zero-value Timeout = %s, want default %s", got, DefaultTimeout)
	}
	if got := (Config{Timeout: -time.Second}).Normalize().Timeout; got != DefaultTimeout {
		t.Fatalf("negative Timeout = %s, want default %s", got, DefaultTimeout)
	}
	const want = 90 * time.Second
	if got := (Config{Timeout: want}).Normalize().Timeout; got != want {
		t.Fatalf("explicit Timeout = %s, want %s", got, want)
	}
}

// TestConfigContextRootsNormalize 受管根归一（H14）：零值回落系统 temp 根
// 单根集合；显式根 Abs 归一 + 去重；temp 根恒在（显式根之外自动并入）。
func TestConfigContextRootsNormalize(t *testing.T) {
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatalf("abs temp dir: %v", err)
	}
	// 零值：[系统 temp 根]。
	if got := (Config{}).Normalize().ContextRoots; len(got) != 1 || got[0] != tempRoot {
		t.Fatalf("zero-value ContextRoots = %v, want [%s]", got, tempRoot)
	}
	// 显式根：Abs+Clean 归一并去重；temp 根恒在。
	extra := t.TempDir()
	got := (Config{ContextRoots: []string{extra, extra, "", tempRoot}}).Normalize().ContextRoots
	if len(got) != 2 {
		t.Fatalf("explicit ContextRoots = %v, want temp+extra 去重后 2 根", got)
	}
	hasTemp, hasExtra := false, false
	for _, r := range got {
		switch r {
		case tempRoot:
			hasTemp = true
		case filepath.Clean(extra):
			hasExtra = true
		}
	}
	if !hasTemp || !hasExtra {
		t.Fatalf("ContextRoots = %v, want temp(%v)+extra(%v)", got, hasTemp, hasExtra)
	}
}

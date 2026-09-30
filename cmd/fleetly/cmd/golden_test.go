package cmd

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/internal/buildinfo"
)

// golden 双形态钉死：每动词至少一例人类形态 + 一例 --json 形态；再生成
// 用 `go test ./cmd/fleetly/cmd -update`（约定见 AGENTS.md）。status 等
// 远程动词的 golden 随进程内 apitest 夹具（bufconn）落地。
var goldenUpdate = flag.Bool("update", false, "rewrite golden snapshot files")

// testBuildInfo 是 golden 夹具的确定性版本三元组。
var testBuildInfo = buildinfo.BuildInfo{
	Version: "0.1.0-test",
	Commit:  "deadbeef",
	Date:    "20260930000000",
}

// runCLI 进程内驱动完整 CLI（真实 commands.App 装配），返回退出码与
// stdout/stderr。
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := &commands.Environment{Stdout: &stdout, Stderr: &stderr}
	code := NewApp(testBuildInfo).Run(context.Background(), env, args)
	return code, stdout.String(), stderr.String()
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".golden")
	if *goldenUpdate {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { //nolint:gosec // 测试产物目录
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil { //nolint:gosec // golden 快照非机密
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有夹具
	if err != nil {
		t.Fatalf("golden %s missing (run `go test ./cmd/fleetly/cmd -update`): %v", name, err)
	}
	if string(want) != got {
		t.Errorf("output drifted from golden %s:\n--- golden ---\n%s--- got ---\n%s", name, want, got)
	}
}

func TestGoldenVersion(t *testing.T) {
	code, out, stderr := runCLI(t, "version")
	if code != 0 || stderr != "" {
		t.Fatalf("version: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "version", out)
}

func TestGoldenVersionJSON(t *testing.T) {
	code, out, stderr := runCLI(t, "version", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("version --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "version-json", out)
}

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoHeartbeatWording 文案断言（state-model §2.2：Swarm 不暴露心跳
// 时间戳，平台不承诺「最后心跳」——nodes 表语义字段为 last_seen_at，
// 代码与注释不得出现「心跳」字样）。扫描范围 = 本阶段交付面：
// internal/state、internal/substrate、cmd/fleetlyd、internal/eventcode 与
// config-example.yaml（2026-09-20 命名审查：eventcode 历史遗留的
// node.down 摘要已改写，扫描面同步扩入，不再豁免）。
func TestNoHeartbeatWording(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "..", "internal", "state"),
		filepath.Join("..", "..", "internal", "substrate"),
		filepath.Join("..", "..", "cmd", "fleetlyd"),
		filepath.Join("..", "..", "internal", "eventcode"),
	}
	files := []string{filepath.Join("..", "..", "config-example.yaml")}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), ".sql") &&
				!strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	banned := []string{"心跳", "heartbeat", "Heartbeat", "HEARTBEAT", "last_heartbeat", "last-heartbeat"}
	for _, f := range files {
		// 本测试文件自身豁免：禁词表与本断言的说明注释必然含该词
		// （扫描器自指问题）；豁免只限本文件。
		if strings.HasSuffix(f, "wording_test.go") {
			continue
		}
		raw, err := os.ReadFile(f) //nolint:gosec // 路径为仓内固定目录枚举（文案断言扫描）
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(raw)
		for _, word := range banned {
			if strings.Contains(content, word) {
				t.Errorf("%s contains banned wording %q（平台不承诺节点心跳时间戳，用 last_seen_at 观测语义）", f, word)
			}
		}
	}
}

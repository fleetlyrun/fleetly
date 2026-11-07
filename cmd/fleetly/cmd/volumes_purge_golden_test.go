package cmd

// volumes purge golden（B7，ADR-0094 决策 5 升级正典③）：wide 档确认三形
// 态——--preview（清单+容量+后果段+票行，两调用流第 1 步）/ --ticket 兑换
// / --yes skip 通道 / 交互形态（伪 TTY y/N）。容量行经 FakeRuntime 载体集
// live 回报（4096 字节）；确认票 43 字符 base64url 形占位化（confirm 族
// 先例）。再生成：go test ./cmd/fleetly/cmd -update。

import (
	"encoding/json"
	"strings"
	"testing"
)

// seedPurgeVolume 建卷 + 假物理载体（容量 4096），回显卷 id。
func seedPurgeVolume(t *testing.T, projectID, name string) string {
	t.Helper()
	code, out, stderr := runCLI(t, "volumes", "create", "--project", projectID, name)
	if code != 0 || stderr != "" {
		t.Fatalf("seed volume: code=%d stderr=%q", code, stderr)
	}
	return extractTailID(out)
}

// purgePreviewJSON 取 --preview --json 的原始输出并解出票（兑换腿用——
// 票逐次随机，golden 面占位化）。
func purgePreviewJSON(t *testing.T, volumeID string) (raw string, ticket string) {
	t.Helper()
	code, out, stderr := runCLI(t, "volumes", "purge", "--preview", "--json", volumeID)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge --preview --json: code=%d stderr=%q", code, stderr)
	}
	var payload struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("purge preview json: %v", err)
	}
	return out, payload.Ticket
}

func TestGoldenVolumesPurgePreview(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "volpurge")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	volumeID := seedPurgeVolume(t, projectID, "pgshop")
	h.Runtime.SetVolumeCarriers(map[string]int64{volumeID: 4096})

	// --preview 人类形态：purge 清单 + 容量行 + 后果段 + 票行。
	code, out, stderr = runCLI(t, "volumes", "purge", "--preview", volumeID)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-preview", normalizeConfirmGolden(out))

	// --preview --json：PreviewPurgeVolumeResponse protojson。
	_, _ = purgePreviewJSON(t, volumeID)
	raw, _ := purgePreviewJSON(t, volumeID)
	compareGolden(t, "volumes-purge-preview-json", normalizeConfirmGolden(raw))
}

// TestGoldenVolumesPurgeDualForm 是动词级双形态钉（coverage 守卫的
// volumes-purge/-json golden 源）：--ticket 兑换腿两形态。
func TestGoldenVolumesPurgeDualForm(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "volpurge-dual")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	dualFormRound(t, "volumes purge", func(label string) []string {
		volumeID := seedPurgeVolume(t, projectID, "dual-"+label)
		h.Runtime.SetVolumeCarriers(map[string]int64{volumeID: 4096})
		_, ticket := purgePreviewJSON(t, volumeID)
		return []string{"volumes", "purge", "--ticket", ticket, volumeID}
	})
}

func TestGoldenVolumesPurgeTicketedAndSkip(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "volpurge2")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)

	// --ticket 兑换腿（两调用流第 2 步）。
	first := seedPurgeVolume(t, projectID, "pgshop")
	h.Runtime.SetVolumeCarriers(map[string]int64{first: 4096})
	_, ticket := purgePreviewJSON(t, first)
	code, out, stderr = runCLI(t, "volumes", "purge", "--ticket", ticket, first)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge --ticket: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-ticket", normalizeConfirmGolden(out))

	// --yes skip 通道 + 容量不可得的诚实缺席（载体缺席 → usage_known=false）。
	second := seedPurgeVolume(t, projectID, "standalone")
	code, out, stderr = runCLI(t, "volumes", "purge", "--yes", second)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-yes", normalizeConfirmGolden(out))

	// --json 兑换腿：PurgeVolumeResponse protojson。
	third := seedPurgeVolume(t, projectID, "pgshop3")
	h.Runtime.SetVolumeCarriers(map[string]int64{third: 4096})
	_, ticket = purgePreviewJSON(t, third)
	code, out, stderr = runCLI(t, "volumes", "purge", "--ticket", ticket, "--json", third)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge --ticket --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-ticket-json", normalizeConfirmGolden(out))
}

func TestGoldenVolumesPurgeInteractive(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "volpurge3")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	volumeID := seedPurgeVolume(t, projectID, "pgshop")
	h.Runtime.SetVolumeCarriers(map[string]int64{volumeID: 4096})

	// 交互形态：伪 TTY y → 预览 + 提示 + 兑换一回执。
	fakeConfirmTTY(t, "y")
	code, out, stderr = runCLI(t, "volumes", "purge", volumeID)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge interactive: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-interactive", normalizeConfirmGolden(out))

	// 交互拒绝腿：N → 零写请求（预览照出，回执只有 aborted 行）。
	other := seedPurgeVolume(t, projectID, "pgshop2")
	h.Runtime.SetVolumeCarriers(map[string]int64{other: 4096})
	fakeConfirmTTY(t, "n")
	code, out, stderr = runCLI(t, "volumes", "purge", other)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes purge interactive (declined): code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, "aborted (nothing was sent)") {
		t.Fatalf("declined purge must print the aborted line, got: %s", out)
	}
}

func TestGoldenVolumesPurgeNonTTYRefusal(t *testing.T) {
	_ = newGoldenHarness(t)
	_, out, _ := runCLI(t, "projects", "create", "volpurge4")
	projectID := extractTailID(out)
	volumeID := seedPurgeVolume(t, projectID, "pgshop")

	origTTY := confirmIsTTY
	confirmIsTTY = func() bool { return false }
	t.Cleanup(func() { confirmIsTTY = origTTY })
	code, _, stderr := runCLI(t, "volumes", "purge", volumeID)
	if code == 0 || stderr == "" {
		t.Fatalf("non-TTY bare purge must refuse locally, got code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-purge-nontty", normalizeConfirmGolden(stderr))
}

package cmd

// volumes delete golden（IA v3 二期⑤b）：成功面双形态（人类一行 / --json
// 空响应对象）+ 精确拒绝面双形态——未知行 E_NOT_FOUND（缺对象仓不影响）；
// 被引用 E_CONFLICT 的报文面由 API 层 handler 与 engine/承载测试覆盖
// （golden harness 无冻结 Spec 播种面）。
//
// S18b 补成功面：此前只有拒绝面 golden，成功删行（平台行 tombstone、底层
// 编排器卷不随删）的输出契约无钉死。

import (
	"strings"
	"testing"
)

// TestGoldenVolumesDeleteSuccess：建卷 → 按 id 删 → 人类形态钉死「已删 +
// 编排器卷残留披露」；第二个卷走 --json 形态（DeleteVolumeResponse 空对象
// = 机器契约）；再删同一 id 落既有 E_NOT_FOUND golden——删除事实的独立
// 断言（不是只看成功输出）。
func TestGoldenVolumesDeleteSuccess(t *testing.T) {
	_ = newGoldenHarness(t)

	code, out, stderr := runCLI(t, "projects", "create", "vols")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)

	code, out, stderr = runCLI(t, "volumes", "create", "--project", projectID, "data")
	if code != 0 || stderr != "" {
		t.Fatalf("seed volume: code=%d stderr=%q", code, stderr)
	}
	volumeID := extractTailID(out)

	// 人类形态：成功删行，输出披露底层编排器卷不随删（数据兜底永不级联）。
	code, out, stderr = runCLI(t, "volumes", "delete", volumeID)
	if code != 0 || stderr != "" {
		t.Fatalf("volumes delete: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-delete-success", normalizeGolden(out))

	// 删除事实：同 id 再删 = E_NOT_FOUND（复用拒绝面 golden 的同一信封）。
	code, out, stderr = runCLI(t, "volumes", "delete", volumeID)
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("deleting a deleted volume: code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "volumes-delete", normalizeGolden(stderr))

	// --json 形态：第二个卷 → DeleteVolumeResponse 空对象。
	code, out, stderr = runCLI(t, "volumes", "create", "--project", projectID, "cache")
	if code != 0 || stderr != "" {
		t.Fatalf("seed second volume: code=%d stderr=%q", code, stderr)
	}
	cacheID := extractTailID(out)
	code, out, stderr = runCLI(t, "volumes", "delete", cacheID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("volumes delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "volumes-delete-success-json", normalizeGolden(out))
}

func TestGoldenVolumesDeleteUnknown(t *testing.T) {
	_ = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "volumes", "delete", "missing-backup-9")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("volumes delete (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "volumes-delete", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "volumes", "delete", "missing-backup-9", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("volumes delete --json (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "volumes-delete-json", normalizeGolden(stderr))
}

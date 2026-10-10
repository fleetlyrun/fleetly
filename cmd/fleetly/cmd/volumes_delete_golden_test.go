package cmd

// volumes delete golden（IA v3 二期⑤b）：精确拒绝面双形态——未知行
// E_NOT_FOUND（缺对象仓不影响）；被引用 E_CONFLICT 的报文面由 API 层
// handler 与 engine/承载测试覆盖（golden harness 无冻结 Spec 播种面）。

import (
	"strings"
	"testing"
)

func TestGoldenVolumesDeleteUnknown(t *testing.T) {
	_ = newGoldenHarness(t)
	code, out, stderr := runCLI(t, "volumes", "delete", "01JD0BKPMISSING000000000001")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("volumes delete (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "volumes-delete", normalizeGolden(stderr))
	code, out, stderr = runCLI(t, "volumes", "delete", "01JD0BKPMISSING000000000001", "--json")
	if code == 0 || out != "" || !strings.Contains(stderr, "not found") {
		t.Fatalf("volumes delete --json (missing row): code=%d out=%q stderr=%q (want not found)", code, out, stderr)
	}
	compareGolden(t, "volumes-delete-json", normalizeGolden(stderr))
}

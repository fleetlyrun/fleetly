package cmd

// 自描述动词 golden（schema / explain，F1.4）：真 RPC 链（harness 挂
// SystemService），文档全静态（无 ID/时间）——golden 逐字节稳定，同时是
// ADR-0026 两个验收锚的 CLI 面：schema 暴露全量 payload 契约、双形态钉死。

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGoldenSelfDescriptionVerbs(t *testing.T) {
	h := newGoldenHarness(t)

	steps := []struct {
		verb string
		args []string
	}{
		{"schema", []string{"schema"}},
		// 覆盖守卫按动词名找 explain 双形态；spec 与 event 两形态各钉一对。
		{"explain", []string{"explain", "app"}},
		{"explain-event", []string{"explain", "deployment.succeeded"}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			code, out, stderr := runCLI(t, st.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			code, out, stderr = runCLI(t, append(st.args, "--json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
	_ = h // 夹具仅提供 bufconn 拨号与 PUBLIC 面注册
}

// TestExplainErrorPaths：机器契约——缺参是用法错误（64），未知名是服务端
// E_NOT_FOUND 信封（1），报错信息给出清单入口。
func TestExplainErrorPaths(t *testing.T) {
	newGoldenHarness(t)

	code, _, stderr := runCLI(t, "explain")
	assert.Equal(t, 64, code)
	assert.Contains(t, stderr, "exactly one resource name")

	code, _, _ = runCLI(t, "explain", "no.such", "extra")
	assert.Equal(t, 64, code)

	code, _, stderr = runCLI(t, "explain", "definitely.not-a-name")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "E_NOT_FOUND")
	assert.Contains(t, stderr, "fleetly schema")
}

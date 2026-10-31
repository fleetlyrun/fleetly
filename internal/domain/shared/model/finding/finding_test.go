package finding

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 链 B：golden 快照钉死条目集（再生成：go test ./internal/domain/shared/model/finding -update）。
func TestGoldenSnapshot(t *testing.T) {
	golden := filepath.Join("testdata", "findings.golden")
	got := Default().Snapshot()
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/domain/shared/model/finding -update")
	assert.Equal(t, string(want), got, "finding set drifted from golden (registry is append-only)")
}

func TestRegistryInvariants(t *testing.T) {
	assert.NotEmpty(t, Default().IDs(), "registry must not be empty")
	for _, f := range All() {
		assert.NotEmpty(t, f.Summary, "%s missing Summary", f.ID)
		assert.True(t, severities[f.Severity], "%s severity %q not in the frozen two-level set", f.ID, f.Severity)
		assert.True(t, faultClasses[f.FaultClass], "%s fault class %q not in the frozen four-value set", f.ID, f.FaultClass)
		// severity 两级定形（验收锚：golden 形状冻结——advice 档 v1 仅稳态 stopped）。
		if f.Severity == SeverityAdvice {
			assert.Equal(t, "F_WORKLOAD_STOPPED_EXPLAINED", f.ID,
				"%s is advice-grade; v1 admits exactly one advice entry (design §2.5)", f.ID)
		}
	}
	// Snapshot 确定性：fix_params 排序序列化，双跑逐字节一致。
	assert.Equal(t, Default().Snapshot(), Default().Snapshot())
}

// 链 A：构造期 fail-fast（ID 格式/重复/Source 缺失/Severity·FaultClass 非法）。
func TestMustRegisterRejects(t *testing.T) {
	newReg := func() *Registry { return NewRegistry() }
	good := Finding{ID: "F_X", Summary: "s", Severity: SeverityWarning, FaultClass: FaultClassApp, Source: "x"}
	assert.Panics(t, func() {
		newReg().MustRegister(Finding{ID: "lowercase", Source: "x", Severity: SeverityWarning, FaultClass: FaultClassApp})
	})
	assert.Panics(t, func() {
		newReg().MustRegister(Finding{ID: "E_WRONG_PREFIX", Source: "x", Severity: SeverityWarning, FaultClass: FaultClassApp})
	})
	assert.Panics(t, func() {
		newReg().MustRegister(Finding{ID: "F_X", Severity: SeverityWarning, FaultClass: FaultClassApp})
	}) // 缺 Source
	assert.Panics(t, func() {
		newReg().MustRegister(Finding{ID: "F_X", Source: "x", Severity: "critical", FaultClass: FaultClassApp})
	})
	assert.Panics(t, func() {
		newReg().MustRegister(Finding{ID: "F_X", Source: "x", Severity: SeverityWarning, FaultClass: "infra"})
	})

	r := newReg()
	r.MustRegister(good)
	assert.Panics(t, func() {
		r.MustRegister(good)
	}, "duplicate must panic")
}

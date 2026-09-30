package errcode

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 链 B：golden 快照钉死码集（再生成：go test ./internal/model/errcode -update）。
func TestGoldenSnapshot(t *testing.T) {
	golden := filepath.Join("testdata", "codes.golden")
	got := Default().Snapshot()
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/model/errcode -update")
	assert.Equal(t, string(want), got, "code set drifted from golden (registry is append-only)")
}

func TestRegistryInvariants(t *testing.T) {
	assert.NotEmpty(t, Default().IDs(), "registry must not be empty")
	for _, c := range All() {
		assert.NotEmpty(t, c.Summary, "%s missing Summary", c.ID)
		assert.NotEmpty(t, c.Suggestion, "%s missing Suggestion", c.ID)
		assert.NotEqual(t, codes.OK, c.GRPC, "%s maps to OK", c.ID)
		assert.Equal(t, DocsURLPrefix+c.ID, DocsURL(c.ID))
	}
}

// 链 A：构造期 fail-fast（格式/重复/Source 缺失/OK 映射）。
func TestMustRegisterRejects(t *testing.T) {
	newReg := func() *Registry { return NewRegistry() }
	assert.Panics(t, func() { newReg().MustRegister(Code{ID: "lowercase", Source: "x", GRPC: codes.Internal}) })
	assert.Panics(t, func() { newReg().MustRegister(Code{ID: "E_X", GRPC: codes.Internal}) }) // 缺 Source
	assert.Panics(t, func() { newReg().MustRegister(Code{ID: "E_X", Source: "x", GRPC: codes.OK}) })

	r := newReg()
	r.MustRegister(Code{ID: "E_X", Source: "s", Summary: "m", Suggestion: "sug", GRPC: codes.NotFound})
	assert.Panics(t, func() {
		r.MustRegister(Code{ID: "E_X", Source: "s", GRPC: codes.NotFound})
	}, "duplicate must panic")
}

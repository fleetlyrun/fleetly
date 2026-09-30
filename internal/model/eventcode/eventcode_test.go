package eventcode

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

// 链 B：golden 快照钉死事件集（再生成：go test ./internal/model/eventcode -update）。
func TestGoldenSnapshot(t *testing.T) {
	golden := filepath.Join("testdata", "events.golden")
	got := Default().Snapshot()
	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o750)) //nolint:gosec // 测试产物目录
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/model/eventcode -update")
	assert.Equal(t, string(want), got, "event set drifted from golden (registry is append-only)")
}

// 链 A：构造期 fail-fast（格式/重复/Source 缺失）。
func TestMustRegisterRejects(t *testing.T) {
	assert.Panics(t, func() { NewRegistry().MustRegister(Event{Name: "no-dot", Source: "x"}) })
	assert.Panics(t, func() { NewRegistry().MustRegister(Event{Name: "Bad.Upper", Source: "x"}) })
	assert.Panics(t, func() { NewRegistry().MustRegister(Event{Name: "a.b"}) }) // 缺 Source
	r := NewRegistry()
	r.MustRegister(Event{Name: "a.b", Summary: "s", Source: "s"})
	assert.Panics(t, func() { r.MustRegister(Event{Name: "a.b", Source: "s"}) })
}

// 链 C：usage 反扫（与 errcode 同构）。全部首批事件已落地发射点。
var eventExemptions = map[string]string{}

func TestRegistryEventsReferencedInProduction(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile))))

	hits := map[string]int{}
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".pb.go") ||
			strings.HasSuffix(rel, ".pb.gw.go") || strings.HasSuffix(rel, "wire_gen.go") ||
			strings.HasPrefix(rel, "internal/model/eventcode/") {
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
		if readErr != nil {
			return readErr
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, name := range Names() {
			if strings.Contains(content, `"`+name+`"`) {
				hits[name]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range Names() {
		if reason, exempt := eventExemptions[name]; exempt {
			if reason == "" {
				t.Errorf("exemption %s must carry a reason", name)
			}
			continue
		}
		if hits[name] == 0 {
			t.Errorf("eventcode %s registered but never referenced in production code", name)
		}
	}
	for name := range eventExemptions {
		if _, ok := Get(name); !ok {
			t.Errorf("exemption %s is no longer registered; remove the exemption entry", name)
		}
	}
}

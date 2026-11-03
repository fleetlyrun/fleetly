package platformlog

// mirror 写侧单测（ADR-0091 验收锚：tee 镜像字段形态、轮转份数上界、
// 队列满丢弃不阻塞、Close 排空落盘）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardHandler 是主径替身（镜像行为不依赖主径实现）。
type discardHandler struct{}

func (discardHandler) Enabled(_ context.Context, _ slog.Level) bool  { return true }
func (discardHandler) Handle(_ context.Context, _ slog.Record) error { return nil }
func (discardHandler) WithAttrs(attrs []slog.Attr) slog.Handler      { return discardHandler{} }
func (discardHandler) WithGroup(name string) slog.Handler            { return discardHandler{} }

// mirroredLines 等待镜像文件出现至少 n 行（写侧异步）后返回全部行。
func mirroredLines(t *testing.T, dataRoot string, n int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		frames, err := Read(dataRoot, Query{})
		require.NoError(t, err)
		if len(frames) >= n {
			var out [][]byte
			for _, fr := range frames {
				out = append(out, fr.Line)
			}
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("mirror did not receive %d lines in time (got %d)", n, len(frames))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMirrorTeeWritesJSONLines(t *testing.T) {
	root := t.TempDir()
	m, err := OpenMirror(root, map[string]string{"service.name": "Fleetly", "service.version": "test"})
	require.NoError(t, err)
	logger := slog.New(m.TeeHandler(discardHandler{}))

	logger.Info("gRPC request completed", "method", "/svc/Do", slog.Duration("duration", 877*time.Microsecond))
	logger.Error("gRPC request failed", "method", "/svc/Do", "error", assert.AnError)

	require.NoError(t, m.Close())
	lines := mirroredLines(t, root, 2)
	require.Len(t, lines, 2, "exactly the two mirrored lines must be on disk after Close")

	var first, second map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &first))
	require.NoError(t, json.Unmarshal(lines[1], &second))

	assert.Equal(t, "info", first["level"])
	assert.Equal(t, "gRPC request completed", first["msg"])
	assert.Equal(t, "/svc/Do", first["method"])
	assert.InDelta(t, 0.000877, first["duration"], 1e-9, "duration must be zap-style seconds float")
	assert.Equal(t, "Fleetly", first["service.name"], "fixed identity fields must ride every line")
	assert.Equal(t, "test", first["service.version"])
	assert.NotEmpty(t, first["ts"], "ts projection must be present")

	assert.Equal(t, "error", second["level"])
	assert.Equal(t, assert.AnError.Error(), second["error"], "error attrs must serialize to their message text")
}

func TestMirrorRotatesAtBound(t *testing.T) {
	root := t.TempDir()
	// 小轮转界（测试接缝）：两条长行即触发轮转。
	m, err := openMirror(root, nil, 200)
	require.NoError(t, err)
	logger := slog.New(m.TeeHandler(discardHandler{}))
	big := strings.Repeat("x", 90)
	for i := 0; i < 6; i++ {
		logger.Info("rotation probe", "i", i, "blob", big)
	}
	require.NoError(t, m.Close())

	for i := 1; i <= MaxFiles-1; i++ {
		p := filepath.Join(root, LogDirName, fmt.Sprintf("%s.%d", FileName, i))
		if i < MaxFiles-1 {
			_, err := os.Stat(p)
			assert.NoError(t, err, "rotated archive %d must exist", i)
		}
	}
	// 份数上界：.log.3 之后不再增长（.log.4 不出现）。
	_, err = os.Stat(filepath.Join(root, LogDirName, FileName+".4"))
	assert.True(t, os.IsNotExist(err), "rotation must cap at %d files", MaxFiles)
	// 保留窗语义：每档恰一行 → 6 行写入后最老 2 行被顶出，最近 4 行在档
	//（.log.3=行3 … .log=行6；设计就是「最近 MaxFiles 份」，非全量守恒）。
	frames, err := Read(root, Query{Text: "rotation probe"})
	require.NoError(t, err)
	require.Len(t, frames, 4, "rotation must retain the most recent %d lines", MaxFiles)
	for i, fr := range frames {
		assert.Contains(t, string(fr.Line), `"i":`+string(rune('0'+i+2)),
			"retained lines must be the newest, oldest-first: slot %d", i)
	}
}

func TestMirrorOfferDropsWhenQueueFull(t *testing.T) {
	// 直构小队列 Mirror（不走 openMirror 的写 goroutine）：offer 语义
	// 单元钉死——满即丢弃计数，绝不阻塞。
	m := &Mirror{ch: make(chan []byte, 1), done: make(chan struct{})}
	m.offer([]byte("a"))
	m.offer([]byte("b"))
	m.offer([]byte("c"))
	assert.Equal(t, int64(2), m.Drops(), "offers beyond capacity must be counted as drops")
	assert.Len(t, m.ch, 1, "only the first line must be queued")
}

func TestMirrorCloseDrainsQueue(t *testing.T) {
	root := t.TempDir()
	m, err := openMirror(root, nil, 1<<30)
	require.NoError(t, err)
	logger := slog.New(m.TeeHandler(discardHandler{}))
	for i := 0; i < 50; i++ {
		logger.Info("drain probe", "i", i)
	}
	require.NoError(t, m.Close())
	frames, err := Read(root, Query{Text: "drain probe"})
	require.NoError(t, err)
	assert.Len(t, frames, 50, "Close must drain every queued line to disk")
}

func TestMirrorMissingDirReportsNoMirror(t *testing.T) {
	_, err := Read(t.TempDir(), Query{})
	assert.ErrorIs(t, err, ErrNoMirror)
}

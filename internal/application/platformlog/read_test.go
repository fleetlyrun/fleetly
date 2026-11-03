package platformlog

// read 侧单测（ADR-0091 验收锚：tail/时间窗/子串过滤、跨轮转拼接旧→新、
// Follow 新增投递与轮转续读、半截行容忍、ctx 取消）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedMirror 直写镜像行（绕过写 goroutine——read 面单元钉过滤与拼接语义；
// 行形态与 formatRecord 一致）。
func seedMirror(t *testing.T, root string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, LogDirName), 0o750))
	f, err := os.OpenFile(mirrorPath(root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	for _, l := range lines {
		_, err := f.WriteString(l + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())
}

func line(ts, msg string) string {
	return `{"ts":"` + ts + `","level":"info","msg":"` + msg + `"}`
}

func TestReadFiltersAndTails(t *testing.T) {
	root := t.TempDir()
	seedMirror(t, root,
		line("2026-11-03T10:00:00.000Z", "alpha"),
		line("2026-11-03T11:00:00.000Z", "beta"),
		`{"ts":"2026-11-03T11:30:00.000Z","level":"warn","msg":"half-json-no-msg"`, // 半截行：跳过
		line("2026-11-03T12:00:00.000Z", "gamma"),
	)

	all, err := Read(root, Query{})
	require.NoError(t, err)
	assert.Len(t, all, 3, "malformed partial line must be skipped")

	since, err := Read(root, Query{Since: time.Date(2026, 11, 3, 11, 30, 0, 0, time.UTC)})
	require.NoError(t, err)
	require.Len(t, since, 1)
	assert.Equal(t, "gamma", since[0].Msg)

	until, err := Read(root, Query{Until: time.Date(2026, 11, 3, 11, 30, 0, 0, time.UTC)})
	require.NoError(t, err)
	assert.Len(t, until, 2, "until bound is inclusive of earlier frames only")

	text, err := Read(root, Query{Text: "bet"})
	require.NoError(t, err)
	require.Len(t, text, 1)
	assert.Equal(t, "beta", text[0].Msg)

	tail, err := Read(root, Query{TailLines: 1})
	require.NoError(t, err)
	require.Len(t, tail, 1)
	assert.Equal(t, "gamma", tail[0].Msg, "tail truncation happens after filtering")

	assert.Equal(t, "info", all[0].Level)
	assert.Equal(t, "alpha", all[0].Msg)
	assert.NotEmpty(t, all[0].Line, "raw line bytes must ride the frame")
}

func TestReadSpansArchivesOldestToNewest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, LogDirName)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	// 手工布轮转布局：.log.2（最旧）→ .log.1 → .log（活跃最新）。
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName+".2"), []byte(line("2026-11-03T08:00:00.000Z", "oldest")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName+".1"), []byte(line("2026-11-03T09:00:00.000Z", "middle")+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, FileName), []byte(line("2026-11-03T10:00:00.000Z", "newest")+"\n"), 0o600))

	frames, err := Read(root, Query{})
	require.NoError(t, err)
	require.Len(t, frames, 3)
	assert.Equal(t, "oldest", frames[0].Msg)
	assert.Equal(t, "middle", frames[1].Msg)
	assert.Equal(t, "newest", frames[2].Msg, "archives must concatenate oldest→newest")
}

func TestFollowDeliversNewLinesAndStopsOnContext(t *testing.T) {
	root := t.TempDir()
	seedMirror(t, root, line("2026-11-03T10:00:00.000Z", "existing"))

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, root, Query{}, func(fr Frame) error {
			got <- fr.Msg
			return nil
		})
	}()

	// 存量不投（Follow 只投新增）；新增两行。
	time.Sleep(50 * time.Millisecond) // Follow 起跑
	seedMirror(t, root, line("2026-11-03T10:01:00.000Z", "fresh-a"), line("2026-11-03T10:02:00.000Z", "fresh-b"))

	select {
	case msg := <-got:
		assert.Equal(t, "fresh-a", msg, "follow must deliver only new lines")
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not deliver the new line")
	}
	select {
	case msg := <-got:
		assert.Equal(t, "fresh-b", msg)
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not deliver the second line")
	}

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not stop on context cancel")
	}
	select {
	case msg := <-got:
		t.Fatalf("no frame after cancel, got %q", msg)
	default:
	}
}

func TestFollowSurvivesRotation(t *testing.T) {
	root := t.TempDir()
	active := mirrorPath(root)
	seedMirror(t, root, line("2026-11-03T10:00:00.000Z", "pre-rotate"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- Follow(ctx, root, Query{}, func(fr Frame) error {
			got <- fr.Msg
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	// 模拟一次轮转：活跃文件移位成 .log.1，新活跃文件重开。
	require.NoError(t, os.Rename(active, active+".1"))
	seedMirror(t, root, line("2026-11-03T10:05:00.000Z", "post-rotate"))

	select {
	case msg := <-got:
		assert.Equal(t, "post-rotate", msg, "follow must resume on the rotated-in active file")
	case <-time.After(2 * time.Second):
		t.Fatal("follow lost the stream across rotation")
	}
}

func TestFollowMissingFileReportsNoMirror(t *testing.T) {
	err := Follow(context.Background(), t.TempDir(), Query{}, func(Frame) error { return nil })
	assert.ErrorIs(t, err, ErrNoMirror)
}

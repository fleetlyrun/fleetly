package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

func frameLine(b *logBuffer, buildID, line string) {
	b.write(buildID, capability.LogFrame{Line: []byte(line)})
}

// 回绕续流（N0.1 P2-1 的核心回归）：环形缓冲截断丢帧后，follow 游标按
// 帧序列号续流——旧实现游标 = len(frames)，缓冲填满后 len 不再增长，
// 新帧全部静默丢发。
func TestLogBufferFollowSurvivesRingWraparound(t *testing.T) {
	b := newLogBuffer(3)
	frameLine(b, "b1", "l0")
	frameLine(b, "b1", "l1")
	frameLine(b, "b1", "l2")

	frames, last := b.recentAfter("b1", -1)
	require.Len(t, frames, 3)
	require.EqualValues(t, 2, last)

	// 回绕：写入 5 帧（容量 3 → 只剩 l4..l6）。旧 len 游标形态下此后
	// 永远停发；seq 游标必须继续收到新帧。
	frameLine(b, "b1", "l3")
	frameLine(b, "b1", "l4")
	frameLine(b, "b1", "l5")
	frameLine(b, "b1", "l6")

	frames, last = b.recentAfter("b1", last)
	require.Equal(t, []string{"l4", "l5", "l6"}, lines(frames), "frames after the cursor must arrive despite wraparound")
	require.EqualValues(t, 6, last)

	// 无新帧：游标原样回传（终态收口判据）。
	frames, last2 := b.recentAfter("b1", last)
	assert.Empty(t, frames)
	assert.EqualValues(t, last, last2)

	// 快照面（旧→新，全量）。
	assert.Equal(t, []string{"l4", "l5", "l6"}, lines(b.recent("b1")))
}

// 终态回收（N0.1 P2-1）：frames map 只增不清会泄漏——终态 Build 保留
// 最近 retainedTerminalBuilds 个，超龄回收；活跃 Build 不回收。
func TestLogBufferPrunesTerminalBuilds(t *testing.T) {
	b := newLogBuffer(4)
	for i := 0; i < retainedTerminalBuilds+2; i++ {
		id := string(rune('A' + i))
		frameLine(b, id, "line")
		b.markTerminal(id)
	}
	// 最早的 A/B 已回收，其余在场。
	assert.Empty(t, b.recent("A"), "builds beyond the retention window must be pruned")
	assert.Empty(t, b.recent("B"))
	for i := 2; i < retainedTerminalBuilds+2; i++ {
		assert.Len(t, b.recent(string(rune('A'+i))), 1)
	}

	// 幂等：同 ID 重复登记不挤占窗口。
	b.markTerminal(string(rune('A' + retainedTerminalBuilds + 1)))
	assert.Len(t, b.terminal, retainedTerminalBuilds)

	// 活跃（未终态）Build 不受回收影响。
	frameLine(b, "live", "wip")
	assert.Len(t, b.recent("live"), 1)
}

func lines(fs []capability.LogFrame) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f.Line)
	}
	return out
}

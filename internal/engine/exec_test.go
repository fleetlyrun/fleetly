package engine

// exec 会话域首批单测（P0-1，2026-10-14）：未附着会话硬 TTL 回收（K1——
// 受理即起计时，配额锁死绝根）、deliver×finish 并发收口无 send-on-closed
// panic（K2，-race 承载）。夹具：newTestEngine 白盒注入 fakeExecFace +
// after 缝（FakeClock 驱不动 time.AfterFunc——捕获回调手动触发）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state/node"
)

// fakeExecFace 实现 RuntimeExec 的引擎消费面（实例解析恒定单实例；其余
// 方法在本文件不触达——ExecWorkload/RunNodeRelay 由 provider 侧消费）。
type fakeExecFace struct{}

func (fakeExecFace) ExecInstances(context.Context, string) ([]capability.ExecTargetInstance, error) {
	return []capability.ExecTargetInstance{{Instance: "i1", CarrierNodeID: "carrier-1"}}, nil
}
func (fakeExecFace) ExecWorkload(context.Context, capability.ExecWorkloadRequest) (int, error) {
	return 0, nil
}
func (fakeExecFace) RunNodeRelay(context.Context, capability.NodeRelayOptions) error { return nil }
func (fakeExecFace) ExecClusterToken(context.Context, string) error                  { return nil }

// relaySink 是最小 RelayWriter（收帧不解析——本文件只要求中继在连）。
type relaySink struct {
	mu     sync.Mutex
	frames [][]byte
}

func (r *relaySink) WriteRelayFrame(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, append([]byte(nil), b...))
	return nil
}

// newExecSessionEngine 建 exec 域可用的引擎：白盒注入 exec 面 + 节点锚定
// 行 + 在连中继；after 缝捕获 TTL 回调供手动触发（返回 nil 计时器——各
// 消费点均 nil 守卫）。
func newExecSessionEngine(t *testing.T) (*Engine, *[]func()) {
	t.Helper()
	e, _, _ := newTestEngine(t)
	e.execFace = fakeExecFace{}
	require.NoError(t, node.New(e.clock).Upsert(context.Background(), e.db.Runner(), &node.Node{
		PlatformID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "manager", Available: true,
	}))
	var fired []func()
	e.exec.after = func(d time.Duration, f func()) *time.Timer {
		fired = append(fired, f)
		return nil
	}
	_, detach, err := e.RelayAttach(context.Background(), "any-token", "carrier-1", "t", &relaySink{})
	require.NoError(t, err)
	t.Cleanup(detach)
	return e, &fired
}

// TestExecUnattachedSessionsCollectedByHardTTL（K1 验收锚）：未附着会话
// 占满 team 限额后由硬 TTL 全量回收、配额复通——修复前 8 个弃用会话即
// 锁死该 team 全部 exec 直至进程重启。
func TestExecUnattachedSessionsCollectedByHardTTL(t *testing.T) {
	e, fired := newExecSessionEngine(t)
	ctx := context.Background()
	in := CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}}

	for i := 0; i < execMaxSessionsPerTeam; i++ {
		info, err := e.CreateSession(ctx, in)
		require.NoError(t, err)
		require.NotEmpty(t, info.ID)
	}
	_, err := e.CreateSession(ctx, in)
	require.ErrorIs(t, err, ErrExecTeamLimit)
	require.Equal(t, execMaxSessionsPerTeam, e.ExecActiveTeamSessions("default"))

	// 硬 TTL 到点：每个未附着会话各有一条回调（受理即装），触发即回收。
	require.Len(t, *fired, execMaxSessionsPerTeam)
	for _, f := range *fired {
		f()
	}
	assert.Equal(t, 0, e.ExecActiveTeamSessions("default"))

	// 配额复通。
	_, err = e.CreateSession(ctx, in)
	require.NoError(t, err)
}

// TestExecDeliverVsFinishNoSendOnClosed（K2 验收锚）：中继迟到帧投递与
// 会话收口并发——closed 复查+发送同临界区后，任何交错都不得 send on
// closed channel panic（-race 承载；client_stalled 满缓冲路径顺带触达）。
func TestExecDeliverVsFinishNoSendOnClosed(t *testing.T) {
	e, _ := newExecSessionEngine(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		info, err := e.CreateSession(ctx, CreateExecInput{
			TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"},
		})
		require.NoError(t, err)
		e.exec.mu.Lock()
		var s *execSession
		for _, cand := range e.exec.sessions {
			if cand.info.ID == info.ID {
				s = cand
			}
		}
		e.exec.mu.Unlock()
		require.NotNil(t, s)

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic in deliver loop: %v", r)
				}
			}()
			for j := 0; j < 300; j++ {
				e.deliverRelayFrame(capability.RelayFrame{
					SessionID: info.ID, Kind: capability.RelayFrameStdout, Payload: []byte("x"),
				})
			}
		}()
		e.finishSession(s, nil)
		<-done
		_, ok := e.ExecSessionByID(info.ID)
		assert.False(t, ok, "session must be deregistered after finish")
	}
}

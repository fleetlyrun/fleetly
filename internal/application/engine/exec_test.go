package engine

// exec 会话域单测：P0-1 首批（2026-10-14）——未附着会话硬 TTL 回收（K1
// ——受理即起计时，配额锁死绝根）、deliver×finish 并发收口无 send-on-
// closed panic（K2，-race 承载）；P3-1 收编批（2026-10-15）——会话 ID
// 随机化、attach 冲突、收流非终止、中继顶替、慢消费端、exit 收口幂等。
// 夹具：newTestEngine 白盒注入 fakeExecFace + after 缝（FakeClock 驱不动
// time.AfterFunc——捕获回调手动触发）。

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
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

// testRelayCredential 是验收夹具的节点个体凭证（node-1/carrier-1 绑定；
// H10：中继附着只认 node_relay_credentials 的 hash 或 engine 内部 token）。
const testRelayCredential = "fltrelay_apitest_engine_fixture"

// relaySink 是最小 RelayWriter（收帧不解析——本文件只要求中继在连；需观
// 测帧类别时用 kinds 反解）。
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

// kinds 反解已收帧的类别集（线上形态 ParseRelayFrame）。
func (r *relaySink) kinds() map[capability.RelayFrameKind]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[capability.RelayFrameKind]bool, len(r.frames))
	for _, b := range r.frames {
		if f, err := capability.ParseRelayFrame(b); err == nil {
			out[f.Kind] = true
		}
	}
	return out
}

// firedSeam 是 after 缝捕获的回调集（互斥——attach 路径在 goroutine 装配
// 计时器，测试断言并发读数与触发；返回 nil 计时器由各 nil 守卫兜住）。
type firedSeam struct {
	mu  sync.Mutex
	fns []func()
}

func (s *firedSeam) install(e *Engine) {
	e.exec.after = func(d time.Duration, f func()) *time.Timer {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.fns = append(s.fns, f)
		return nil
	}
}

// all 返回已捕获回调快照。
func (s *firedSeam) all() []func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]func(), len(s.fns))
	copy(out, s.fns)
	return out
}

// len 返回已捕获回调数。
func (s *firedSeam) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fns)
}

// newExecDomainEngine 建 exec 域可用的引擎底座：白盒注入 exec 面 + 节点
// 锚定行 + 节点个体凭证行 + after 缝（捕获 TTL 回调供手动触发）。中继由
// 测试自行 RelayAttach（需持有 detach 与帧观测面的场景走此底层形态）。
func newExecDomainEngine(t *testing.T) (*Engine, *firedSeam) {
	t.Helper()
	e, _, _ := newTestEngine(t)
	e.execFace = fakeExecFace{}
	require.NoError(t, noderepo.New(e.clock, e.db.Runner()).Upsert(context.Background(), &node.Node{
		PlatformID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "manager", Available: true,
	}))
	seedRelayCredential(t, e, "node-1", "carrier-1", testRelayCredential)
	fired := &firedSeam{}
	fired.install(e)
	return e, fired
}

// seedRelayCredential 落一行节点个体凭证（只经 repo 写 hash——与生产
// 铸造面同款纪律）。
func seedRelayCredential(t *testing.T, e *Engine, platformID, carrierID, secret string) {
	t.Helper()
	require.NoError(t, e.repos.Nodes.PutRelayCredential(context.Background(), &node.RelayCredential{
		PlatformID: platformID, CarrierID: carrierID, SHA256: node.HashRelayCredential(secret),
	}))
}

// attachExecRelay 便捷壳：carrier-1 上附着中继（帧观测走 relaySink 夹具），
// detach 入 cleanup。
func attachExecRelay(t *testing.T, e *Engine, rs *relaySink) {
	t.Helper()
	_, detach, err := e.RelayAttach(context.Background(), testRelayCredential, "carrier-1", "t", rs)
	require.NoError(t, err)
	t.Cleanup(detach)
}

// newExecSessionEngine 建 exec 域可用的引擎：底座之上内建在连中继（不需
// 观测中继帧的测试用）。
func newExecSessionEngine(t *testing.T) (*Engine, *firedSeam) {
	t.Helper()
	e, fired := newExecDomainEngine(t)
	attachExecRelay(t, e, &relaySink{})
	return e, fired
}

// newExecSessionEngineSink 同 newExecSessionEngine，但交出中继连接上的
// relaySink（帧观测面）。
func newExecSessionEngineSink(t *testing.T) (*Engine, *firedSeam, *relaySink) {
	t.Helper()
	e, fired := newExecDomainEngine(t)
	rs := &relaySink{}
	attachExecRelay(t, e, rs)
	return e, fired, rs
}

// fakeExecPipe 是最小 ExecClientPipe 夹具：Send 记帧（互斥保护），Recv
// 阻塞至 closeRecv（close 即收流 io.EOF 语义）。
type fakeExecPipe struct {
	mu     sync.Mutex
	frames []ExecServerFrame
	recv   chan struct{}
}

func newExecPipe() *fakeExecPipe { return &fakeExecPipe{recv: make(chan struct{})} }

// closeRecv 触发消费端收流（Recv 返回 io.EOF——非终止语义，只调一次）。
func (p *fakeExecPipe) closeRecv() { close(p.recv) }

func (p *fakeExecPipe) Recv() (ExecClientFrame, error) {
	<-p.recv // 未 close 恒阻塞；close 即收流
	return ExecClientFrame{}, io.EOF
}

func (p *fakeExecPipe) Send(f ExecServerFrame) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = append(p.frames, f)
	return nil
}

// sent 返回已收下行帧快照。
func (p *fakeExecPipe) sent() []ExecServerFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ExecServerFrame(nil), p.frames...)
}

// execSessionPtr 是白盒取会话活体指针（注册表读持 x.mu——relay 等字段
// 与受理/顶替路径同锁）。
func execSessionPtr(t *testing.T, e *Engine, id string) *execSession {
	t.Helper()
	e.exec.mu.Lock()
	defer e.exec.mu.Unlock()
	s := e.exec.sessions[id]
	require.NotNil(t, s, "session %s must be registered", id)
	return s
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
	require.Len(t, fired.all(), execMaxSessionsPerTeam)
	for _, f := range fired.all() {
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
				e.exec.deliver(capability.RelayFrame{
					SessionID: info.ID, Kind: capability.RelayFrameStdout, Payload: []byte("x"),
				})
			}
		}()
		e.exec.finish(s, nil)
		<-done
		_, ok := e.ExecSessionByID(info.ID)
		assert.False(t, ok, "session must be deregistered after finish")
	}
}

// TestExecSessionIDsRandomAndUnordered（P3-1 验收锚）：会话 ID 形态
// exec-<32hex> 且不可枚举——200 次受理全唯一、相邻受理不构成序关系
// （防回归成序号：单调序号使会话 ID 可枚举，是跨队探测的放大器）。
func TestExecSessionIDsRandomAndUnordered(t *testing.T) {
	e, _ := newExecSessionEngine(t)
	ctx := context.Background()

	seen := make(map[string]bool, 200)
	prev := ""
	for i := 0; i < 200; i++ {
		// 限额按 team 计——逐次换 team 隔离限额，专测 ID 铸造面。
		info, err := e.CreateSession(ctx, CreateExecInput{
			TeamID: fmt.Sprintf("team-%d", i), AppID: tAppID, Process: "web", Argv: []string{"sh"},
		})
		require.NoError(t, err)
		assert.Regexp(t, `^exec-[0-9a-f]{32}$`, info.ID)
		assert.False(t, seen[info.ID], "session id must be unique: %s", info.ID)
		seen[info.ID] = true
		if prev != "" {
			// 防回归成序号：相邻两次不构成序关系——不互为前缀（增位演
			// 进 exec-1 → exec-12），也不是 +1 演进（零垫序号形态
			// exec-…01 → exec-…02）。
			assert.False(t, strings.HasPrefix(info.ID, prev) || strings.HasPrefix(prev, info.ID),
				"consecutive session ids must not be prefix-related: %s -> %s", prev, info.ID)
			pn, ok1 := new(big.Int).SetString(strings.TrimPrefix(prev, "exec-"), 16)
			cn, ok2 := new(big.Int).SetString(strings.TrimPrefix(info.ID, "exec-"), 16)
			require.True(t, ok1 && ok2)
			assert.NotZero(t, cn.Cmp(new(big.Int).Add(pn, big.NewInt(1))),
				"consecutive session ids must not be a +1 sequence: %s -> %s", prev, info.ID)
		}
		prev = info.ID
	}
}

// TestExecAttachConflict（P3-1 验收锚）：单消费端附着律——第二附着者收
// attach_conflict 错误帧且 AttachClient 返回 nil（不惊动首附着）。
func TestExecAttachConflict(t *testing.T) {
	e, _ := newExecSessionEngine(t)
	ctx := context.Background()
	info, err := e.CreateSession(ctx, CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	// 首附着后台驱动（meta 帧落地即 attached 置位可见）。
	first := newExecPipe()
	t.Cleanup(first.closeRecv)
	done := make(chan error, 1)
	go func() { done <- e.AttachClient(ctx, info.ID, first) }()
	assert.Eventually(t, func() bool {
		fs := first.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)

	// 第二附着：恰一条 attach_conflict 错误帧。
	second := newExecPipe()
	require.NoError(t, e.AttachClient(ctx, info.ID, second))
	fs := second.sent()
	require.Len(t, fs, 1)
	require.NotNil(t, fs[0].Err)
	assert.Equal(t, "attach_conflict", fs[0].Err.Code)
	assert.Nil(t, fs[0].Meta)

	// 收尾：收口会话，首附着返回（out 关闭路径）。
	e.exec.finish(execSessionPtr(t, e, info.ID), nil)
	require.NoError(t, <-done)
}

// TestExecRecvEOFNotTermination（P3-1 验收锚）：消费端收流（Recv EOF）
// ≠ 终止——StdinEOF 帧转达中继、会话仍在册；附着装配空闲计时；随后硬
// TTL 回调触发才收口清出。
func TestExecRecvEOFNotTermination(t *testing.T) {
	e, fired, rs := newExecSessionEngineSink(t)
	ctx := context.Background()
	info, err := e.CreateSession(ctx, CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)
	require.Len(t, fired.all(), 1) // 受理即装硬 TTL（K1）

	// 附着即收流（Recv 立即 EOF）。
	pipe := newExecPipe()
	pipe.closeRecv()
	done := make(chan error, 1)
	go func() { done <- e.AttachClient(ctx, info.ID, pipe) }()

	// 空闲计时装配（after 回调数变 2：受理硬 TTL + attach 空闲）。
	assert.Eventually(t, func() bool { return fired.len() == 2 }, 2*time.Second, 10*time.Millisecond)

	// 收流 ≠ 终止：StdinEOF 帧已达中继、会话仍在册。
	assert.Eventually(t, func() bool { return rs.kinds()[capability.RelayFrameStdinEOF] },
		2*time.Second, 10*time.Millisecond)
	_, alive := e.ExecSessionByID(info.ID)
	assert.True(t, alive, "recv EOF must not terminate the session")

	// 硬 TTL 回调（fired[0]）触发 → 收口清出，附着返回。
	fired.all()[0]()
	require.NoError(t, <-done)
	_, alive = e.ExecSessionByID(info.ID)
	assert.False(t, alive)
}

// TestRelayAttachReplacesSameNode（H10 §6 验收锚）：同节点中继顶替——
// 同一凭证同一 carrier 再附着，已附着会话收口（relay_disconnected 如实
// 告知），未附着会话重指新连接（白盒指针比对）；旧连接 detach 走身份
// 校验，继任者与未附着会话不受牵连。
func TestRelayAttachReplacesSameNode(t *testing.T) {
	e, _ := newExecDomainEngine(t)
	ctx := context.Background()
	_, detachOld, err := e.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err) // detachOld 不入 cleanup——测试内显式 detach 验身份校验

	in := CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}}
	attachedInfo, err := e.CreateSession(ctx, in)
	require.NoError(t, err)
	unattachedInfo, err := e.CreateSession(ctx, in)
	require.NoError(t, err)

	// 附着其一（后台驱动）。
	pipe := newExecPipe()
	t.Cleanup(pipe.closeRecv)
	done := make(chan error, 1)
	go func() { done <- e.AttachClient(ctx, attachedInfo.ID, pipe) }()
	assert.Eventually(t, func() bool {
		fs := pipe.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)
	sAttached := execSessionPtr(t, e, attachedInfo.ID)
	e.exec.mu.Lock()
	old := sAttached.relay
	e.exec.mu.Unlock()

	// 同 carrier 新中继附着（顶替）。
	_, detachNew, err := e.RelayAttach(ctx, testRelayCredential, "carrier-1", "v2", &relaySink{})
	require.NoError(t, err)
	t.Cleanup(detachNew)

	// 附着会话收口（relay_disconnected）。
	assert.Eventually(t, func() bool {
		for _, f := range pipe.sent() {
			if f.Err != nil && f.Err.Code == "relay_disconnected" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, <-done)
	_, alive := e.ExecSessionByID(attachedInfo.ID)
	assert.False(t, alive)

	// 未附着会话重指新连接（白盒 s.relay 指针比对）。
	e.exec.mu.Lock()
	current := e.exec.relays["node-1"]
	repointed := e.exec.sessions[unattachedInfo.ID].relay
	e.exec.mu.Unlock()
	require.NotNil(t, current)
	assert.NotSame(t, old, current)
	assert.Same(t, current, repointed)

	// 旧连接 detach：身份校验——已被顶替的连接不误删继任者，未附着会话
	// 不受牵连。
	detachOld()
	e.exec.mu.Lock()
	after := e.exec.relays["node-1"]
	e.exec.mu.Unlock()
	assert.Same(t, current, after)
	_, alive = e.ExecSessionByID(unattachedInfo.ID)
	assert.True(t, alive, "unattached session must survive old-conn detach")
}

// TestExecSlowConsumerStallClosesSession（P3-1 验收锚）：慢消费端——下
// 行缓冲填满（无消费端不排水）后再投一帧即 client_stalled 收口（内存膨
// 胀防护）。
func TestExecSlowConsumerStallClosesSession(t *testing.T) {
	e, _ := newExecSessionEngine(t)
	ctx := context.Background()
	info, err := e.CreateSession(ctx, CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	frame := capability.RelayFrame{SessionID: info.ID, Kind: capability.RelayFrameStdout, Payload: []byte("x")}
	for i := 0; i < execOutboundCap; i++ {
		e.exec.deliver(frame)
	}
	_, alive := e.ExecSessionByID(info.ID)
	assert.True(t, alive, "a full buffer alone must not close the session")

	e.exec.deliver(frame) // 满后再投一帧 → client_stalled 收口
	_, alive = e.ExecSessionByID(info.ID)
	assert.False(t, alive, "a frame past the outbound cap must close a stalled session")
}

// TestExecExitFrameClosesAndFinishIdempotent（P3-1 验收锚）：exit 帧收口
// ——deliver 投递退出码帧，下行泵见 Exit 即收口：终结帧透传消费端、out
// 关闭、注册表清出；finish 幂等（重复收口无 panic）。
func TestExecExitFrameClosesAndFinishIdempotent(t *testing.T) {
	e, _ := newExecSessionEngine(t)
	ctx := context.Background()
	info, err := e.CreateSession(ctx, CreateExecInput{TeamID: "default", AppID: tAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	// 附着消费端（后台驱动）——收口由下行泵驱动。
	pipe := newExecPipe()
	t.Cleanup(pipe.closeRecv)
	done := make(chan error, 1)
	go func() { done <- e.AttachClient(ctx, info.ID, pipe) }()
	assert.Eventually(t, func() bool {
		fs := pipe.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)
	s := execSessionPtr(t, e, info.ID)

	// exit 帧投递（relay→manager 形态，退出码 3 透传）。
	e.exec.deliver(capability.RelayFrame{
		SessionID: info.ID, Kind: capability.RelayFrameExit,
		Payload: capability.EncodeRelayJSON(capability.RelaySessionExit{Code: 3}),
	})

	// 终结帧已投到消费端；out 关闭、注册表清出、附着返回。
	assert.Eventually(t, func() bool {
		fs := pipe.sent()
		last := fs[len(fs)-1]
		return last.Exit != nil && *last.Exit == 3
	}, 2*time.Second, 10*time.Millisecond)
	require.NoError(t, <-done)
	_, open := <-s.out
	assert.False(t, open, "out must be closed after the exit frame")
	_, alive := e.ExecSessionByID(info.ID)
	assert.False(t, alive)

	// 幂等：重复 finish 无 panic（closed 已置位短路）。
	e.exec.finish(s, nil)
	e.exec.finish(s, nil)
}

// TestRelayAttachRejectsJoinToken（H10 §6 验收锚）：join token 不再是
// relay 凭证——RelayAttach 与 ValidateRelayCredential 都拒绝；内部 token
// 仍被信任（控制面进程内中继），空凭证拒绝。
func TestRelayAttachRejectsJoinToken(t *testing.T) {
	e, _ := newExecDomainEngine(t)
	ctx := context.Background()

	// swarm join token 形态（真实形态 SWMTKN-* 与夹具的 TESTTOKEN 同判）。
	for _, joinToken := range []string{"TESTTOKEN", "SWMTKN-1-abc"} {
		_, _, err := e.RelayAttach(ctx, joinToken, "carrier-1", "v1", &relaySink{})
		require.ErrorIs(t, err, ErrExecRelayUnauthorized,
			"join token %q must not authenticate a relay", joinToken)
		require.ErrorIs(t, e.ValidateRelayCredential(ctx, joinToken), ErrExecRelayUnauthorized)
	}

	// 空凭证拒绝。
	require.ErrorIs(t, e.ValidateRelayCredential(ctx, ""), ErrExecRelayUnauthorized)

	// 合法节点个体凭证与内部 token 通过；未锚定 carrier 的内部 token
	// 附着仍按锚定校验（内部面只免凭证面，不免锚定面）。
	require.NoError(t, e.ValidateRelayCredential(ctx, testRelayCredential))
	internal, err := e.RelayInternalCredential(ctx, "carrier-1")
	require.NoError(t, err)
	require.NoError(t, e.ValidateRelayCredential(ctx, internal))
	_, detach, err := e.RelayAttach(ctx, internal, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err)
	detach()

	// 失败路径不留连接（join token 被拒后 relays 为空）。
	e.exec.mu.Lock()
	_, online := e.exec.relays["node-1"]
	e.exec.mu.Unlock()
	assert.False(t, online, "a rejected credential must not register a relay")
}

// TestRelayAttachRejectsOtherCarrier（H10 §6 验收锚）：节点 A 的凭证 +
// hello 声明已锚定的节点 B carrier → ErrExecRelayUnauthorized（凭证绑定
// 轴与 hello 解析出的节点双向不匹配）；B 无连接，已有 A 连接不受影响。
func TestRelayAttachRejectsOtherCarrier(t *testing.T) {
	e, _ := newExecDomainEngine(t)
	ctx := context.Background()
	require.NoError(t, e.repos.Nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-2", CarrierID: "carrier-2", Hostname: "h2", Role: "worker", Available: true,
	}))

	// A 正常附着。
	aSink := &relaySink{}
	_, detachA, err := e.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", aSink)
	require.NoError(t, err)
	t.Cleanup(detachA)

	// A 的凭证冒充 B：拒。
	_, _, err = e.RelayAttach(ctx, testRelayCredential, "carrier-2", "v1", &relaySink{})
	require.ErrorIs(t, err, ErrExecRelayUnauthorized,
		"a credential bound to one carrier must not attach as another carrier")

	e.exec.mu.Lock()
	a := e.exec.relays["node-1"]
	_, bOnline := e.exec.relays["node-2"]
	e.exec.mu.Unlock()
	assert.NotNil(t, a, "the existing A connection must stay registered")
	assert.False(t, bOnline, "the rejected connection must not be registered under B")
}

// TestRelayAttachRejectsUnanchoredCarrier（H10 §6 验收锚）：凭证行存在但
// ByCarrier 无行（节点尚未锚定）→ ErrExecNodeUnanchored。
func TestRelayAttachRejectsUnanchoredCarrier(t *testing.T) {
	e, _ := newExecDomainEngine(t)
	ctx := context.Background()
	seedRelayCredential(t, e, "node-orphan", "carrier-orphan", "fltrelay_orphan_credential")

	_, _, err := e.RelayAttach(ctx, "fltrelay_orphan_credential", "carrier-orphan", "v1", &relaySink{})
	require.ErrorIs(t, err, ErrExecNodeUnanchored,
		"a credential row without an anchored carrier must fail on the anchoring axis")

	e.exec.mu.Lock()
	_, online := e.exec.relays["node-orphan"]
	e.exec.mu.Unlock()
	assert.False(t, online)
}

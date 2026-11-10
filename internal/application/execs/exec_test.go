package execs

// exec 会话域单测（P5 wave-2 随域拆包自 engine 包迁入，断言面零漂移）：
// P0-1 首批（2026-10-14）——未附着会话硬 TTL 回收（K1——受理即起计时，
// 配额锁死绝根）、deliver×finish 并发收口无 send-on-closed panic（K2，
// -race 承载）；P3-1 收编批（2026-10-15）——会话 ID 随机化、attach 冲突、
// 收流非终止、中继顶替、慢消费端、exit 收口幂等。夹具：hermetic 应用模块
// （真 SQLite 节点/审计 repo + fakeExecFace + after 缝——FakeClock 驱不动
// time.AfterFunc，捕获回调手动触发）；emitLedger 端口绑真 writes.Emit
// （LedgerOnly 审计行断言吃真实行）。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	noderepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/node"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// testAppID 是受理夹具的 App 行 ID（会话域零持久化——AppID 只入会话信息；
// 实例解析走 fakeExecFace 恒定单实例）。
const testAppID = "01JD0APP000000000000000000"

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
//
//nolint:gosec // 测试夹具常量，非真实凭证
const testRelayCredential = "fltrelay_apitest_engine_fixture"

// relaySink 是最小 contract.RelayWriter（收帧不解析——本文件只要求中继在
// 连；需观测帧类别时用 kinds 反解）。
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
// 计时器，测试断言并发读数与触发；返回 nil 计时器由各 nil 契约测试兜住）。
type firedSeam struct {
	mu  sync.Mutex
	fns []func()
}

func (s *firedSeam) install(x *Execs) {
	x.after = func(d time.Duration, f func()) *time.Timer {
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

// newTestSatellite 建 hermetic 应用模块（真 SQLite 节点/审计 repo + 节点锚定行
// + 节点个体凭证行 + after 缝；emitLedger 绑真 writes.Emit——审计断言吃
// 真实行）。中继由测试自行 RelayAttach（需持有 detach 与帧观测面的场景
// 走此底层形态）。
func newTestSatellite(t *testing.T) (*Execs, *firedSeam) {
	t.Helper()
	db, clock := statetest.New(t)
	runner := db.Runner()
	nodes := noderepo.New(clock, runner)
	audits := auditrepo.New(clock, runner)
	stores := transact.Stores{Nodes: nodes, Audits: audits}
	require.NoError(t, nodes.Upsert(context.Background(), &node.Node{
		PlatformID: "node-1", CarrierID: "carrier-1", Hostname: "h", Role: "manager", Available: true,
	}))
	require.NoError(t, nodes.PutRelayCredential(context.Background(), &node.RelayCredential{
		PlatformID: "node-1", CarrierID: "carrier-1", SHA256: node.HashRelayCredential(testRelayCredential),
	}))
	x := New(Deps{
		Log:      slog.New(slog.DiscardHandler),
		ExecFace: fakeExecFace{},
		Repos:    stores,
		Clock:    clock,
		EmitLedger: func(ctx context.Context, action, resource, detail string) error {
			// 与 engine 核 emitLedger 同款落账（LedgerOnly 审计行）——
			// 顶替审计断言吃真实行。
			return writes.Emit(ctx, writes.Repos{Audits: audits}, writes.FactRow{
				Action: action, Resource: resource, Detail: detail, LedgerOnly: true,
			})
		},
	})
	fired := &firedSeam{}
	fired.install(x)
	return x, fired
}

// attachExecRelay 便捷壳：carrier-1 上附着中继（帧观测走 relaySink 夹具），
// detach 入 cleanup。
func attachExecRelay(t *testing.T, x *Execs, rs *relaySink) {
	t.Helper()
	_, detach, err := x.RelayAttach(context.Background(), testRelayCredential, "carrier-1", "t", rs)
	require.NoError(t, err)
	t.Cleanup(detach)
}

// newSessionSatellite 建内建在连中继的应用模块（不需观测中继帧的测试用）。
func newSessionSatellite(t *testing.T) (*Execs, *firedSeam) {
	t.Helper()
	x, fired := newTestSatellite(t)
	attachExecRelay(t, x, &relaySink{})
	return x, fired
}

// newSessionSatelliteSink 同 newSessionSatellite，但交出中继连接上的
// relaySink（帧观测面）。
func newSessionSatelliteSink(t *testing.T) (*Execs, *firedSeam, *relaySink) {
	t.Helper()
	x, fired := newTestSatellite(t)
	rs := &relaySink{}
	attachExecRelay(t, x, rs)
	return x, fired, rs
}

// fakeExecPipe 是最小 contract.ExecClientPipe 夹具：Send 记帧（互斥保护），
// Recv 阻塞至 closeRecv（close 即收流 io.EOF 语义）。
type fakeExecPipe struct {
	mu     sync.Mutex
	frames []contract.ExecServerFrame
	recv   chan struct{}
}

func newExecPipe() *fakeExecPipe { return &fakeExecPipe{recv: make(chan struct{})} }

// closeRecv 触发消费端收流（Recv 返回 io.EOF——非终止语义，只调一次）。
func (p *fakeExecPipe) closeRecv() { close(p.recv) }

func (p *fakeExecPipe) Recv() (contract.ExecClientFrame, error) {
	<-p.recv // 未 close 恒阻塞；close 即收流
	return contract.ExecClientFrame{}, io.EOF
}

func (p *fakeExecPipe) Send(f contract.ExecServerFrame) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = append(p.frames, f)
	return nil
}

// sent 返回已收下行帧快照。
func (p *fakeExecPipe) sent() []contract.ExecServerFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]contract.ExecServerFrame(nil), p.frames...)
}

// execSessionPtr 是白盒取会话活体指针（注册表读持 x.mu——relay 等字段
// 与受理/顶替路径同锁）。
func execSessionPtr(t *testing.T, x *Execs, id string) *execSession {
	t.Helper()
	x.mu.Lock()
	defer x.mu.Unlock()
	s := x.sessions[id]
	require.NotNil(t, s, "session %s must be registered", id)
	return s
}

// TestExecUnattachedSessionsCollectedByHardTTL（K1 验收锚）：未附着会话
// 占满 team 限额后由硬 TTL 全量回收、配额复通——修复前 8 个弃用会话即
// 锁死该 team 全部 exec 直至进程重启。
func TestExecUnattachedSessionsCollectedByHardTTL(t *testing.T) {
	x, fired := newSessionSatellite(t)
	ctx := context.Background()
	in := contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}}

	for i := 0; i < execMaxSessionsPerTeam; i++ {
		info, err := x.CreateSession(ctx, in)
		require.NoError(t, err)
		require.NotEmpty(t, info.ID)
	}
	_, err := x.CreateSession(ctx, in)
	require.ErrorIs(t, err, contract.ErrExecTeamLimit)
	require.Equal(t, execMaxSessionsPerTeam, x.ExecActiveTeamSessions("default"))

	// 硬 TTL 到点：每个未附着会话各有一条回调（受理即装），触发即回收。
	require.Len(t, fired.all(), execMaxSessionsPerTeam)
	for _, f := range fired.all() {
		f()
	}
	assert.Equal(t, 0, x.ExecActiveTeamSessions("default"))

	// 配额复通。
	_, err = x.CreateSession(ctx, in)
	require.NoError(t, err)
}

// TestExecDeliverVsFinishNoSendOnClosed（K2 验收锚）：中继迟到帧投递与
// 会话收口并发——closed 复查+发送同临界区后，任何交错都不得 send on
// closed channel panic（-race 承载；client_stalled 满缓冲路径顺带触达）。
func TestExecDeliverVsFinishNoSendOnClosed(t *testing.T) {
	x, _ := newSessionSatellite(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		info, err := x.CreateSession(ctx, contract.CreateExecInput{
			TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"},
		})
		require.NoError(t, err)
		x.mu.Lock()
		var s *execSession
		for _, cand := range x.sessions {
			if cand.info.ID == info.ID {
				s = cand
			}
		}
		x.mu.Unlock()
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
				x.deliver(capability.RelayFrame{
					SessionID: info.ID, Kind: capability.RelayFrameStdout, Payload: []byte("x"),
				})
			}
		}()
		x.finish(s, nil)
		<-done
		_, ok := x.ExecSessionByID(info.ID)
		assert.False(t, ok, "session must be deregistered after finish")
	}
}

// TestExecSessionIDsRandomAndUnordered（P3-1 验收锚）：会话 ID 形态
// exec-<32hex> 且不可枚举——200 次受理全唯一、相邻受理不构成序关系
// （防回归成序号：单调序号使会话 ID 可枚举，是跨队探测的放大器）。
func TestExecSessionIDsRandomAndUnordered(t *testing.T) {
	x, _ := newSessionSatellite(t)
	ctx := context.Background()

	seen := make(map[string]bool, 200)
	prev := ""
	for i := 0; i < 200; i++ {
		// 限额按 team 计——逐次换 team 隔离限额，专测 ID 铸造面。
		info, err := x.CreateSession(ctx, contract.CreateExecInput{
			TeamID: fmt.Sprintf("team-%d", i), AppID: testAppID, Process: "web", Argv: []string{"sh"},
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
	x, _ := newSessionSatellite(t)
	ctx := context.Background()
	info, err := x.CreateSession(ctx, contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	// 首附着后台驱动（meta 帧落地即 attached 置位可见）。
	first := newExecPipe()
	t.Cleanup(first.closeRecv)
	done := make(chan error, 1)
	go func() { done <- x.AttachClient(ctx, info.ID, first) }()
	assert.Eventually(t, func() bool {
		fs := first.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)

	// 第二附着：恰一条 attach_conflict 错误帧。
	second := newExecPipe()
	require.NoError(t, x.AttachClient(ctx, info.ID, second))
	fs := second.sent()
	require.Len(t, fs, 1)
	require.NotNil(t, fs[0].Err)
	assert.Equal(t, "attach_conflict", fs[0].Err.Code)
	assert.Nil(t, fs[0].Meta)

	// 收尾：收口会话，首附着返回（out 关闭路径）。
	x.finish(execSessionPtr(t, x, info.ID), nil)
	require.NoError(t, <-done)
}

// TestExecRecvEOFNotTermination（P3-1 验收锚）：消费端收流（Recv EOF）
// ≠ 终止——StdinEOF 帧转达中继、会话仍在册；附着装配空闲计时；随后硬
// TTL 回调触发才收口清出。
func TestExecRecvEOFNotTermination(t *testing.T) {
	x, fired, rs := newSessionSatelliteSink(t)
	ctx := context.Background()
	info, err := x.CreateSession(ctx, contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)
	require.Len(t, fired.all(), 1) // 受理即装硬 TTL（K1）

	// 附着即收流（Recv 立即 EOF）。
	pipe := newExecPipe()
	pipe.closeRecv()
	done := make(chan error, 1)
	go func() { done <- x.AttachClient(ctx, info.ID, pipe) }()

	// 空闲计时装配（after 回调数变 2：受理硬 TTL + attach 空闲）。
	assert.Eventually(t, func() bool { return fired.len() == 2 }, 2*time.Second, 10*time.Millisecond)

	// 收流 ≠ 终止：StdinEOF 帧已达中继、会话仍在册。
	assert.Eventually(t, func() bool { return rs.kinds()[capability.RelayFrameStdinEOF] },
		2*time.Second, 10*time.Millisecond)
	_, alive := x.ExecSessionByID(info.ID)
	assert.True(t, alive, "recv EOF must not terminate the session")

	// 硬 TTL 回调（fired[0]）触发 → 收口清出，附着返回。
	fired.all()[0]()
	require.NoError(t, <-done)
	_, alive = x.ExecSessionByID(info.ID)
	assert.False(t, alive)
}

// TestRelayAttachReplacesSameNode（H10 §6 验收锚）：同节点中继顶替——
// 同一凭证同一 carrier 再附着，已附着会话收口（relay_disconnected 如实
// 告知），未附着会话重指新连接（白盒指针比对）；旧连接 detach 走身份
// 校验，继任者与未附着会话不受牵连。
func TestRelayAttachReplacesSameNode(t *testing.T) {
	x, _ := newTestSatellite(t)
	ctx := context.Background()
	_, detachOld, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err) // detachOld 不入 cleanup——测试内显式 detach 验身份校验

	in := contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}}
	attachedInfo, err := x.CreateSession(ctx, in)
	require.NoError(t, err)
	unattachedInfo, err := x.CreateSession(ctx, in)
	require.NoError(t, err)

	// 附着其一（后台驱动）。
	pipe := newExecPipe()
	t.Cleanup(pipe.closeRecv)
	done := make(chan error, 1)
	go func() { done <- x.AttachClient(ctx, attachedInfo.ID, pipe) }()
	assert.Eventually(t, func() bool {
		fs := pipe.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)
	sAttached := execSessionPtr(t, x, attachedInfo.ID)
	x.mu.Lock()
	old := sAttached.relay
	x.mu.Unlock()

	// 同 carrier 新中继附着（顶替）。
	_, detachNew, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v2", &relaySink{})
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
	_, alive := x.ExecSessionByID(attachedInfo.ID)
	assert.False(t, alive)

	// 未附着会话重指新连接（白盒 s.relay 指针比对）。
	x.mu.Lock()
	current := x.relays["node-1"]
	repointed := x.sessions[unattachedInfo.ID].relay
	x.mu.Unlock()
	require.NotNil(t, current)
	assert.NotSame(t, old, current)
	assert.Same(t, current, repointed)

	// 旧连接 detach：身份校验——已被顶替的连接不误删继任者，未附着会话
	// 不受牵连。
	detachOld()
	x.mu.Lock()
	after := x.relays["node-1"]
	x.mu.Unlock()
	assert.Same(t, current, after)
	_, alive = x.ExecSessionByID(unattachedInfo.ID)
	assert.True(t, alive, "unattached session must survive old-conn detach")
}

// TestRelayAttachReplacementAudit（H10 §4.9 验收锚）：old != nil 的二次附着
// 发 node.relay_replaced LedgerOnly 审计（Detail 带新版本）；正常断线重连
// （旧连接已经 dropRelay 摘除，old == nil）不触发。
func TestRelayAttachReplacementAudit(t *testing.T) {
	x, _ := newTestSatellite(t)
	ctx := context.Background()

	count := func() (int, []string) {
		rows, err := x.repos.Audits.List(ctx, 100)
		require.NoError(t, err)
		var details []string
		for _, row := range rows {
			if row.Action == "node.relay_replaced" {
				require.Equal(t, "node/node-1", row.Resource)
				details = append(details, row.Detail)
			}
		}
		return len(details), details
	}

	_, detachOld, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err)
	detachOld()

	// 正常重连（旧连接已摘除）：无顶替事实。
	_, detachNew, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err)
	n, _ := count()
	assert.Zero(t, n, "a clean reconnect after the old connection was dropped must not emit node.relay_replaced")

	// 活体顶替：一行审计，Detail 带新版本。
	_, detachNext, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v2", &relaySink{})
	require.NoError(t, err)
	t.Cleanup(detachNew) // 已被顶替：身份校验为 no-op
	t.Cleanup(detachNext)
	n, details := count()
	require.Equal(t, 1, n, "a live replacement must emit exactly one node.relay_replaced row")
	assert.Contains(t, details[0], `"version":"v2"`)
}

// TestExecSlowConsumerStallClosesSession（P3-1 验收锚）：慢消费端——下
// 行缓冲填满（无消费端不排水）后再投一帧即 client_stalled 收口（内存膨
// 胀防护）。
func TestExecSlowConsumerStallClosesSession(t *testing.T) {
	x, _ := newSessionSatellite(t)
	ctx := context.Background()
	info, err := x.CreateSession(ctx, contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	frame := capability.RelayFrame{SessionID: info.ID, Kind: capability.RelayFrameStdout, Payload: []byte("x")}
	for i := 0; i < execOutboundCap; i++ {
		x.deliver(frame)
	}
	_, alive := x.ExecSessionByID(info.ID)
	assert.True(t, alive, "a full buffer alone must not close the session")

	x.deliver(frame) // 满后再投一帧 → client_stalled 收口
	_, alive = x.ExecSessionByID(info.ID)
	assert.False(t, alive, "a frame past the outbound cap must close a stalled session")
}

// TestExecExitFrameClosesAndFinishIdempotent（P3-1 验收锚）：exit 帧收口
// ——deliver 投递退出码帧，下行泵见 Exit 即收口：终结帧透传消费端、out
// 关闭、注册表清出；finish 幂等（重复收口无 panic）。
func TestExecExitFrameClosesAndFinishIdempotent(t *testing.T) {
	x, _ := newSessionSatellite(t)
	ctx := context.Background()
	info, err := x.CreateSession(ctx, contract.CreateExecInput{TeamID: "default", AppID: testAppID, Process: "web", Argv: []string{"sh"}})
	require.NoError(t, err)

	// 附着消费端（后台驱动）——收口由下行泵驱动。
	pipe := newExecPipe()
	t.Cleanup(pipe.closeRecv)
	done := make(chan error, 1)
	go func() { done <- x.AttachClient(ctx, info.ID, pipe) }()
	assert.Eventually(t, func() bool {
		fs := pipe.sent()
		return len(fs) > 0 && fs[0].Meta != nil
	}, 2*time.Second, 10*time.Millisecond)
	s := execSessionPtr(t, x, info.ID)

	// exit 帧投递（relay→manager 形态，退出码 3 透传）。
	x.deliver(capability.RelayFrame{
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
	_, alive := x.ExecSessionByID(info.ID)
	assert.False(t, alive)

	// 幂等：重复 finish 无 panic（closed 已置位短路）。
	x.finish(s, nil)
	x.finish(s, nil)
}

// TestRelayAttachRejectsJoinToken（H10 §6 验收锚）：join token 不再是
// relay 凭证——RelayAttach 与 ValidateRelayCredential 都拒绝；内部 token
// 仍被信任（控制面进程内中继），空凭证拒绝。
func TestRelayAttachRejectsJoinToken(t *testing.T) {
	x, _ := newTestSatellite(t)
	ctx := context.Background()

	// swarm join token 形态（真实形态 SWMTKN-* 与夹具的 TESTTOKEN 同判）。
	for _, joinToken := range []string{"TESTTOKEN", "SWMTKN-1-abc"} {
		_, _, err := x.RelayAttach(ctx, joinToken, "carrier-1", "v1", &relaySink{})
		require.ErrorIs(t, err, contract.ErrExecRelayUnauthorized,
			"join token %q must not authenticate a relay", joinToken)
		require.ErrorIs(t, x.ValidateRelayCredential(ctx, joinToken), contract.ErrExecRelayUnauthorized)
	}

	// 空凭证拒绝。
	require.ErrorIs(t, x.ValidateRelayCredential(ctx, ""), contract.ErrExecRelayUnauthorized)

	// 合法节点个体凭证与内部 token 通过；未锚定 carrier 的内部 token
	// 附着仍按锚定校验（内部面只免凭证面，不免锚定面）。
	require.NoError(t, x.ValidateRelayCredential(ctx, testRelayCredential))
	internal, err := x.RelayInternalCredential(ctx, "carrier-1")
	require.NoError(t, err)
	require.NoError(t, x.ValidateRelayCredential(ctx, internal))
	_, detach, err := x.RelayAttach(ctx, internal, "carrier-1", "v1", &relaySink{})
	require.NoError(t, err)
	detach()

	// 失败路径不留连接（join token 被拒后 relays 为空）。
	x.mu.Lock()
	_, online := x.relays["node-1"]
	x.mu.Unlock()
	assert.False(t, online, "a rejected credential must not register a relay")
}

// TestRelayAttachRejectsOtherCarrier（H10 §6 验收锚）：节点 A 的凭证 +
// hello 声明已锚定的节点 B carrier → contract.ErrExecRelayUnauthorized
// （凭证绑定轴与 hello 解析出的节点双向不匹配）；B 无连接，已有 A 连接不受
// 影响。
func TestRelayAttachRejectsOtherCarrier(t *testing.T) {
	x, _ := newTestSatellite(t)
	ctx := context.Background()
	require.NoError(t, x.repos.Nodes.Upsert(ctx, &node.Node{
		PlatformID: "node-2", CarrierID: "carrier-2", Hostname: "h2", Role: "worker", Available: true,
	}))

	// A 正常附着。
	aSink := &relaySink{}
	_, detachA, err := x.RelayAttach(ctx, testRelayCredential, "carrier-1", "v1", aSink)
	require.NoError(t, err)
	t.Cleanup(detachA)

	// A 的凭证冒充 B：拒。
	_, _, err = x.RelayAttach(ctx, testRelayCredential, "carrier-2", "v1", &relaySink{})
	require.ErrorIs(t, err, contract.ErrExecRelayUnauthorized,
		"a credential bound to one carrier must not attach as another carrier")

	x.mu.Lock()
	a := x.relays["node-1"]
	_, bOnline := x.relays["node-2"]
	x.mu.Unlock()
	assert.NotNil(t, a, "the existing A connection must stay registered")
	assert.False(t, bOnline, "the rejected connection must not be registered under B")
}

// TestRelayAttachRejectsUnanchoredCarrier（H10 §6 验收锚）：凭证行存在但
// ByCarrier 无行（节点尚未锚定）→ contract.ErrExecNodeUnanchored。
func TestRelayAttachRejectsUnanchoredCarrier(t *testing.T) {
	x, _ := newTestSatellite(t)
	ctx := context.Background()
	require.NoError(t, x.repos.Nodes.PutRelayCredential(ctx, &node.RelayCredential{
		PlatformID: "node-orphan", CarrierID: "carrier-orphan",
		SHA256: node.HashRelayCredential("fltrelay_orphan_credential"),
	}))

	_, _, err := x.RelayAttach(ctx, "fltrelay_orphan_credential", "carrier-orphan", "v1", &relaySink{})
	require.ErrorIs(t, err, contract.ErrExecNodeUnanchored,
		"a credential row without an anchored carrier must fail on the anchoring axis")

	x.mu.Lock()
	_, online := x.relays["node-orphan"]
	x.mu.Unlock()
	assert.False(t, online)
}

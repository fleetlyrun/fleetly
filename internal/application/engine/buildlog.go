package engine

// build 日志面聚合（P3 批 6 文件分组；P11/ADR-0040 决策 3 出口链）：脱敏
// 表 → writer 链 → VL 攒批器 + 环形缓冲（live 面）全家族同居一文件。
// builder 面向用户的日志出口唯一点是 executeBuild 构造的 writer——链形态：
//
//	redactWriter（脱敏，值→指纹短形态）→ 环形缓冲（既有 live 面）
//	                                     ↘ VL ingest（kind=build 持久化）
//
// 所有产生点（BuildKit SolveStatus 翻译/推送流/railpack 合成帧）不变，
// 脱敏天然单点；持久化 best-effort（失败丢批告警不阻断构建——live 面
// 恒在，诚实边界）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// logBuffer 是构建日志最近缓冲（诚实边界："仅实时+最近缓冲"，F0.9/F0.25
// 同口径；持久化检索 N2）。有界环形，最新优先快照供读取面。
//
// 帧带 per-Build 单调序列号（N0.1 P2-1）：回绕丢帧后读取面按 seq 续流，
// 不依赖 len(frames) 位置——"游标=长度"假设在环形截断后静默停发。
// 终态 Build 的缓冲保留最近 retainedTerminalBuilds 个，其余回收（frames
// map 只增不清会随时间泄漏）。
type logBuffer struct {
	mu       sync.Mutex
	frames   map[string][]seqFrame
	nextSeq  map[string]int64
	terminal []string // 终态完成序（旧→新）
	cap      int
}

// seqFrame 是带序列号的日志帧。
type seqFrame struct {
	seq int64
	f   capability.LogFrame
}

// retainedTerminalBuilds 是终态 Build 日志缓冲保留数（最近的可回读，更早
// 的回收；活跃 Build 不回收）。
const retainedTerminalBuilds = 8

func newLogBuffer(capacity int) *logBuffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &logBuffer{
		frames:  map[string][]seqFrame{},
		nextSeq: map[string]int64{},
		cap:     capacity,
	}
}

func (b *logBuffer) write(buildID string, f capability.LogFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := b.nextSeq[buildID]
	b.nextSeq[buildID] = seq + 1
	buf := append(b.frames[buildID], seqFrame{seq: seq, f: f})
	if len(buf) > b.cap {
		buf = buf[len(buf)-b.cap:]
	}
	b.frames[buildID] = buf
}

// recentAfter 返回 seq 大于 after 的帧（旧→新）与本次见到的最新 seq（无
// 新帧时原样回传 after——游标不动）。
func (b *logBuffer) recentAfter(buildID string, after int64) ([]capability.LogFrame, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []capability.LogFrame
	last := after
	for _, sf := range b.frames[buildID] {
		if sf.seq <= after {
			continue
		}
		out = append(out, sf.f)
		last = sf.seq
	}
	return out, last
}

// recent 返回最近缓冲快照（旧→新；首个消费面，apitest/golden 同形态）。
func (b *logBuffer) recent(buildID string) []capability.LogFrame {
	out, _ := b.recentAfter(buildID, -1)
	if out == nil {
		return []capability.LogFrame{}
	}
	return out
}

// markTerminal 登记终态完成并回收超龄终态缓冲（幂等：重复登记同 ID 忽略）。
func (b *logBuffer) markTerminal(buildID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.terminal); n > 0 && b.terminal[n-1] == buildID {
		return
	}
	b.terminal = append(b.terminal, buildID)
	if len(b.terminal) > retainedTerminalBuilds {
		drop := b.terminal[0]
		b.terminal = b.terminal[1:]
		delete(b.frames, drop)
		delete(b.nextSeq, drop)
	}
}

// bufferWriter 适配 LogWriter → 环形缓冲（P3 批 6 显式最小依赖：logs 即
// 目标环形缓冲活体，不挂 *Engine 回引——enginebackref 守卫执法）。
type bufferWriter struct {
	logs    *logBuffer
	buildID string
}

func (w *bufferWriter) WriteLog(_ context.Context, f capability.LogFrame) error {
	f.WorkloadID = w.buildID
	w.logs.write(w.buildID, f)
	return nil
}

// RecentBuildLogs 返回构建日志最近缓冲快照（旧→新；B4 读面消费）。
func (e *Engine) RecentBuildLogs(buildID string) []capability.LogFrame {
	return e.build.logs.recent(buildID)
}

// RecentBuildLogsAfter 返回 seq 大于 after 的日志帧（旧→新）与最新 seq
// （follow 续流游标；N0.1 P2-1）。
func (e *Engine) RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64) {
	return e.build.logs.recentAfter(buildID, after)
}

// redactMinValueLen 是进脱敏表的最小值长度（短值误替换率高——普通词撞
// 秘密的概率随长度骤降；8 字节是 P11 的诚实阈值，ADR-0040 决策 3）。
const redactMinValueLen = 8

// redactTable 是构建日志出口脱敏表（P11）：值 → 指纹短形态
// `secret:<sha256 前 8 hex>`（不可逆、同值同形、可 grep 对账）。替换序
// 按值长降序（长值优先——避免短值前缀先替换破坏长值匹配）。
type redactTable struct {
	entries []redactEntry
}

type redactEntry struct {
	value   string
	display string
}

// newRedactTable 从 Secret 值集铸造脱敏表（nil/空集 = 恒等直通）。重复值
// 去重；短值跳过。
func newRedactTable(values []string) *redactTable {
	seen := make(map[string]bool, len(values))
	entries := make([]redactEntry, 0, len(values))
	for _, v := range values {
		if len(v) < redactMinValueLen || seen[v] {
			continue
		}
		seen[v] = true
		sum := sha256.Sum256([]byte(v))
		entries = append(entries, redactEntry{value: v, display: "secret:" + hex.EncodeToString(sum[:])[:8]})
	}
	sort.Slice(entries, func(i, j int) bool { return len(entries[i].value) > len(entries[j].value) })
	return &redactTable{entries: entries}
}

// redact 替换行内全部 Secret 值出现（零表 = 原样返回）。
func (r *redactTable) redact(line []byte) []byte {
	for i := range r.entries {
		line = bytes.ReplaceAll(line, []byte(r.entries[i].value), []byte(r.entries[i].display))
	}
	return line
}

// buildRedactionTable 从构建输入铸造脱敏表：推送凭证 Secret（构建上下文
// 中唯一真实流动的 Secret）+ SecretFiles 值（预留面首次消费，P11 措辞
// "Secret 值注入构建 env 时同步登记"的承载点）。
func buildRedactionTable(input *capability.BuildRequest) *redactTable {
	var values []string
	if input.PushCred != nil && input.PushCred.Secret != "" {
		values = append(values, input.PushCred.Secret)
	}
	for _, v := range input.SecretFiles {
		if len(v) > 0 {
			values = append(values, string(v))
		}
	}
	return newRedactTable(values)
}

// buildLogWriter 是 builder.Build 的 writer 实参（出口单点）：脱敏 → 环形
// 缓冲（live 面）+ VL ingest 攒批（持久化面）。
type buildLogWriter struct {
	inner  *bufferWriter
	redact *redactTable
	ingest *buildLogIngester // nil = Logging 面停用（环形缓冲单飞——现状）
}

func (w *buildLogWriter) WriteLog(ctx context.Context, f capability.LogFrame) error {
	f.Line = w.redact.redact(f.Line)
	if w.ingest != nil {
		w.ingest.append(&f)
	}
	return w.inner.WriteLog(ctx, f)
}

// buildLogIngester 是 build 日志的 VL 攒批器（域归因盖戳 Kind=build、
// Source=BuildID；达量/终批 flush，best-effort）。P3 批 6 显式最小依赖：
// logging/log 直持端口与日志器，不挂 *Engine 回引（enginebackref 守卫
// 执法：域活体禁挂壳回引）。
type buildLogIngester struct {
	logging capability.Logging // 受管日志存储端口（nil = Logging 面停用，flush 直通清批）
	log     *slog.Logger       // 丢批告警日志器
	buildID string
	team    string
	project string
	app     string
	frames  []capability.LogFrame
	dropped int
}

func (g *buildLogIngester) append(f *capability.LogFrame) {
	f.Team, f.Project, f.App = g.team, g.project, g.app
	f.Kind = capability.LogKindBuild
	f.Source = g.buildID
	g.frames = append(g.frames, *f)
	if len(g.frames) >= logIngestBatchFrames {
		g.flush(context.Background())
	}
}

// flush 终批入库（构建收尾/达量触发；失败丢批告警不阻断——live 面恒在）。
func (g *buildLogIngester) flush(ctx context.Context) {
	if len(g.frames) == 0 || g.logging == nil {
		g.frames = nil
		return
	}
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := g.logging.Ingest(ictx, g.frames); err != nil {
		g.dropped += len(g.frames)
		g.log.Warn("build log ingest failed; persistence skipped for this batch",
			"build", g.buildID, "dropped", g.dropped, "err", err)
	}
	g.frames = nil
}

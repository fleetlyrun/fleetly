package engine

// build 日志出口链（P11/ADR-0040 决策 3）：builder 面向用户的日志出口唯一
// 点是 executeBuild 构造的 writer——本文件把它升级为链：
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
	"sort"
	"time"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

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
// Source=BuildID；达量/终批 flush，best-effort）。
type buildLogIngester struct {
	e       *Engine
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
	if len(g.frames) == 0 || g.e.logging == nil {
		g.frames = nil
		return
	}
	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := g.e.logging.Ingest(ictx, g.frames); err != nil {
		g.dropped += len(g.frames)
		g.e.log.Warn("build log ingest failed; persistence skipped for this batch",
			"build", g.buildID, "dropped", g.dropped, "err", err)
	}
	g.frames = nil
}

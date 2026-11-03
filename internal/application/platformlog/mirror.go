// Package platformlog 承载控制面自身日志镜像（ADR-0091）：daemon 的
// slog 流经 tee handler 追加进数据根下自轮转日志文件（<DataRoot>/logs/
// fleetlyd.log，16MB×4），新 RPC 从服务端直读该文件——控制面排障不再
// 依赖主机登录（journald）或 VictoriaLogs（诊断通道不得与被诊断组件共
// 命运，2026-11-03 用户裁决）。
//
// 写侧纪律（best-effort，主日志径零风险）：tee 在调用方 goroutine 序列化
// 单行 JSON 后非阻塞入队；单写 goroutine 消费并按字节界轮转；队列满/写
// 失败/盘满 = 丢弃计数，永不阻塞、永不影响 stdout/journald 主径。镜像行
// 格式与 daemon stdout 的 zap JSON 行同字段习惯（ts/level/msg 小写、
// duration 秒浮点），但属独立契约（reader 单源解析自己的写入面）。
package platformlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// 镜像文件布局常量（ADR-0091 决策 1）。
const (
	// LogDirName 是数据根下的镜像目录名。
	LogDirName = "logs"
	// FileName 是活跃镜像文件名（轮转产物为 <FileName>.1~<FileName>.3）。
	FileName = "fleetlyd.log"
	// MaxFileBytes 是单文件轮转界（16MB）。
	MaxFileBytes = 16 << 20
	// MaxFiles 是轮转保留份数（活跃 1 + 历史 3 = 共 4 份，~64MB 上界）。
	MaxFiles = 4
	// queueDepth 是异步队列深度；满即丢弃（best-effort 契约）。
	queueDepth = 1024
	// timeFormat 是镜像行 ts 字段形态（与 daemon stdout 的 zap ISO8601
	// 毫秒形态对齐；解析侧按 RFC3339 宽容）。
	timeFormat = "2006-01-02T15:04:05.000Z07:00"
)

// Mirror 是控制面日志镜像（tee 写侧）：OpenMirror 打开文件并启动单写
// goroutine；TeeHandler 装饰主 handler；Close 排空队列后落盘退出（幂等）。
type Mirror struct {
	path      string
	fixedJSON []byte // 预格式化固定字段前缀（,"service.name":… 形态；空 = 无）
	ch        chan []byte
	drops     atomic.Int64
	done      chan struct{}

	// mu 串行化 offer 的 send 与 Close 的 close(ch)——裸并发是 data race
	//（send 对已关 channel 即 panic；race 检测器实测咬出，2026-11-03）。
	// 日志 QPS 低（每请求两行），锁竞争不构成路径负担。
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	maxBytes  int64 // 轮转界（生产 = MaxFileBytes；测试注入小界加速轮转）
}

// OpenMirror 打开（或创建）<dataRoot>/logs/fleetlyd.log 镜像。fixed 是
// 每行携带的固定字段（service.name/version/id 等身份面；nil = 无）。
// 调用点是 daemon 的 logger provider——先于一切日志行存在，无 boot 缓冲
// 必要（logger 创建点即镜像起点）。
func OpenMirror(dataRoot string, fixed map[string]string) (*Mirror, error) {
	return openMirror(dataRoot, fixed, MaxFileBytes)
}

func openMirror(dataRoot string, fixed map[string]string, maxBytes int64) (*Mirror, error) {
	dir := filepath.Join(dataRoot, LogDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("platformlog: create mirror dir: %w", err)
	}
	m := &Mirror{
		path:      filepath.Join(dir, FileName),
		fixedJSON: fixedJSONPrefix(fixed),
		ch:        make(chan []byte, queueDepth),
		done:      make(chan struct{}),
		maxBytes:  maxBytes,
	}
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("platformlog: open mirror file: %w", err)
	}
	go m.writeLoop(f)
	return m, nil
}

// TeeHandler 返回镜像 tee：next 是主 handler（lynxzap → stdout/journald，
// 行为零变化），镜像侧 best-effort。镜像级别面自持（Info+，见 Enabled）。
func (m *Mirror) TeeHandler(next slog.Handler) slog.Handler {
	return teeHandler{next: next, m: m}
}

// Close 关停镜像：关闭队列（写 goroutine 排空存量后退出）并等待落定。
// 幂等；进程退出未调 Close 时最多丢队列内残留行（best-effort 契约）。
func (m *Mirror) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		close(m.ch)
		m.mu.Unlock()
	})
	<-m.done
	return nil
}

// Drops 返回累计丢弃行数（队列满计数；诊断面/测试锚）。
func (m *Mirror) Drops() int64 { return m.drops.Load() }

// writeLoop 是单写 goroutine：消费队列、按字节界轮转。写失败（盘满/fd
// 坏）= 丢弃并轮转重开（下一行起再试；极端环境镜像自愈到可用为止）。
func (m *Mirror) writeLoop(f *os.File) {
	defer close(m.done)
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	for line := range m.ch {
		if size > 0 && size+int64(len(line)) > m.maxBytes {
			m.rotate(f)
			nf, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				continue // 文件不可开：丢行；队列关闭后本循环自然退出
			}
			f = nf
			size = 0
		}
		n, err := f.Write(line)
		size += int64(n)
		if err != nil {
			m.rotate(f) // fd 疑坏：轮转重开给下一行机会
			if nf, nerr := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); nerr == nil {
				f = nf
				size = 0
			}
		}
	}
	_ = f.Close()
}

// rotate 关当前文件并按龄移位（.log.2→.log.3、.log.1→.log.2、.log→.log.1；
// 最老一份被顶出删除）。失败容忍（残留旧文件只占盘不破坏正确性）。
func (m *Mirror) rotate(f *os.File) {
	_ = f.Close()
	_ = os.Remove(m.suffixed(MaxFiles - 1))
	for i := MaxFiles - 2; i >= 1; i-- {
		_ = os.Rename(m.suffixed(i), m.suffixed(i+1))
	}
	_ = os.Rename(m.path, m.suffixed(1))
}

func (m *Mirror) suffixed(i int) string { return fmt.Sprintf("%s.%d", m.path, i) }

// offer 非阻塞入队；满/已关 = 丢弃计数（调用方是主日志径，绝不阻塞）。
func (m *Mirror) offer(line []byte) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.drops.Add(1)
		return
	}
	select {
	case m.ch <- line:
	default:
		m.drops.Add(1)
	}
	m.mu.Unlock()
}

// teeHandler 是 slog.Handler 装饰器：主径先走（错误原样上抛），镜像侧
// 无论主径成败都投递（主径失败本身就是要诊断的对象）。
type teeHandler struct {
	next slog.Handler
	m    *Mirror
}

// Enabled 是镜像自持级别面（Info+，常量）：不委托 next——主径 handler
// 可能整面停用（测试夹具 DiscardHandler 恒 false，委托会让镜像一并失明，
// apitest 实咬）。主径是否落 stdout 由 next 自决；镜像宁全勿缺，Debug 以下
// 双方共识不收（daemon 生产级别即 info）。
func (h teeHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (h teeHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.next.Handle(ctx, r)
	h.m.offer(formatRecord(h.m.fixedJSON, &r))
	return err
}

func (h teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return teeHandler{next: h.next.WithAttrs(attrs), m: h.m}
}

func (h teeHandler) WithGroup(name string) slog.Handler {
	return teeHandler{next: h.next.WithGroup(name), m: h.m}
}

// formatRecord 序列化单行镜像 JSON（调用方 goroutine——slog attrs 须在
// 调用栈内 resolve，跨 goroutine 传 []byte 不传 Record）。
func formatRecord(fixedJSON []byte, r *slog.Record) []byte {
	var b bytes.Buffer
	b.WriteString(`{"ts":`)
	appendJSONString(&b, r.Time.UTC().Format(timeFormat))
	b.WriteString(`,"level":`)
	appendJSONString(&b, strings.ToLower(r.Level.String()))
	b.WriteString(`,"msg":`)
	appendJSONString(&b, r.Message)
	b.Write(fixedJSON)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteByte(',')
		appendJSONString(&b, a.Key)
		b.WriteByte(':')
		appendAttrValue(&b, a.Value)
		return true
	})
	b.WriteString("}\n")
	return b.Bytes()
}

// fixedJSONPrefix 预格式化固定字段（键字典序保证确定性）。
func fixedJSONPrefix(fixed map[string]string) []byte {
	if len(fixed) == 0 {
		return nil
	}
	keys := make([]string, 0, len(fixed))
	for k := range fixed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteByte(',')
		appendJSONString(&b, k)
		b.WriteByte(':')
		appendJSONString(&b, fixed[k])
	}
	return b.Bytes()
}

func appendAttrValue(b *bytes.Buffer, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		appendJSONString(b, v.String())
	case slog.KindInt64:
		fmt.Fprintf(b, "%d", v.Int64())
	case slog.KindUint64:
		fmt.Fprintf(b, "%d", v.Uint64())
	case slog.KindFloat64:
		// JSON 数字形态（zap 对齐：普通浮点直写）。
		fmt.Fprintf(b, "%v", v.Float64())
	case slog.KindBool:
		fmt.Fprintf(b, "%v", v.Bool())
	case slog.KindDuration:
		// 秒浮点（daemon stdout 的 zap duration 同形态，0.000877837）。
		fmt.Fprintf(b, "%v", v.Duration().Seconds())
	case slog.KindTime:
		appendJSONString(b, v.Time().UTC().Format(timeFormat))
	default:
		any := v.Any()
		if err, ok := any.(error); ok {
			appendJSONString(b, err.Error())
			return
		}
		enc, err := json.Marshal(any)
		if err != nil {
			appendJSONString(b, fmt.Sprint(any))
			return
		}
		b.Write(enc)
	}
}

func appendJSONString(b *bytes.Buffer, s string) {
	enc, err := json.Marshal(s)
	if err != nil { // string 恒可编（Marshal 对 string 不失败；防御分支）
		b.WriteString(`""`)
		return
	}
	b.Write(enc)
}

package logs

import (
	"io"
	"log/slog"
)

// discardLogger 是缺省静默 logger（测试/构造兜底）。
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ring 是 per app-service 有界环形缓冲（T2.20 验收：ring buffer 限深）。
// 追加超容量时挤掉最旧条目；回放（Follow 接入）按序输出当前快照。
type ring struct {
	buf  []Entry
	next int
	full bool
}

func newRing(size int) *ring {
	if size <= 0 {
		size = 1
	}
	return &ring{buf: make([]Entry, size)}
}

// append 追加一条（覆盖最旧）。
func (r *ring) append(e Entry) {
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// snapshot 按时间序返回当前缓冲内容（切片拷贝，容量以下时更短）。
func (r *ring) snapshot() []Entry {
	if !r.full {
		out := make([]Entry, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]Entry, len(r.buf))
	copy(out, r.buf[r.next:])
	copy(out[len(r.buf)-r.next:], r.buf[:r.next])
	return out
}

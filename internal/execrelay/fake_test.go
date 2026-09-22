package execrelay

// relay 侧单测的假实现集：MessageConn（帧录制 + 注入）与 execDocker
//（label/探测/attach 行为可编排——label 卫兵、shell 白名单、时限注入缝的
// 验收面）。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeConn 是 MessageConn 假实现：Write 录制、Read 从注入通道取帧。
type fakeConn struct {
	mu      sync.Mutex
	written [][]byte
	inbound chan []byte
	closed  bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{inbound: make(chan []byte, 32)}
}

func (c *fakeConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m, ok := <-c.inbound:
		if !ok {
			return nil, errors.New("connection closed")
		}
		return m, nil
	}
}

func (c *fakeConn) Write(_ context.Context, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	c.written = append(c.written, append([]byte(nil), p...))
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

// isClosed 报告连接是否已关。
func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// feed 注入一条入站帧。
func (c *fakeConn) feed(msg []byte) {
	c.inbound <- msg
}

// writtenDecoded 解码全部已写帧为 (类型, 载荷)。
func (c *fakeConn) writtenDecoded(t *testing.T) []frameTuple {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]frameTuple, 0, len(c.written))
	for _, w := range c.written {
		typ, payload, err := DecodeFrame(w)
		if err != nil {
			t.Fatalf("decode written frame: %v", err)
		}
		out = append(out, frameTuple{typ: typ, payload: payload})
	}
	return out
}

// waitForWritten 轮询直到谓词命中（谓词收到全部已写帧快照）。
func (c *fakeConn) waitForWritten(t *testing.T, budget time.Duration, pred func([]frameTuple) bool) []frameTuple {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		frames := c.writtenDecoded(t)
		if pred(frames) {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("written frames did not satisfy predicate within %s (got %d frames)", budget, len(frames))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type frameTuple struct {
	typ     FrameType
	payload []byte
}

// closeFrameOf 解码一条 session.close 帧载荷。
func closeFrameOf(t *testing.T, f frameTuple) SessionCloseFrame {
	t.Helper()
	var c SessionCloseFrame
	if err := DecodeJSONPayload(f.payload, &c); err != nil {
		t.Fatalf("decode close frame: %v", err)
	}
	return c
}

// ── 假 execDocker ────────────────────────────────────────────────────────────

// fakeExecStream 是 ExecStream 假实现：读侧从通道取（模拟容器输出）、写侧
// 录制（模拟 stdin）。
type fakeExecStream struct {
	readCh  chan []byte
	mu      sync.Mutex
	written [][]byte
	closed  bool
}

func newFakeExecStream() *fakeExecStream {
	return &fakeExecStream{readCh: make(chan []byte, 16)}
}

func (s *fakeExecStream) Read(p []byte) (int, error) {
	m, ok := <-s.readCh
	if !ok {
		return 0, errors.New("stream closed")
	}
	n := copy(p, m)
	return n, nil
}

func (s *fakeExecStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, append([]byte(nil), p...))
	return len(p), nil
}

func (s *fakeExecStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.readCh)
	return nil
}

func (s *fakeExecStream) ExecID() string { return "exec-1" }

func (s *fakeExecStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// fakeDocker 是 execDocker 假实现（编排面：label 值空 = 无 label；probeCodes
// 缺项 = 探测失败；stream 缺失 = attach 失败）。
type fakeDocker struct {
	mu            sync.Mutex
	appLabel      string
	missing       bool
	probeCodes    map[string]int
	attachErr     error
	stream        *fakeExecStream
	resized       [][2]uint16
	attachCalls   int
	attachedShell string
}

func (d *fakeDocker) ContainerInspect(_ context.Context, _ string) (ContainerInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.missing {
		return ContainerInfo{}, ErrContainerNotFound
	}
	return ContainerInfo{AppLabel: d.appLabel}, nil
}

func (d *fakeDocker) ExecProbe(_ context.Context, _ string, cmd []string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(cmd) == 0 {
		return 0, errors.New("empty probe cmd")
	}
	code, ok := d.probeCodes[cmd[0]]
	if !ok {
		return 0, errors.New("probe not available for " + cmd[0])
	}
	return code, nil
}

func (d *fakeDocker) ExecAttachPTY(_ context.Context, _, shell string, cols, rows uint16) (ExecStream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attachCalls++
	if d.attachErr != nil {
		return nil, d.attachErr
	}
	if d.stream == nil {
		return nil, errors.New("no stream staged")
	}
	d.attachedShell = shell
	_ = cols
	_ = rows
	return d.stream, nil
}

func (d *fakeDocker) ExecResize(_ context.Context, _ string, cols, rows uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resized = append(d.resized, [2]uint16{cols, rows})
	return nil
}

// writtenStdin 是 exec 流收到的 stdin 总和（字符串形态）。
func writtenStdin(s *fakeExecStream) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, w := range s.written {
		b.Write(w)
	}
	return b.String()
}

// testLogger 是静默测试日志（discard——失败信息走 t.Fatalf）。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// eventually 轮询谓词直到命中（测试主等待面——不引入 sleep 断言）。
func eventually(t *testing.T, budget time.Duration, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !pred() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s: %s", budget, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

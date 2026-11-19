package activator

// 唤醒面行为锚（P2-1a 第三刀；设计 v2 §1.4 三段式）：快径直转（零冷
// 路径头）、体帽 413、扣留帽 429、deadline 503 + Retry-After、冷路径
// X-Fleetly-Wake 头、唤醒拍幂等（N 请求一次 Wake 落账）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFaces 是三窄接口的测试双（Host→函数映射、Wake 计数、Resolve 可
// 编程状态机）。
type fakeFaces struct {
	mu        sync.Mutex
	hostToFn  map[string]string
	wakeCalls int
	// resolveScript 是逐次 Resolve 返回（nil 尾 = 恒末值）。
	resolveScript []resolveResult
}

type resolveResult struct {
	addr  string
	ready bool
}

func (f *fakeFaces) FunctionIDForHost(_ context.Context, host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hostToFn[host], nil
}

func (f *fakeFaces) Wake(_ context.Context, functionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakeCalls++
	return nil
}

func (f *fakeFaces) Resolve(_ context.Context, functionID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.resolveScript) == 0 {
		return "", false, nil
	}
	r := f.resolveScript[0]
	if len(f.resolveScript) > 1 {
		f.resolveScript = f.resolveScript[1:]
	}
	return r.addr, r.ready, nil
}

func newTestHandler(f *fakeFaces) *Handler {
	return New(Config{
		WakeDeadline:       2 * time.Second,
		MaxHeldPerFunction: 2,
		PollInterval:       10 * time.Millisecond,
		BodyLimit:          1024,
	}, f, f, f)
}

// 快径：已醒（首拍 ready）即直转，零 Wake 调用、零冷路径头。
func TestAwakeFastPathForwards(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{"img.fns.test": "fn-1"}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("PROTO-LINE HTTP/1.1"))
	}))
	defer backend.Close()
	f.resolveScript = []resolveResult{{addr: backend.Listener.Addr().String(), ready: true}}
	h := newTestHandler(f)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "PROTO-LINE")
	assert.Empty(t, rec.Header().Get("X-Fleetly-Wake"), "fast path carries no wake header")
	assert.Equal(t, 0, f.wakeCalls, "awake function triggers no wake")
}

// 冷路径：parked（首拍 not ready）→ Wake 落账一次 → 就绪后透传 +
// X-Fleetly-Wake 头（幂等合并：一次扣留恰一次 Wake）。
func TestColdPathWakesThenForwards(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{"img.fns.test": "fn-1"}}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "1", r.Header.Get("X-Fleetly-Wake"), "cold forward carries the wake header")
		_, _ = w.Write([]byte("PROTO-LINE HTTP/1.1"))
	}))
	defer backend.Close()
	f.resolveScript = []resolveResult{
		{ready: false}, // parked（快径判定失败）
		{ready: false}, // 扣留后第一拍（仍在收敛）
		{addr: backend.Listener.Addr().String(), ready: true},
	}
	h := newTestHandler(f)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "PROTO-LINE")
	assert.Equal(t, 1, f.wakeCalls, "held request triggers exactly one wake")
}

// deadline：持续 not ready → 503 + Retry-After（诚实快速失败）。
func TestDeadlineFails503(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{"img.fns.test": "fn-1"}}
	f.resolveScript = []resolveResult{{ready: false}} // 恒 parked
	h := newTestHandler(h2_cfg(f))

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// 扣留帽：MaxHeldPerFunction=2 → 第三个并发扣留 429（唤醒风暴防线）。
// 两席由独立短窗 handler 占用（各自 300ms deadline 自然退出）；主断言在
// 占用窗内打第三请求。
func TestHeldCapFails429(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{"img.fns.test": "fn-1"}}
	f.resolveScript = []resolveResult{{ready: false}}
	occupier := New(Config{
		WakeDeadline:       300 * time.Millisecond,
		MaxHeldPerFunction: 2,
		PollInterval:       20 * time.Millisecond,
	}, f, f, f)

	// 占用面：用 occupier 的两席证明 cap 生效（第三请求 429）。
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
			rec := httptest.NewRecorder()
			occupier.ServeHTTP(rec, req)
		}()
	}
	// 等两席占用到位。
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 2, occupier.HeldCount("fn-1"))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
	rec := httptest.NewRecorder()
	occupier.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "third concurrent hold gets 429")
	wg.Wait() // 两席 deadline 到自然退出
}

// 体帽：ContentLength > BodyLimit → 413（大载荷诚实降级）。
func TestBodyLimitFails413(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{"img.fns.test": "fn-1"}}
	f.resolveScript = []resolveResult{{ready: false}} // 进冷路径才检查体
	h := newTestHandler(f)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://img.fns.test/", nil)
	req.ContentLength = 4096 // > BodyLimit 1024
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// 未知 Host → 404（无函数映射）。
func TestUnknownHost404(t *testing.T) {
	f := &fakeFaces{hostToFn: map[string]string{}}
	h := newTestHandler(f)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://nowhere.test/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// h2_cfg 给 deadline 测试一个短窗（不拖慢套件）。
func h2_cfg(f *fakeFaces) *fakeFaces {
	f.resolveScript = []resolveResult{{ready: false}}
	return f
}

// 编译期：Handler 满足 http.Handler。
var _ http.Handler = (*Handler)(nil)

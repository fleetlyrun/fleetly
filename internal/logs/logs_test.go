package logs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

// fakePort 是测试用底座端口：可编程的流产物。
type fakePort struct {
	mu       sync.Mutex
	services map[string][]string // app -> services
	lines    map[string][]substrate.LogLine
}

func newFakePort() *fakePort {
	return &fakePort{
		services: map[string][]string{},
		lines:    map[string][]substrate.LogLine{},
	}
}

func (f *fakePort) setApp(app string, services ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services[app] = services
}

func (f *fakePort) emit(service string, lines ...substrate.LogLine) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines[service] = append(f.lines[service], lines...)
}

func (f *fakePort) ManagedServiceProcesses(_ context.Context, app string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.services[app], nil
}

func (f *fakePort) StreamServiceLogs(_ context.Context, service string, since time.Time, _ bool) (<-chan substrate.LogLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(chan substrate.LogLine)
	go func() {
		defer close(out)
		for _, l := range f.lines[service] {
			// 轮询游标语义：只投递 since 之后的行。
			if !since.IsZero() && !l.At.After(since) {
				continue
			}
			out <- l
		}
	}()
	return out, nil
}

// newTestManager 构造接 fake 端口的 Manager（加速轮询、独立落盘目录、
// 真实 secrets.Box——脱敏链路端到端）。
func newTestManager(t *testing.T) (*Manager, *fakePort, *state.Store, *secrets.Box) {
	t.Helper()
	st := openStateStub(t)
	port := newFakePort()
	dir := t.TempDir()
	cfg := Config{Dir: filepath.Join(dir, "logs"), RetentionDays: 7, ScanIntervalMillis: 10, RingSize: 4}
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	mg := NewManager(cfg, st, port, box, discardLogger())
	return mg, port, st, box
}

// openStateStub 打开测试用状态库（temp 目录独立，os.Args/CWD 不触碰）。
func openStateStub(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

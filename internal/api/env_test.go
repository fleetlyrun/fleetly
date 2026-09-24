package api

// env 写路径联动测试（H9：env set/remove 成功后即时失效日志脱敏值集——
// 30s TTL 窗内新 secret 值会被明文采集并按天落盘保留 7 天，写点失效把
// 暴露窗收敛到单次重建）。服务直连单测（不经 gRPC）：回调注入与触发点位
// 是本测的对象，鉴权/传输链与其正交。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// TestEnvServiceInvalidationHook H9：SetEnv/RemoveEnv 写点成功后触发失效
// 回调（appID 载荷）；失败路径（删除不存在的键 → 404）不触发。
func TestEnvServiceInvalidationHook(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	box, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key"))
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, st, "hookapp")
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}

	var mu sync.Mutex
	var fired []string
	svc := NewEnvService(st, box).WithEnvChangedHook(func(appID string) {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, appID)
	})

	if _, err := svc.SetEnv(directCtx(ctx), &serverv1.SetEnvRequest{App: "hookapp", Key: "API_KEY", Value: "v-1-secret-value"}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	if _, err := svc.RemoveEnv(directCtx(ctx), &serverv1.RemoveEnvRequest{App: "hookapp", Key: "API_KEY"}); err != nil {
		t.Fatalf("RemoveEnv: %v", err)
	}
	mu.Lock()
	got := append([]string{}, fired...)
	mu.Unlock()
	if len(got) != 2 || got[0] != app.ID || got[1] != app.ID {
		t.Fatalf("invalidation calls = %v, want [%s %s]", got, app.ID, app.ID)
	}

	// 失败路径不触发：删除不存在的键 → 404，回调不调用。
	if _, err := svc.RemoveEnv(directCtx(ctx), &serverv1.RemoveEnvRequest{App: "hookapp", Key: "GONE"}); err == nil {
		t.Fatal("remove missing key should fail (404)")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 2 {
		t.Fatalf("failure path fired callback: %v", fired)
	}
}

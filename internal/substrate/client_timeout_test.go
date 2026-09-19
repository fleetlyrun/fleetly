package substrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// D2（S17 类 D：超时与取消闭环）挂起注入测试：假 Docker API 永不返回
// （handler 阻塞在门闸上）——非流式调用必须在注入预算（1s）内以
// DeadlineExceeded 终结，而非无限阻塞（dockerd 假死形态）。

// newHangingDockerAPI 起一个永不响应的假 Docker API（所有路径挂住；
// Cleanup 时放行并关闭，避免泄漏阻塞的 handler goroutine）。
func newHangingDockerAPI(t *testing.T) *Client {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 挂住：不读不写不返回
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("construct client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestNonStreamingCallDeadlineBound 非流式调用面（D2）：每个包装方法在
// 预算内限时失败。调用方 ctx 给 10s——若 per-call 预算缺失将挂满 10s
// （断言上限 3s 即拦住该回归）。
func TestNonStreamingCallDeadlineBound(t *testing.T) {
	c := newHangingDockerAPI(t)

	orig := defaultCallTimeout
	defaultCallTimeout = time.Second // 测试注入缩短预算
	t.Cleanup(func() { defaultCallTimeout = orig })

	cases := []struct {
		name string
		call func(ctx context.Context) error
	}{
		// client.go：Info / NodeList / NodeInspect / NodeUpdate
		{"SelfNodeID(Info)", func(ctx context.Context) error { _, err := c.SelfNodeID(ctx); return err }},
		{"ListNodeObservations(NodeList)", func(ctx context.Context) error { _, err := c.ListNodeObservations(ctx); return err }},
		{"UpdateNodeLabel(NodeInspect+NodeUpdate)", func(ctx context.Context) error {
			return c.UpdateNodeLabel(ctx, "n1", "k", "v", state.ObjectVersion{})
		}},
		{"ResolveObjectVersion(ServiceInspect)", func(ctx context.Context) error {
			_, err := c.ResolveObjectVersion(ctx, state.ObjectKindService, "web")
			return err
		}},
		// services.go：ServiceCreate/Update/Remove/Inspect/List + TaskList +
		// NetworkEnsure + SwarmReady + ImageDigest
		{"ServiceCreate", func(ctx context.Context) error { return c.ServiceCreate(ctx, engine.ServiceSpec{Name: "web"}) }},
		{"ServiceUpdate(Inspect+Update)", func(ctx context.Context) error {
			return c.ServiceUpdate(ctx, "web", engine.ServiceSpec{Name: "web"})
		}},
		{"ServiceRemove", func(ctx context.Context) error { return c.ServiceRemove(ctx, "web") }},
		{"ServiceInspect", func(ctx context.Context) error { _, err := c.ServiceInspect(ctx, "web"); return err }},
		{"ServiceList", func(ctx context.Context) error { _, err := c.ServiceList(ctx, nil); return err }},
		{"TaskList", func(ctx context.Context) error { _, err := c.TaskList(ctx, "web"); return err }},
		{"NetworkEnsure(Inspect+Create)", func(ctx context.Context) error { return c.NetworkEnsure(ctx, "net-x") }},
		{"SwarmReady(Info)", func(ctx context.Context) error { return c.SwarmReady(ctx) }},
		{"ImageDigest(ImageInspect)", func(ctx context.Context) error { _, err := c.ImageDigest(ctx, "nginx:1"); return err }},
		// images.go：InspectImage / LoadImage / Volume / Container / Tag /
		// Remove。LoadImage 以挂起 reader 驱动（装载调用挂住）。
		{"InspectImage(ImageInspect)", func(ctx context.Context) error { _, err := c.InspectImage(ctx, "nginx:1"); return err }},
		{"LoadImage(ImageLoad)", func(ctx context.Context) error { return c.LoadImage(ctx, hangingReader{}) }},
		{"EnsureVolumePresent(Inspect+Create)", func(ctx context.Context) error { return c.EnsureVolumePresent(ctx, "vol") }},
		{"TagImage", func(ctx context.Context) error { return c.TagImage(ctx, "a:1", "b:2") }},
		{"RemoveImage", func(ctx context.Context) error { return c.RemoveImage(ctx, "a:1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			start := time.Now()
			err := tc.call(ctx)
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("call not bounded by per-call timeout: elapsed %v (want ≤3s)", elapsed)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("want context.DeadlineExceeded, got %v", err)
			}
		})
	}
}

// hangingReader 是永不产出字节的 tar 流（ImageLoad 挂起注入面）。
type hangingReader struct{}

func (hangingReader) Read([]byte) (int, error) {
	time.Sleep(10 * time.Second) // 挂住：无数据可读（预算先到即切断）
	return 0, nil
}

// TestPingUsesCallerContext D2 排除面钉死：Ping（健康探测）不走 per-call
// 预算——由调用方 ctx 管理（500ms 调用方 deadline 内终结，证明既未被
// 30s 预算接管、也非无限挂起）。
func TestPingUsesCallerContext(t *testing.T) {
	c := newHangingDockerAPI(t)

	orig := defaultCallTimeout
	defaultCallTimeout = 30 * time.Second // 显式钉回缺省（防同包前序注入污染）
	t.Cleanup(func() { defaultCallTimeout = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Ping(ctx)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ping not bounded by caller context: elapsed %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded (caller ctx), got %v", err)
	}
}

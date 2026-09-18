//go:build fleetly_docker

package substrate_test

// 实机集成测试（T2-7 日志管线验收第 4 项）：ManagedServiceProcesses 服务
// 发现与 StreamServiceLogs 真实日志流（stdcopy 解帧 + 时间戳头部解析）。
//
// 不进 CI（CI 无 docker）。跑法（本机 Docker 可用、且有 fleetly 受管服务
// 在跑时）：
//
//	go test -tags fleetly_docker ./internal/substrate/ -run TestRealServiceLogStream -v
//
// 环境变量 FLEETLY_LOG_APP 指定目标应用（缺省 t27web——T2-7 实机部署的
// 探针应用；不存在则测试跳过）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/naming"
)

func TestRealServiceLogStream(t *testing.T) {
	c := newRealClient(t)
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app := os.Getenv("FLEETLY_LOG_APP")
	if app == "" {
		app = "t27web"
	}
	svcs, err := c.ManagedServiceProcesses(ctx, app)
	if err != nil {
		t.Fatalf("ManagedServiceProcesses: %v", err)
	}
	if len(svcs) == 0 {
		t.Skipf("no managed services for app %s (deploy one first)", app)
	}
	swarmName, err := naming.ServiceName(app, svcs[0])
	if err != nil {
		t.Fatalf("ServiceName: %v", err)
	}
	lines, err := c.StreamServiceLogs(ctx, swarmName, time.Now().Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("StreamServiceLogs: %v", err)
	}
	n := 0
	for l := range lines {
		if n < 3 {
			t.Logf("line: at=%s stderr=%v content=%q", l.At.Format(time.RFC3339Nano), l.Stderr, l.Line)
			if l.At.IsZero() {
				t.Errorf("line %d has zero timestamp (header parse failed)", n)
			}
			if l.Line == "" {
				t.Errorf("line %d has empty content", n)
			}
		}
		n++
	}
	if n == 0 {
		t.Skip("service produced no logs in window (app may just be idle)")
	}
	t.Logf("total lines: %d", n)
}

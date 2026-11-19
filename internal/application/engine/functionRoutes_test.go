package engine

// P2-0 修复锚（Phase 2 设计 v2 §1.6 咬出）：函数公网集指纹必须含
// replicas——缩零/唤醒（0↔rung）改合成路由合并集（parked 期后端指唤醒
// 组件），漏并 = 发布签名短路吞掉变更（§6 mandatory① 同型漏洞）。
import (
	"context"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func TestFunctionsPublicFingerprintCoversReplicas(t *testing.T) {
	e, _, _ := newTestEngine(t)
	ctx := context.Background()
	const (
		projectID = "01JD0PROJ00000000000000000"
		fnID      = "01JDFP000000000000000000"
	)
	statetest.SeedFunction(t, e.db, fnID, projectID, "pub-fn")
	// 公开行：指纹随 replicas 变化（0 ↔ 1 两次读）。
	if err := e.repos.Functions.SetExposure(ctx, fnID, "public", "pub.fns.test"); err != nil {
		t.Fatal(err)
	}
	fpZero := e.functionsPublicFingerprint(ctx)
	if err := e.repos.Functions.SetCapacity(ctx, fnID, 2); err != nil {
		t.Fatal(err)
	}
	fpOne := e.functionsPublicFingerprint(ctx)
	if fpZero == fpOne {
		t.Fatal("public fingerprint must change with replicas — park/wake would be swallowed by the publish-signature short-circuit (design v3 section 6 mandatory item 1)")
	}
}

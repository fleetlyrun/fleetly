package engine

// B2e 循环表三条目的引擎侧钉板（ADR-0096 决策 1 验收锚「循环表三条目 10→12
// 登记 + loop failure 原样执法断言」；design §4.3 明示漂移清单下的稳态等价）：
//
//	①循环名序：database 保留名、pubsub/bucketstore 紧随其后（表序 = DriveOnce
//	  序，schedule 先于 task 既约不动）——health 面循环名集的稳定契约；
//	②占位循环（descriptor 未登记）驱动零载体写、零 dead 行——机制先行占位
//	  对稳态观测面不可见；
//	③类型循环 panic 只死本类型循环（panic → 有界重启 → 预算耗尽 loop failure
//	  教义原样执法，故障域隔离：database 循环与其余环照常存活）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataServiceRingsRegisteredInOrder(t *testing.T) {
	e, _, _ := newTestEngine(t)
	assert.Equal(t, []string{
		"deployment", "build", "managed",
		"database", "pubsub", "bucketstore",
		"backup", "logging", "metrics", "schedule", "task", "browse",
		"drift",
	}, e.ringNames(), "ring table holds the three data-service kind rings (10→12); drift stays last")
}

func TestPlaceholderKindRingsAreIdle(t *testing.T) {
	e, rt, _ := newTestEngine(t)
	ctx := context.Background()
	before := len(rt.calls())
	driven := 0
	for _, r := range e.rings {
		if r.name == "pubsub" || r.name == "bucketstore" {
			r.step(ctx)
			driven++
		}
	}
	require.Equal(t, 2, driven, "fixture: both placeholder rings must be registered")
	assert.Len(t, rt.calls(), before, "unregistered kinds must not reach the Runtime (descriptor-absent no-op)")
	assert.Empty(t, e.DeadRings())
	for _, row := range e.HealthComponents() {
		assert.NotContains(t, row.Details, " dead", "placeholder rings leave the health face untouched")
	}
}

func TestKindRingPanicStaysInItsOwnRing(t *testing.T) {
	e, _, _ := newTestEngine(t)
	e.ringRestart = ringRestartPolicy{maxAttempts: 2, baseDelay: time.Millisecond, maxDelay: time.Millisecond}
	injected := false
	for i := range e.rings {
		if e.rings[i].name == "pubsub" {
			e.rings[i].step = func(context.Context) { panic("injected kind-level panic") }
			injected = true
		}
	}
	require.True(t, injected, "fixture: pubsub ring must be registered")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for len(e.DeadRings()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, []string{"pubsub"}, e.DeadRings(),
		"a kind-level panic that exhausts its restart budget kills exactly its kind ring: database and the rest keep converging (fault-domain isolation)")
	var dead []string
	for _, row := range e.HealthComponents() {
		if !row.Healthy && row.Name != ConvergenceHealthComponent {
			dead = append(dead, row.Details)
		}
	}
	assert.Equal(t, []string{"ring=pubsub dead"}, dead, "the dead kind ring is health-visible under its own ring name")
}

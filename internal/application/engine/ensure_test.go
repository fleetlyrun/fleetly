// ensure_test.go 承载批 1 确保缓存句柄化的验收锚（设计稿 §3.5）：
//
//   - Fresh 是三条件合取（在册 ∧ 签名未变 ∧ 未到强制重放节拍）——签名或
//     节拍任一失配即不 fresh，这是收敛域短路的机械语义；
//   - 表独立（两柄两张表，影子表结构性不可能）+ 全柄共享引擎单把 ensureMu
//     （白盒指针断言）+ -race 并发锤；
//   - AppliedCache 三动词契约（热应用摘要的记账/失配判据/强制清理）。

package engine

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
)

func TestEnsureCacheHandle(t *testing.T) {
	e, _, _ := newTestEngine(t)
	t0 := time.Now()

	t.Run("fresh is a three-way conjunction", func(t *testing.T) {
		c := e.NewEnsureCache()
		if _, ok := c.Fresh("k", "s1", t0); ok {
			t.Fatal("an empty table is never fresh")
		}
		c.Remember("k", contract.Memo{Sig: "s1", Gen: 7, At: t0})
		if _, ok := c.Fresh("k", "s2", t0.Add(time.Second)); ok {
			t.Fatal("a signature change must break the short circuit")
		}
		m, ok := c.Fresh("k", "s1", t0.Add(time.Second))
		if !ok || m.Sig != "s1" || m.Gen != 7 {
			t.Fatalf("within the replay window the full memo must come back: ok=%v memo=%+v", ok, m)
		}
		if _, ok := c.Fresh("k", "s1", t0.Add(61*time.Second)); ok {
			t.Fatal("past the forced-replay beat (default 60s) the entry must go stale")
		}
		c.Forget("k")
		if _, ok := c.Fresh("k", "s1", t0); ok {
			t.Fatal("forget drops the entry")
		}
		c.Forget("absent") // 缺失键清删是 no-op，不得 panic
	})

	t.Run("two handles hold two independent tables", func(t *testing.T) {
		a, b := e.NewEnsureCache(), e.NewEnsureCache()
		a.Remember("k", contract.Memo{Sig: "sa", At: t0})
		if _, ok := b.Fresh("k", "sa", t0); ok {
			t.Fatal("a second handle must not see the first handle's table (no shadow table)")
		}
		b.Remember("k", contract.Memo{Sig: "sb", At: t0})
		if m, _ := a.Fresh("k", "sa", t0); m.Sig != "sa" {
			t.Fatal("writes through one handle must not leak into the other")
		}
	})

	t.Run("digest cache contract", func(t *testing.T) {
		a := e.NewAppliedCache()
		if _, ok := a.DigestOf("k"); ok {
			t.Fatal("an empty table reports no digest")
		}
		a.Remember("k", "d1")
		if d, ok := a.DigestOf("k"); !ok || d != "d1" {
			t.Fatalf("digest roundtrip broken: %q ok=%v", d, ok)
		}
		a.Forget("k")
		if _, ok := a.DigestOf("k"); ok {
			t.Fatal("forget drops the digest")
		}
	})

	t.Run("all handles share one mutex and survive a race hammer", func(t *testing.T) {
		c1, c2, ap := e.NewEnsureCache(), e.NewEnsureCache(), e.NewAppliedCache()
		// 单锁拓扑：所有句柄的互斥量都是引擎 ensureMu 的同一指针（白盒）。
		shared := c1.(*ensureCache).mu
		if c2.(*ensureCache).mu != shared || ap.(*appliedCache).mu != shared {
			t.Fatal("every handle must share the engine's single ensure mutex")
		}
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := fmt.Sprintf("k%d", i%4)
				for j := 0; j < 200; j++ {
					c1.Fresh(key, "s", t0)
					c1.Remember(key, contract.Memo{Sig: "s", Gen: uint64(j), At: t0})
					c1.Forget(key)
					c2.Fresh(key, "s", t0)
					c2.Remember(key, contract.Memo{Sig: "s", At: t0})
					ap.Remember(key, "d")
					ap.DigestOf(key)
					ap.Forget(key)
				}
			}(i)
		}
		wg.Wait()
	})
}

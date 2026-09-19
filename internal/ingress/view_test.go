package ingress

// E2 验收测试（S19）：markServed 记录 snapshot 返回的 revision——
// 「本次实际下发载荷」的 revision，而非「当下」revision。快照之后的新
// 变更被误记为已服务 → 挑战收敛门 awaitServed 虚假通过 → Present 放行
// CA 校验时载荷尚未含挑战路由。

import (
	"fmt"
	"sync"
	"testing"
)

// TestMarkServedRecordsSnapshotRevision 确定性回归（旧实现直接取当下
// revision，此处必挂）：快照后注入新挑战，markServed 仍记快照时的 rev。
func TestMarkServedRecordsSnapshotRevision(t *testing.T) {
	v := newView("")
	v.setRoutes([]Route{{App: "demo", Service: "web", Port: "80", Domains: []string{"d.test"}}})
	r1 := v.addChallenge("tok1", "ka1")
	cfg, rev := v.snapshot()
	if rev < r1 {
		t.Fatalf("snapshot rev = %d < challenge rev %d (payload contains the challenge)", rev, r1)
	}
	if cfg.HTTP.Routers[acmeChallengeRouterName] == nil {
		t.Fatal("challenge router missing from snapshot payload")
	}
	// 快照之后的新变更：markServed 必须仍记快照时的 rev（旧实现取当下
	// revision 会把 tok2 误记为已服务——本次载荷实际不含它）。
	r2 := v.addChallenge("tok2", "ka2")
	if r2 <= rev {
		t.Fatalf("sanity: second challenge rev %d must exceed snapshot rev %d", r2, rev)
	}
	v.markServed(rev)
	if got := v.servedRevision.Load(); got != rev {
		t.Fatalf("servedRevision = %d, want snapshot revision %d (not the newer %d)", got, rev, r2)
	}
	// awaitServed 对 tok2 的 rev 不得立即通过（载荷尚未服务过它）。
	if v.awaitServed(r2, 0) {
		t.Fatal("awaitServed(r2) must not pass: the payload containing tok2 has not been served")
	}
}

// TestSnapshotRevisionUnderConcurrentChallenges -race 交错压测：并发
// addChallenge/removeChallenge 与 snapshot/markServed 交错下的不变量——
// 每个快照 rev ≤ 快照时的 currentRevision（载荷一致性由确定性用例与
// -race 守；rev 只增不减使该断言等价于「rev 取自快照时刻之前的值」）。
func TestSnapshotRevisionUnderConcurrentChallenges(t *testing.T) {
	v := newView("")
	v.setRoutes([]Route{{App: "demo", Service: "web", Port: "80", Domains: []string{"d.test"}}})
	v.markServed(v.currentRevision())

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tok := fmt.Sprintf("tok-%d-%d", g, i)
				v.addChallenge(tok, "ka")
				v.removeChallenge(tok)
			}
		}(g)
	}
	for i := 0; i < 300; i++ {
		_, rev := v.snapshot()
		if cur := v.currentRevision(); rev > cur {
			t.Fatalf("snapshot rev %d exceeds current revision %d", rev, cur)
		}
		v.markServed(rev)
		if got, cur := v.servedRevision.Load(), v.currentRevision(); got > cur {
			t.Fatalf("servedRevision %d exceeds current revision %d", got, cur)
		}
	}
	close(stop)
	wg.Wait()
}

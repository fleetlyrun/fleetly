package guards

// 提案 2026-11-05 C7 静态执法：组合根 repo 构造点圈禁。newStoreSet
//（internal/assembly/provides.go）是聚合 repo 的唯一构造源——五个消费面
//（NewBinder/NewAuthnRepos/NewSeedRepos/NewAnchor/NewAPIDeps 及其余读点）
// 按字段投影，加聚合只改单源一处。[a-z]+repo.New( 形态的构造调用出现在
// assembly 其他生产文件 = 单源被旁路（第二份清单回潮），即红。
//
// 文件级白名单双向保鲜：白名单条目不再命中即红（搬走/改名要同步守卫）；
// 全仓零命中同样致命失败（守卫不可盲——单源整文件消失时扫描必须喊出来，
// 而不是静默变绿）。

import (
	"regexp"
	"strings"
	"testing"
)

// repoCtorAllowlist 是获准承载 repo 构造调用的 assembly 文件（条目带理由）。
var repoCtorAllowlist = map[string]string{
	"internal/assembly/provides.go": "newStoreSet 单源：全部聚合 repo 构造与 anchor 投影住此（提案 2026-11-05 C7）",
}

// repoCtorRe 匹配 persistence 各聚合包的构造调用形态（xxxrepo.New(——
// import 别名统一以 repo 结尾；outbox 等非 repo 后缀别名不在本守卫面）。
var repoCtorRe = regexp.MustCompile(`[a-z]+repo\.New\(`)

// TestAssemblyRepoConstructionSingleSource：assembly 生产文件里 repo 构造
// 只许出现在白名单文件；白名单条目零命中即红（双向保鲜）。
func TestAssemblyRepoConstructionSingleSource(t *testing.T) {
	hits := map[string]int{}
	walkRepoGoFiles(t, func(rel, source string) {
		if !strings.HasPrefix(rel, "internal/assembly/") {
			return
		}
		if n := len(repoCtorRe.FindAllString(source, -1)); n > 0 {
			hits[rel] = n
		}
	})

	for rel := range hits {
		if _, ok := repoCtorAllowlist[rel]; !ok {
			t.Errorf("%s constructs aggregate repos directly — repo construction lives only in the newStoreSet single source (internal/assembly/provides.go); project from the store set or extend the allowlist with a reason",
				rel)
		}
	}
	for rel := range repoCtorAllowlist {
		if hits[rel] == 0 {
			t.Errorf("repo ctor allowlist entry %s no longer constructs repos — the single source moved; update the guard (双向保鲜)", rel)
		}
	}
	if len(hits) == 0 {
		t.Fatal("scan found no repo construction sites under internal/assembly — guard is blind, fix the scan")
	}
}

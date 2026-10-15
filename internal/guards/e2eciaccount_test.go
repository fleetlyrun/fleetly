package guards

// G20 e2e↔CI 对账守卫：mise.toml 的全部 e2e 任务与 .github/workflows/
// ci.yml 的全部 `mise run e2e:*` 调用一一对账——每个 e2e 任务要么被 CI
// 调用、要么登记对账白名单（带中文理由）。双向保鲜：新增任务必登记或进
// CI（漏挂即红）；任务删除/改名后白名单条目悬空、或白名单任务被 CI 收编
// 后条目过期，即红；CI 调用指向不存在的任务（改名漂移）即红。
// 防盲：mise e2e 任务实测 12（dind/h2c/template/browse/twonode/upgrade/
// backup/bluegreen/k3s/k3s-tw/runtimeswitch/k3s-ha）、CI e2e 调用实测 9
// （2026-10-16 P5-3），阈值留余量——解析退化（文件挪走/改格式）在此显形。

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// e2eciWhitelist 是 e2e↔CI 对账白名单（任务名 → 理由，中文一句话）。
// 理由依据各 e2e/dind-*.sh 头部"前置"段如实登记；任务进 CI 或删除即删条目。
var e2eciWhitelist = map[string]string{
	"e2e:template":  "需宿主机预置 traefik:v3.5、traefik/whoami:v1.10 与 nginx:1.27 三镜像（dind-template.sh 头部前置段），CI job 未挂（时间预算），本地常态跑",
	"e2e:browse":    "需宿主机预载 sosedoff/pgweb:0.17.0、postgres:17-bookworm 与 traefik 镜像进 dind（dind-browse.sh 预载腿），CI job 未挂（时间预算），本地常态跑",
	"e2e:bluegreen": "需宿主机预置 traefik:v3.5 与 busybox:1.37（dind-bluegreen.sh 头部前置段），CI job 未挂（时间预算），本地常态跑",
}

// miseTaskRe 匹配 mise.toml 任务头（[tasks."e2e:*"]）。
var miseTaskRe = regexp.MustCompile(`\[tasks\."(e2e:[A-Za-z0-9._-]+)"\]`)

// ciCallRe 匹配 CI 步骤里的 mise e2e 调用（run: mise run e2e:*）。
var ciCallRe = regexp.MustCompile(`mise\s+run\s+(e2e:[A-Za-z0-9._-]+)`)

// parseMiseE2ETasks 从 mise.toml 文本提取全部 e2e 任务名（去重升序）。
func parseMiseE2ETasks(content string) []string {
	var names []string
	for _, m := range miseTaskRe.FindAllStringSubmatch(content, -1) {
		names = append(names, m[1])
	}
	return unique(names)
}

// parseCIE2ECalls 从 CI workflow 文本提取全部 `mise run e2e:*` 调用（去重升序）。
func parseCIE2ECalls(content string) []string {
	var names []string
	for _, m := range ciCallRe.FindAllStringSubmatch(content, -1) {
		names = append(names, m[1])
	}
	return unique(names)
}

// TestE2ECIAccount：mise e2e 任务集 ⊆ CI 调用集 ∪ 白名单，三面互证保鲜。
func TestE2ECIAccount(t *testing.T) {
	mise := parseMiseE2ETasks(readFileLF(t, "mise.toml"))
	ci := parseCIE2ECalls(readFileLF(t, ".github/workflows/ci.yml"))

	// 防盲：实测值之下即解析退化。
	if len(mise) < 8 {
		t.Fatalf("mise.toml scan found only %d e2e tasks (expected >= 8) — guard is blind, fix the scan", len(mise))
	}
	if len(ci) < 5 {
		t.Fatalf("ci.yml scan found only %d e2e invocations (expected >= 5) — guard is blind, fix the scan", len(ci))
	}

	miseSet := map[string]bool{}
	for _, name := range mise {
		miseSet[name] = true
	}
	ciSet := map[string]bool{}
	for _, name := range ci {
		ciSet[name] = true
	}

	// CI 调用必须指向在册任务（任务改名后 workflow 未跟 → 漂移）。
	for _, name := range ci {
		if !miseSet[name] {
			t.Errorf("ci.yml invokes %s but mise.toml has no such e2e task — renamed or removed, fix the workflow", name)
		}
	}

	// 律文：每任务要么在 CI、要么登记白名单（带理由）。
	for _, name := range mise {
		if ciSet[name] {
			continue
		}
		reason, ok := e2eciWhitelist[name]
		if !ok {
			t.Errorf("e2e task %s is neither invoked in ci.yml nor registered in e2eciWhitelist — add a CI job or register an exemption with a reason (G20 e2e↔CI 对账)", name)
			continue
		}
		if reason == "" {
			t.Errorf("e2e↔CI whitelist entry %s must carry a reason", name)
		}
	}

	// 条目保鲜：白名单必须仍指向在册任务，且尚未被 CI 收编。
	for name := range e2eciWhitelist {
		if !miseSet[name] {
			t.Errorf("e2e↔CI whitelist entry %s points at a task that no longer exists in mise.toml — delete or update the entry", name)
		}
		if ciSet[name] {
			t.Errorf("e2e↔CI whitelist entry %s is now invoked in ci.yml; delete the stale entry", name)
		}
	}
}

// TestE2ECIAccountShapes：解析器形状验证（内联文本，注入式）——非 e2e
// 任务不收、调用去重、跨行提取。
func TestE2ECIAccountShapes(t *testing.T) {
	miseToml := `
[tasks."build"]
run = "sh build.sh"
[tasks."e2e:dind"]
run = "sh e2e/dind-smoke.sh"
[tasks."e2e:k3s-ha"]
run = "sh e2e/dind-k3s-ha.sh"
[tasks."generate:all"]
run = "go generate ./..."
`
	got := parseMiseE2ETasks(miseToml)
	if len(got) != 2 || got[0] != "e2e:dind" || got[1] != "e2e:k3s-ha" {
		t.Fatalf("mise parse: want [e2e:dind e2e:k3s-ha], got %v", got)
	}

	ciYml := `
      - name: smoke
        run: mise run e2e:dind
      - name: smoke again
        run: mise run e2e:dind
      - name: ha
        run: |
          mise run e2e:k3s-ha
      - name: unrelated
        run: mise run test
`
	got = parseCIE2ECalls(ciYml)
	if len(got) != 2 || got[0] != "e2e:dind" || got[1] != "e2e:k3s-ha" {
		t.Fatalf("ci parse: want [e2e:dind e2e:k3s-ha], got %v", got)
	}
	if !sort.StringsAreSorted(got) || strings.Contains(strings.Join(got, ","), "test") {
		t.Fatalf("ci parse ordering/prefix drift: %v", got)
	}
}

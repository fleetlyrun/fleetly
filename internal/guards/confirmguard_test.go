package guards

// confirmguard（B1 变更确认闭环同批守卫，设计稿 2026-10-30 §1.2 重裁
// §8.2'；ADR 编号随 W5 补录）：ConfirmableVerbs 表 ↔ proto 双向对账——
//
//	正向：请求体携带 confirmation 字段的 RPC ⊆ ConfirmableVerbs（带票面
//	      而不入强制集 = 立法与契约脱钩，静默缺一块强制）；
//	反向：表内 RPC 的请求必有 confirmation 字段（入集而无通道 = 拦截器
//	      恒拒 REQUIRED 的死立法）；死条目（proto 无此方法）同红；
//	豁免族：PreviewDeploy/PreviewTeardown/ScaleProcess（mint/轻量可逆面）
//	      不得入表——入表即红。
//
// 扫描形态沿 freezeguard/writeclassguard 的行扫描先例（walkRepoFiles）。
// PreviewDeployRequest 经嵌套 DeployRequest 继承形状，但其**直接**字段不
// 含 confirmation——豁免族的形状语义由本守卫钉死（嵌套携带不构成自身的
// 确认通道）。

import (
	"bufio"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/application/governance"
)

// confirmExemptVerbs 是「携带 confirmation 字段但显式不入强制集」的豁免
// 面（FullMethod → 理由）——当前为空：confirmation 字段只出现在三个
// 强制集请求体上。豁免不再命中即红（双向保鲜）。
var confirmExemptVerbs = map[string]string{}

// confirmBannedVerbs 是豁免族黑名单（设计稿 §1.2）：mint 族（铸票面）与
// Restart/Scale 轻量可逆族不得入表。
var confirmBannedVerbs = []string{
	"/fleetly.delivery.v1.DeploymentsService/PreviewDeploy",
	"/fleetly.delivery.v1.DeploymentsService/PreviewTeardown",
	"/fleetly.delivery.v1.DeploymentsService/ScaleProcess",
	"/fleetly.delivery.v1.DeploymentsService/RestartProcess",
}

// messageFields 扫描 proto 全部 message 定义：message 名 → 直接字段名集
// （brace 深度感知——嵌套 message/enum 块内的字段不算父消息的直接字段；
// 嵌套消息以「父.子」记名，顶层请求类型不与嵌套路径混淆）。
func messageFields(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	type frame struct {
		name  string
		depth int // 开块时刻的净深度
	}
	var stack []frame
	depth := 0
	inEnum := 0
	walkRepoLines(t, func(line string) {
		// message 开块（含单行自闭合形态 "message X { ... }"——净 brace
		// 归零时立即出栈，否则死栈吞掉后续全部弹出）。
		if strings.HasPrefix(line, "message ") {
			name := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "message "), "{", 2)[0])
			if len(stack) > 0 {
				name = stack[len(stack)-1].name + "." + name
			}
			stack = append(stack, frame{name: name, depth: depth})
			out[name] = map[string]bool{}
			depth += braceDelta(line)
			for len(stack) > 0 && depth <= stack[len(stack)-1].depth {
				stack = stack[:len(stack)-1]
			}
			return
		}
		if strings.HasPrefix(line, "enum ") || strings.HasPrefix(line, "oneof ") {
			inEnum++
		}
		depth += braceDelta(line)
		// 直接字段行：栈顶 message 的成员、非嵌套块内、形如
		// "[optional|repeated] type name = N;"——字段名 = "=" 前最后一个 token。
		if len(stack) > 0 && inEnum == 0 && depth == stack[len(stack)-1].depth+1 && isProtoField(line) {
			head, _, _ := strings.Cut(line, "=")
			f := strings.Fields(head)
			if len(f) >= 2 {
				out[stack[len(stack)-1].name][f[len(f)-1]] = true
			}
		}
		// enum/oneof 出块。
		if inEnum > 0 && braceDelta(line) < 0 && strings.Contains(line, "}") {
			inEnum--
		}
		// message 出块。
		for len(stack) > 0 && depth <= stack[len(stack)-1].depth {
			stack = stack[:len(stack)-1]
		}
	})
	return out
}

// protoRequestMethod 扫描 rpc 行：FullMethod → 请求 message 类型名。
func protoRequestMethod(t *testing.T) map[string]string {
	t.Helper()
	methodReq := map[string]string{}
	pkg, svc := "", ""
	walkRepoLines(t, func(line string) {
		if v, ok := strings.CutSuffix(line, ";"); ok && strings.HasPrefix(v, "package ") {
			pkg = strings.TrimSpace(strings.TrimPrefix(v, "package "))
			return
		}
		if strings.HasPrefix(line, "service ") {
			svc = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "service "), "{"))
			return
		}
		if strings.HasPrefix(line, "rpc ") && pkg != "" && svc != "" {
			rest := strings.TrimPrefix(line, "rpc ")
			name, tail, _ := strings.Cut(rest, "(")
			reqType, _, ok := strings.Cut(strings.TrimPrefix(tail, "("), ")")
			if !ok {
				return
			}
			methodReq["/"+pkg+"."+svc+"/"+strings.TrimSpace(name)] =
				strings.TrimSpace(strings.TrimPrefix(reqType, "stream "))
		}
	})
	return methodReq
}

// walkRepoLines 是 walkRepoFiles 的逐行回调形态（proto 子树，.proto 后缀）。
func walkRepoLines(t *testing.T, fn func(line string)) {
	t.Helper()
	err := walkRepoFiles(t, "proto", ".proto", func(_ string, scan *bufio.Scanner) {
		for scan.Scan() {
			fn(strings.TrimSpace(scan.Text()))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// braceDelta 统计一行的净 brace 深度变化（开 - 闭）。
func braceDelta(line string) int {
	d := 0
	for _, r := range line {
		switch r {
		case '{':
			d++
		case '}':
			d--
		}
	}
	return d
}

// isProtoField 判定 proto 字段行（"type name = N;" 形态；排除 option/rpc/
// message/enum/oneof/reserved/comment/close 行）。
func isProtoField(line string) bool {
	if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "option ") ||
		strings.HasPrefix(line, "rpc ") || strings.HasPrefix(line, "message ") ||
		strings.HasPrefix(line, "enum ") || strings.HasPrefix(line, "oneof ") ||
		strings.HasPrefix(line, "reserved") || strings.HasPrefix(line, "}") ||
		strings.HasPrefix(line, "syntax") || strings.HasPrefix(line, "import") ||
		strings.HasPrefix(line, "package") || strings.HasPrefix(line, "service") {
		return false
	}
	return strings.Contains(line, "=") && strings.HasSuffix(line, ";")
}

// TestConfirmableVerbsProtoReconciliation：表 ↔ proto 双向对账 + 豁免族
// 黑名单 + 台账实数。
func TestConfirmableVerbsProtoReconciliation(t *testing.T) {
	fields := messageFields(t)
	methodReq := protoRequestMethod(t)
	if len(methodReq) < 150 {
		t.Fatalf("proto rpc scan found only %d methods — guard is blind, fix the scan", len(methodReq))
	}
	bearing := map[string]bool{}
	for method, reqType := range methodReq {
		if f, ok := fields[reqType]; ok && f["confirmation"] {
			bearing[method] = true
		}
	}
	if len(bearing) < 3 {
		t.Fatalf("confirmation-bearing scan found only %d methods — guard is blind, fix the scan", len(bearing))
	}

	all := protoAllMethods(t)
	usedExemptions := map[string]bool{}

	// 正向：带 confirmation 字段的 RPC ⊆ 表（或豁免面带理由）。
	var unlisted []string
	for m := range bearing {
		if _, ok := governance.ConfirmableVerbs[m]; ok {
			continue
		}
		if reason, ok := confirmExemptVerbs[m]; ok && reason != "" {
			usedExemptions[m] = true
			continue
		}
		unlisted = append(unlisted, m)
	}
	sort.Strings(unlisted)
	for _, m := range unlisted {
		t.Errorf("RPC %s carries a confirmation field but is not in governance.ConfirmableVerbs — a confirmation channel outside the mandatory set is legislation-contract drift; add it to the table or exempt with a reason (design 2026-10-30 §1.2)", m)
	}

	// 反向：表内 RPC 的请求必有 confirmation 字段；死条目红。
	var noChannel, dead []string
	for m := range governance.ConfirmableVerbs {
		if !all[m] {
			dead = append(dead, m)
			continue
		}
		if !bearing[m] {
			noChannel = append(noChannel, m)
		}
	}
	sort.Strings(dead)
	for _, m := range dead {
		t.Errorf("governance.ConfirmableVerbs lists %s but no such proto method exists — remove the stale entry", m)
	}
	sort.Strings(noChannel)
	for _, m := range noChannel {
		t.Errorf("governance.ConfirmableVerbs lists %s but its request message carries no confirmation field — a mandatory verb without the ticket channel is dead legislation (every call would fail E_CONFIRMATION_REQUIRED with no legal bypass)", m)
	}

	// 豁免族黑名单：mint/轻量面不得入表。
	for _, m := range confirmBannedVerbs {
		if _, ok := governance.ConfirmableVerbs[m]; ok {
			t.Errorf("governance.ConfirmableVerbs lists %s — the mint family (PreviewDeploy/PreviewTeardown) and the Restart/Scale exemption family must never enter the mandatory set (design 2026-10-30 §1.2)", m)
		}
	}

	// 双向保鲜：豁免不再命中即红。
	for m, reason := range confirmExemptVerbs {
		if reason == "" {
			t.Errorf("confirm exemption %s must carry a reason", m)
		}
		if !usedExemptions[m] {
			t.Errorf("confirm exemption %s no longer covers a real gap (member gained the table, lost the field, or left proto); remove the entry", m)
		}
	}

	// 台账实数：强制集 = Deploy + Rollback + DeleteApp（设计稿 §1.2——
	// delete 族其余动词（Database/Route/Project）的确认立法随各自批次扩表，
	// 扩表同 commit 重编本断言与 ADR 附录）。
	if len(governance.ConfirmableVerbs) != 3 {
		t.Errorf("governance.ConfirmableVerbs holds %d entries; the ledger says 3 (Deploy + Rollback + DeleteApp) — re-transcribe the ledger with the table change", len(governance.ConfirmableVerbs))
	}
}

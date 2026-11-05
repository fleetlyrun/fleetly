package guards

// 提案 2026-11-05 C6：「唯一事务门面」执法。transact.Tx 是写径门面
// （ADR-0080；装配注释自称唯一门面），但门面旁路有两条：直调 db.Tx
// （两处在案：单聚合浅事务）与绕过 wire 直构 transact.NewTx。本守卫把
// 周界钉成法条：
//
//   - 法条一：`.Tx(` 直调限 persistence（定义域）与 transact（门面自身）
//     + 站点白名单（条目带理由；新增即红，退役不再命中即红——双向保鲜）；
//   - 法条二：`transact.NewTx(` 限 internal/assembly（组合根）与
//     internal/testsupport（apitest 夹具与生产 assembly 同源构造）。
//
// 执法用 AST 而非文本扫描：注释/字符串里出现 .Tx( 不算调用（B 侧评审
// 建议——installenv 式完整表达式锚在此不够，调用形态多变）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// txDirectAllowlist 是 db.Tx 直调的获准站点（装配注释「唯一事务门面」的
// 在案例外——条目即债务台账）。
var txDirectAllowlist = map[string]string{
	"internal/assembly/provides.go":       "retention janitor sweepUploads：单聚合浅事务（sourcerepo.Delete），无需 Binder 全套 Stores 形态",
	"internal/interfaces/admin/rewrap.go": "rewrap 批量重封：单聚合浅事务，同上",
}

// newTxAllowedPrefixes 是 transact.NewTx 的合法构造域。
var newTxAllowedPrefixes = []string{
	"internal/assembly/",
	"internal/testsupport/",
}

func TestTransactionFacadePerimeter(t *testing.T) {
	direct := map[string]int{}     // rel → .Tx( 直调次数
	newTxCalls := map[string]int{} // rel → transact.NewTx( 调用次数

	walkRepoGoFiles(t, func(rel, source string) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, source, 0)
		if err != nil {
			// 解析失败不执法（gofmt/vet 门禁另有把关），零命中文件不进面。
			return
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Tx":
				direct[rel]++
			case "NewTx":
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "transact" {
					newTxCalls[rel]++
				}
			}
			return true
		})
	})

	if len(direct) == 0 && len(newTxCalls) == 0 {
		t.Fatal("scan found no transaction call sites — guard is blind, fix the scan")
	}

	// 法条一：直调圈禁。
	for rel, count := range direct {
		if strings.HasPrefix(rel, "internal/infrastructure/persistence/") ||
			strings.HasPrefix(rel, "internal/application/transact/") {
			continue
		}
		if reason, ok := txDirectAllowlist[rel]; ok {
			_ = reason
			continue
		}
		t.Errorf("%s calls db.Tx directly (%d site(s)) — transactions go through the transact.Tx facade (ADR-0080); single-aggregate shallow calls need an allowlist entry with a reason",
			rel, count)
	}
	for rel := range txDirectAllowlist {
		if direct[rel] == 0 {
			t.Errorf("tx allowlist entry %s no longer calls db.Tx — remove the stale entry (双向保鲜)", rel)
		}
	}

	// 法条二：门面构造域。
	for rel := range newTxCalls {
		allowed := false
		for _, p := range newTxAllowedPrefixes {
			if strings.HasPrefix(rel, p) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Errorf("%s constructs transact.NewTx — the transaction facade is wired only in the composition root (internal/assembly) and its apitest twin (internal/testsupport)", rel)
		}
	}
}

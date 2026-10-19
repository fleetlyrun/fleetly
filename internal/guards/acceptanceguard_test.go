package guards

// 受理面反扫（ADR-0024 同批守卫 ①）：api 层受理位 + 统一写原语的机械
// 执法，两个不变式：
//
//  1. proto 的创建型/删除型动词 handler 必须经 Services.commit 写原语
//     （受理检查 → 聚合写 → 事件 → 审计的顺序与回滚只有一个拥有者）；
//     不经原语的写面要么红，要么在豁免表带理由。
//  2. fleetlygrpc 包内事务直调（ADR-0080 形态 `Tx.Do`）只许出现在
//     acceptance.go（原语本体）——手写四件一拍编排（2026-10-01 曾达 29 处）
//     不得回潮。针随 ADR-0080 换形（R12-M1：旧针 `.DB.Tx(` 已零命中）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// acceptanceVerbs 是受理面覆盖的动词集（创建型 + 删除型；幂等守卫只管
// 创建型，本守卫把 Delete 家族一并纳入——删除守卫是受理位的同等公民）。
var acceptanceVerbs = []string{"Create", "Put", "Set", "Deploy", "Submit", "Rollback", "Delete"}

// acceptanceExemptions 是 FullMethod → 理由（不经 commit 原语的写面）。
// 豁免不再命中即红。
var acceptanceExemptions = map[string]string{
	// 部署动词是 engine 写路径：api 只做归一化 + 转交，四件一拍住在
	// engine 的 admission/transit（ADR-0024 两层归口；engine 侧的统一
	// 由 R-8 transit helper 承接，api 侧原语管不住也不该管）。
	"/fleetly.delivery.v1.DeploymentsService/Deploy":   "engine write path (admission transit owns the four-in-one-tx)",
	"/fleetly.delivery.v1.DeploymentsService/Rollback": "engine write path (replays via Submit)",
	// Task 删除是 engine 写路径（ADR-0023 同款"先收口后落账"：载体拆除
	// 先于一切行写；Run 终态化与 tombstone 的四件一拍住在 engine 的
	// transitTask/transitRun）。
	"/fleetly.automation.v1.TasksService/DeleteTask": "engine write path (carrier teardown precedes row writes per ADR-0023)",
}

// fleetlygrpcHandlers 解析 fleetlygrpc 全部非测试 .go，产出方法名 →
// 方法体是否含 commit 调用（AST 级，注释免疫）。
func fleetlygrpcHandlers(t *testing.T) map[string]bool {
	t.Helper()
	handlers := map[string]bool{}
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "interfaces", "grpc", "fleetlygrpc"))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil {
				continue // 只对账 RPC handler（方法形态）
			}
			usesCommit := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "commit" {
						usesCommit = true
					}
				}
				return true
			})
			handlers[fd.Name.Name] = usesCommit
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(handlers) < 15 {
		t.Fatalf("handler scan found only %d methods — guard is blind, fix the scan", len(handlers))
	}
	return handlers
}

// TestAcceptanceWritePathsGoThroughCommit：创建型/删除型 handler × commit
// 原语对账。
func TestAcceptanceWritePathsGoThroughCommit(t *testing.T) {
	want := scanProtoMethods(t, acceptanceVerbs)
	handlers := fleetlygrpcHandlers(t)

	usedExemptions := map[string]bool{}
	var missing []string
	for m := range want {
		method := m[strings.LastIndex(m, "/")+1:]
		if handlers[method] {
			continue
		}
		if reason, ok := acceptanceExemptions[m]; ok && reason != "" {
			usedExemptions[m] = true
			continue
		}
		missing = append(missing, m)
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("create/delete RPC %s does not write through the Services.commit primitive — acceptance checks, ordering, and rollback live in one place (acceptance.go); route the write path through it or exempt with a reason (ADR-0024 guard)", m)
	}

	// 双向保鲜：豁免不再命中即红。
	for m, reason := range acceptanceExemptions {
		if reason == "" {
			t.Errorf("acceptance exemption %s must carry a reason", m)
		}
		if !usedExemptions[m] {
			t.Errorf("acceptance exemption %s no longer matches a real gap; remove the entry", m)
		}
	}
}

// TestNoHandRolledTxChoreography：fleetlygrpc 包内事务直调（ADR-0080 形态
// `Tx.Do`）只许出现在 acceptance.go（原语本体）——手写四件一拍编排不得
// 回潮。先钉"针的真实形态"再反扫：ADR-0080 换形后旧针 `.DB.Tx(` 静默
// 零命中（R12-M1），本锚断言防同名失配再次发生。
func TestNoHandRolledTxChoreography(t *testing.T) {
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "interfaces", "grpc", "fleetlygrpc"))
	acceptancePath := filepath.Join(root, "acceptance.go")
	acceptanceBody, err := os.ReadFile(acceptancePath)
	if err != nil {
		t.Fatal(err)
	}
	// 形态锚：唯一事务入口是服务持有的 Tx.Do（ADR-0080）。若事务 API 再
	// 换形，此断言先红——提醒改针，而不是让守卫悄悄退化成零扫描。
	if !strings.Contains(string(acceptanceBody), "s.Tx.Do(") {
		t.Fatal("guard anchor missing: acceptance.go no longer contains `s.Tx.Do(`; the ADR-0080 transaction-form needle is stale — update the needle before trusting this guard")
	}

	scanned := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path[len(repoRoot(t))+1:])
		if rel == "internal/interfaces/grpc/fleetlygrpc/acceptance.go" {
			return nil // 原语本体：唯一允许的事务直调点
		}
		scanned++
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(content), ".Tx.Do(") {
			t.Errorf("%s hand-rolls the four-in-one-tx choreography via Tx.Do; declare a writeFact and go through Services.commit (ADR-0024)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防盲：扫描面文件数下限（目录/后缀漂移导致的零扫描即红）。
	if scanned < 10 {
		t.Fatalf("scanned only %d files under fleetlygrpc — guard is blind, fix the scan", scanned)
	}
}

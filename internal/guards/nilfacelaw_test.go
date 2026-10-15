package guards

// G17「nil 面错误码律」——执法手册 G17 条目的反推形态。
//
// 渊源：G17 条目名存于 docs/design/2026-10-14-architecture-convergence-report.html，
// 其底稿（原始执法承诺文本）已失效；形态由 P4-3 码律反推——2026-10-16 用户
// 裁定三项：①cipher-nil（未配主密钥）面拆分新码 503 族；②Logging/Metrics
// 两码分立；③provider 可选子面缺失收 501 族；且 E_INTERNAL 在 nil 能力面
// 清零（白名单豁免除外）。
//
// 律文：internal/interfaces 的生产代码里，`if X == nil`（或 `if X != nil`
// 的 else 支）这一 nil 能力面协商分支若以 errcode 收场（分支返回路径含
// apperr.New 字面量调用），映射码必须在「关态/未实现族」白名单内——
// 配置级 off 态走 503 族（absence is an explicit off state，E_REGISTRY_
// UNAVAILABLE 范式），可选子面缺席走 501 族（honest failure，无降级路径，
// E_EXEC_UNSUPPORTED 范式）；落 E_INTERNAL 即红——nil 面缺席是配置事实，
// 不是内部故障，脱敏 500 会把可行动的处置提示（set xxx.addr / 装主密钥）
// 埋进服务端日志。
//
// 识别精度（防误报）：只执法「nil 分支返回 errcode」形态——
//   - nil 支的直接语句序列里的 ReturnStmt（含链式 WithCause/Suggestion）；
//   - 不下钻嵌套 if/循环/函数字面量：嵌套 if 自带 nil 比较条件时由其自身
//     独立受检（嵌套形态照样在射程内），非 nil 比较的嵌套分支（输入校验、
//     词法检查）不是能力面协商点，不在执法面；
//   - 非 apperr 的 nil 防御（continue/构造兜底/return nil）天然不在射程。
//
// 豁免表带理由、双向保鲜（enginebackref 范式）：确属刻意的族外映射须显式
// 豁免（文件 + 锚 snippet + 理由），扫描不再命中即红。

import (
	"fmt"
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

// nilFaceLawCodes 是 nil 面映射的「关态/未实现族」白名单码集（只增）：
// 503 族 off 态（显式关态）+ 501 族未实现（honest failure）+ 既有同族
// （E_SECRET_UNAVAILABLE 留守 seal/open 运算失败语义）。
var nilFaceLawCodes = map[string]bool{
	"E_MASTER_KEY_UNAVAILABLE":    true, // 未配主密钥，cipher 面缺席（503，P4-3 批 3 新码）
	"E_LOG_STORE_UNAVAILABLE":     true, // logging.addr 未配（503，P4-3 批 3 新码）
	"E_METRICS_STORE_UNAVAILABLE": true, // metrics.addr 未配（503，P4-3 批 3 新码）
	"E_RUNTIME_FACE_UNSUPPORTED":  true, // runtime 可选子面/引擎面缺席（501，P4-3 批 3 新码）
	"E_REGISTRY_UNAVAILABLE":      true, // registry 未配/无 content 面（503，P0-7.2 先例）
	"E_BROWSE_DISABLED":           true, // browse.host_suffix 空（off 态先例）
	"E_EXEC_UNSUPPORTED":          true, // exec 子面缺席（501 先例）
	"E_BROWSER_UNSUPPORTED":       true, // 引擎无 browser 映射（501 先例）
	"E_SECRET_UNAVAILABLE":        true, // 收窄后只剩 seal/open 运算失败（密钥在场）
	"E_CATALOG_UNAVAILABLE":       true, // 目录刷新失败（503 族）
}

// nilFaceExemption 是一条豁免：锚 snippet 必须在命中 if 语句的源文本中出现。
type nilFaceExemption struct {
	anchor string // if 语句源文本中必含的锚（取足以定位该映射点的片段）
	reason string // 为何允许族外码收场（评审记录；空理由即红）
}

// nilFaceExemptions 是豁免表（文件 → 豁免条目）。现树两条——均为请求载荷/
// 词法校验形态（E_INVALID_ARGUMENT 是输入错误的对码），不是平台能力面协商：
// 条目不再命中即红（双向保鲜）；后续新出现的族外 nil 面映射必须在此登记
// （带理由）或改用关态/未实现族码。
var nilFaceExemptions = map[string][]nilFaceExemption{
	"internal/interfaces/grpc/fleetlygrpc/uploads.go": {
		{
			// UploadSource 首帧 meta 缺席是 proto 载荷校验（调用方没带元数据
			// 帧），meta 是请求字段而非平台配置面——E_INVALID_ARGUMENT 对码
			// 正确，不属 G17 能力面射程（2026-10-16 P4-3 批 3 评审留存）。
			anchor: "the first frame must carry the upload metadata",
			reason: "request-payload validation: a missing first-frame meta is the caller's input error (E_INVALID_ARGUMENT), not a platform capability face",
		},
	},
	"internal/interfaces/grpc/fleetlygrpc/hooks.go": {
		{
			// validateGitRepo 的 perr==nil 是 url.Parse 成功路径上的词法执法
			//（内嵌访问材料拒绝，ADR-0073 阶段 A 决策 6）——解析成功分支不是
			// 能力面缺席（2026-10-16 P4-3 批 3 评审留存）。
			anchor: "must not embed access material in the URL",
			reason: "lexical validation on the url.Parse success path (embedded credentials rejection), not a face-absence negotiation",
		},
	},
}

// nilFaceHit 是一处 nil 面映射点命中。
type nilFaceHit struct {
	rel    string // 仓库相对路径（斜杠分隔）
	line   int    // if 语句行号
	code   string // 映射的 errcode 字面量（无字面量记 <nonliteral>）
	source string // if 语句源文本（豁免锚对账面）
}

// nilCompare 判定条件表达式是否含 nil 比较；negated 报告该比较的形态
// （false = `X == nil`，nil 路径在 Then；true = `X != nil`，nil 路径在 Else）。
// 复合条件取保守面：&&/|| 任一支含 nil 比较即认定协商点（漏报比误报代价高
// ——律的目标是 nil 面不落 E_INTERNAL）。
func nilCompare(cond ast.Expr) (has bool, negated bool) {
	switch c := cond.(type) {
	case *ast.ParenExpr:
		return nilCompare(c.X)
	case *ast.BinaryExpr:
		isNilCmp := func(x, y ast.Expr) (bool, bool) {
			if isNilIdent(y) {
				return true, false // X == nil
			}
			if isNilIdent(x) {
				return true, false // nil == X
			}
			return false, false
		}
		if c.Op == token.EQL {
			if has, neg := isNilCmp(c.X, c.Y); has {
				return has, neg
			}
		}
		if c.Op == token.NEQ {
			if isNilIdent(c.Y) || isNilIdent(c.X) {
				return true, true // X != nil
			}
		}
		if c.Op == token.LAND || c.Op == token.LOR {
			if has, neg := nilCompare(c.X); has {
				return true, neg
			}
			if has, neg := nilCompare(c.Y); has {
				return true, neg
			}
		}
	}
	return false, false
}

// isNilIdent 报告 e 是否字面 nil。
func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// isApperrNew 报告 call 是否 apperr.New 字面调用。
func isApperrNew(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "New" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "apperr"
}

// collectBranchApperrCodes 收集分支直接语句序列中 ReturnStmt 携带的
// apperr.New 码字面量。不下钻嵌套 if/循环/函数字面量——嵌套 nil 比较若自身
// 是协商点，会在遍历中独立受检；其余嵌套形态不是 nil 面映射点。
func collectBranchApperrCodes(branch ast.Stmt) []string {
	block, ok := branch.(*ast.BlockStmt)
	if !ok {
		return nil
	}
	var codes []string
	for _, stmt := range block.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok {
			continue
		}
		ast.Inspect(ret, func(n ast.Node) bool {
			// 函数字面量是独立词法作用域，不属本分支返回路径。
			if _, ok := n.(*ast.FuncLit); ok {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || !isApperrNew(call) {
				return true
			}
			if len(call.Args) == 0 {
				codes = append(codes, "<nonliteral>")
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				codes = append(codes, "<nonliteral>")
				return true
			}
			codes = append(codes, strings.Trim(lit.Value, `"`))
			return true
		})
	}
	return codes
}

// nilFaceHitsInFile 对单个已解析文件执法 nil 面识别，返回全部映射点命中。
func nilFaceHitsInFile(fset *token.FileSet, file *ast.File, src []byte) []nilFaceHit {
	var hits []nilFaceHit
	ast.Inspect(file, func(n ast.Node) bool {
		ifstmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		has, negated := nilCompare(ifstmt.Cond)
		if !has {
			return true
		}
		var nilPath ast.Stmt = ifstmt.Body
		if negated {
			nilPath = ifstmt.Else // X != nil：nil 路径在 else 支
		}
		if nilPath == nil {
			return true
		}
		codes := collectBranchApperrCodes(nilPath)
		if len(codes) == 0 {
			return true // nil 支不以 errcode 收场（continue/构造/return nil）——不在射程
		}
		start := fset.Position(ifstmt.Pos()).Offset
		end := fset.Position(ifstmt.End()).Offset
		source := ""
		if start >= 0 && end <= len(src) && start < end {
			source = string(src[start:end])
		}
		for _, code := range codes {
			hits = append(hits, nilFaceHit{
				rel:    "", // 由调用方填
				line:   fset.Position(ifstmt.Pos()).Line,
				code:   code,
				source: source,
			})
		}
		return true
	})
	return hits
}

// scanNilFaceMappingPoints 扫描 internal/interfaces 全部非测试生产 .go
// （生成物不在面——信封形态由生成模板决定，手改即错的纪律不靠本守卫），
// 返回全部 nil 面映射点命中。
func scanNilFaceMappingPoints(t *testing.T) ([]nilFaceHit, int) {
	t.Helper()
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "interfaces"))
	var hits []nilFaceHit
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path[len(repoRoot(t))+1:])
		if generatedFile(rel) {
			return nil
		}
		src, readErr := os.ReadFile(path) //nolint:gosec // 守卫扫描仓库自有源文件
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return parseErr
		}
		files++
		for _, h := range nilFaceHitsInFile(fset, file, src) {
			h.rel = rel
			hits = append(hits, h)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("scan found no Go files under internal/interfaces — guard is blind, fix the scan")
	}
	return hits, files
}

// TestNilFaceErrcodeLaw：internal/interfaces 的 nil 能力面协商分支若以
// errcode 收场，映射码必须在关态/未实现族白名单内——G17 反推形态
// （渊源见文件头注）。
func TestNilFaceErrcodeLaw(t *testing.T) {
	hits, files := scanNilFaceMappingPoints(t)

	// 防盲自检：interfaces 生产文件数实测 35（2026-10-16），阈值 28 留余量；
	// 映射点实测 13（P4-3 批 3 重接线 11 点 + 豁免在案 2 点），阈值 8——
	// 红时先重测再调值。
	if files < 28 {
		t.Fatalf("interfaces scan found only %d production Go files — guard is blind, fix the scan", files)
	}
	if len(hits) < 8 {
		t.Fatalf("scan found only %d nil-face mapping points — guard is blind, fix the scan", len(hits))
	}

	usedExemptions := map[string]map[int]bool{}
	var bad []string
	for _, h := range hits {
		if nilFaceLawCodes[h.code] {
			continue
		}
		exempted := false
		for i, ex := range nilFaceExemptions[h.rel] {
			if ex.anchor == "" || !strings.Contains(h.source, ex.anchor) {
				continue
			}
			if usedExemptions[h.rel] == nil {
				usedExemptions[h.rel] = map[int]bool{}
			}
			usedExemptions[h.rel][i] = true
			exempted = true
			break
		}
		if exempted {
			continue
		}
		bad = append(bad, fmt.Sprintf(
			"%s:%d nil-face branch maps to %s — a nil capability face is a config fact, not an internal fault; "+
				"map it to the off-state/unimplemented family (503: E_REGISTRY_UNAVAILABLE precedents / "+
				"501: E_EXEC_UNSUPPORTED precedents, see nilFaceLawCodes) instead of masking the actionable "+
				"hint behind a redacted E_INTERNAL (G17 nil-face errcode law, P4-3 batch 3)",
			h.rel, h.line, h.code))
	}
	sort.Strings(bad)
	for _, v := range bad {
		t.Errorf("%s", v)
	}

	// 双向保鲜：豁免条目必须仍命中真实映射点，且理由非空。
	for rel, entries := range nilFaceExemptions {
		for i, ex := range entries {
			if ex.reason == "" {
				t.Errorf("nil-face errcode exemption %s#%d must carry a reason", rel, i)
			}
			if !usedExemptions[rel][i] {
				t.Errorf("nil-face errcode exemption %s (#%d, anchor %q) no longer matches a real out-of-family mapping; remove the entry", rel, i, ex.anchor)
			}
		}
	}
}

// TestNilFaceErrcodeLawShapes 是识别精度的用例面（嵌套 if、if/else 两支、
// return 三形态、非 apperr 防御不误报）——识别器对内联夹具逐例断言。
func TestNilFaceErrcodeLawShapes(t *testing.T) {
	parse := func(t *testing.T, src string) []nilFaceHit {
		t.Helper()
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		return nilFaceHitsInFile(fset, file, []byte(src))
	}
	codesOf := func(hits []nilFaceHit) []string {
		var out []string
		for _, h := range hits {
			out = append(out, h.code)
		}
		return out
	}

	t.Run("return 三形态全部命中", func(t *testing.T) {
		hits := parse(t, `package p
import "github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
func a() error {
	var x *int
	if x == nil {
		return apperr.New("E_INTERNAL", "one")
	}
	return nil
}
func b() (int, error) {
	var x *int
	if x == nil {
		return 0, apperr.New("E_INTERNAL", "two")
	}
	return 0, nil
}
func c() (int, error) {
	var x *int
	if x == nil {
		return 0, apperr.New("E_INTERNAL", "three").WithCause(nil)
	}
	return 0, nil
}`)
		got := codesOf(hits)
		if len(hits) != 3 {
			t.Fatalf("want 3 hits, got %d (%v)", len(hits), got)
		}
		for _, c := range got {
			if c != "E_INTERNAL" {
				t.Fatalf("unexpected code %q", c)
			}
		}
	})

	t.Run("if 不等 nil 的 else 支是 nil 路径", func(t *testing.T) {
		hits := parse(t, `package p
import "github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
func a() error {
	var x *int
	if x != nil {
		_ = x
	} else {
		return apperr.New("E_INTERNAL", "else branch is the nil path")
	}
	return nil
}`)
		if len(hits) != 1 {
			t.Fatalf("want 1 hit from the else branch, got %d", len(hits))
		}
	})

	t.Run("等值条件的 else 支是非 nil 路径不在射程", func(t *testing.T) {
		hits := parse(t, `package p
import "github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
func a() error {
	var x *int
	if x == nil {
		x = &y
	} else {
		return apperr.New("E_INTERNAL", "non-nil path is not a nil-face mapping")
	}
	return nil
}
var y int`)
		if len(hits) != 0 {
			t.Fatalf("the else (non-nil) branch must not be flagged, got %v", codesOf(hits))
		}
	})

	t.Run("嵌套 nil 比较由内层独立受检", func(t *testing.T) {
		hits := parse(t, `package p
import "github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
func a() error {
	var x, y *int
	if x == nil {
		if y == nil {
			return apperr.New("E_INTERNAL", "nested nil comparison is its own negotiation point")
		}
	}
	return nil
}`)
		if len(hits) != 1 {
			t.Fatalf("want exactly 1 hit (the inner nil comparison), got %d (%v)", len(hits), codesOf(hits))
		}
		if hits[0].code != "E_INTERNAL" {
			t.Fatalf("unexpected code %q", hits[0].code)
		}
	})

	t.Run("复合条件与链式收场", func(t *testing.T) {
		hits := parse(t, `package p
import "github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
func a(flag bool) error {
	var x *int
	if flag && x == nil {
		return apperr.New("E_REGISTRY_UNAVAILABLE", "compound condition")
	}
	return nil
}`)
		if len(hits) != 1 || hits[0].code != "E_REGISTRY_UNAVAILABLE" {
			t.Fatalf("compound && with nil comparison must be flagged, got %v", codesOf(hits))
		}
	})

	t.Run("非 apperr 的 nil 防御不在执法面", func(t *testing.T) {
		hits := parse(t, `package p
import "log/slog"
func a(xs []*int, logger *slog.Logger) error {
	for _, x := range xs {
		if x == nil {
			continue
		}
		_ = x
	}
	if logger == nil {
		logger = slog.Default()
	}
	var err error
	if err == nil {
		return nil
	}
	return nil
}`)
		if len(hits) != 0 {
			t.Fatalf("nil defenses without errcode returns must not be flagged, got %v", codesOf(hits))
		}
	})
}

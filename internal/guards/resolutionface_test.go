package guards

// 项目解析器归属契约测试（ADR-0088 程序 P4 wave-0，2026-11-01）。
//
// 渊源：设计稿 2026-10-30 §4 正交性审计——模块间 9 条误置边中 7 条的病根
// 是「域事实帮手随首个消费者文件落户」（team/网络名/控制面节点/卷登记/
// 凭证解析五族曾散落 backup.go/database.go）。wave-0 把五族逐字收拢进
// 解析器单源文件（resolve.go，词条见 CONTEXT.md「项目解析器」）；本契约测试
// 把收拢结果钉为**定义位置锚**（errcode Source 锚同款：按「接收者.方法 ×
// 文件归属」对账，位置漂移即红）。
//
// 律文（双向保鲜）：
//   - 锚表内帮手必须存在——搬走（定义漂移到别的文件）或改名（键消失）即红；
//   - 解析器文件（resolve.go）必须还在扫描面内——文件消失/改名即 fatal
//     （防盲：契约测试不得静默变哑）；
//   - 新解析器帮手入面必须同批扩锚表（扩面走评审，模块间边 AST 契约测试
//     （P4 wave-3）执法「新消费者只准经本面/核」）。
//
// 键形态：接收者限定方法键（Engine.databasePassword）与包级函数裸名键
//（DBCredentialSecretName——wave-3b 铸名族入面起锚面包级导出成员；
// interfaces 层直调要求包级形态，锚同权钉位）。
//
// 正向锚：①锚表全量真命中下限（零命中即 fatal）；②shapes 子测试注入
// 合成树——帮手落错文件/键消失两态必须红、全量落位必须绿（判据有分辨
// 力的活体证明，registrysourceanchor shapes 范式）。

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

// resolutionFaceFile 是解析器单源文件的包内基名（wave-0 收拢落点）。
const resolutionFaceFile = "resolve.go"

// resolutionFaceAnchors 是解析器帮手归属：接收者限定方法键 → 期望定义
// 文件基名。帮手搬走/改名即红；接收者形态变更（wave-1 应用模块化改挂应用模块
// struct）同样红——那是显式改判，须同批更新锚表并重新裁决归属。
var resolutionFaceAnchors = map[string]string{
	// team 归属族。
	"Engine.projectTeamOf": resolutionFaceFile,
	// 项目网络名族（+名列表壳）。
	"Engine.projectNetworkFacts":          resolutionFaceFile,
	"Engine.projectNetworkFactsNamesOnly": resolutionFaceFile,
	// 控制面节点族。
	"Engine.controlPlaneNode": resolutionFaceFile,
	// 卷登记族。
	"Engine.ensureDatabaseVolume": resolutionFaceFile,
	// 凭证解析族。
	"Engine.databasePassword": resolutionFaceFile,
	// 数据库铸名族（P4 wave-3b 自 database.go 入面——凭证链头段 + 寻址
	// 铸名；包级导出形态：interfaces 层 API 创建/投影直调，模块间豁免表
	// 两条 DatabaseDNSName 边随迁清偿）。
	"DBCredentialSecretName": resolutionFaceFile,
	"DatabaseDNSName":        resolutionFaceFile,
	// registry 端点族（P4 wave-2b 入面——wave-0 注记兑现：族员曾暂驻
	// buildsource/materials/registrycontent，intake 应用模块化后应用模块侧消费
	// 经 resolver 端口注入，本面是唯一真源；managedEndpoint 是类型内唯一带
	// 独立硬界的成员，B15-2 语义随迁）。
	"Engine.registryEndpoint":         resolutionFaceFile,
	"Engine.registryProjectEndpoints": resolutionFaceFile,
	"Engine.registryEndpointFor":      resolutionFaceFile,
	"Engine.managedEndpoint":          resolutionFaceFile,
}

// resolutionFaceScan 是一次 engine 包方法归属扫描的产出。
type resolutionFaceScan struct {
	homes   map[string]string // 接收者.方法 → 定义文件基名
	files   int               // 扫描文件数（防盲下限）
	sawFace bool              // 解析器文件是否在扫描面内（消失即 fatal）
}

// scanEngineMethodHomes 解析 root 下全部非测试 .go（跳过 testdata/），产出
// 方法归属表。root 注入（真实包 / shapes 合成树同权）。
func scanEngineMethodHomes(root string) (resolutionFaceScan, error) {
	fset := token.NewFileSet()
	scan := resolutionFaceScan{homes: map[string]string{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		scan.files++
		if filepath.Base(path) == resolutionFaceFile {
			scan.sawFace = true
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// 方法键带接收者限定（Engine.x）；包级函数键=裸名（engineFuncKey
			// 同款口径）——铸名族是包级导出成员，与方法同权钉位。
			scan.homes[engineFuncKey(fd)] = filepath.Base(path)
		}
		return nil
	})
	return scan, err
}

// resolutionFaceViolations 是归属判据（纯函数，执法与 shapes 共用）：
// 返回锚表的对账问题清单（确定性排序）。
func resolutionFaceViolations(scan resolutionFaceScan) []string {
	var bad []string
	hits := 0
	for key, want := range resolutionFaceAnchors {
		got, ok := scan.homes[key]
		if !ok {
			bad = append(bad, "resolution-face helper "+key+" is not defined anywhere in the package — renamed or removed; re-anchor it in the same commit (ADR-0088 P4 wave-0)")
			continue
		}
		hits++
		if got != want {
			bad = append(bad, "resolution-face helper "+key+" is defined in "+got+", want "+want+" — a domain-facts helper must not drift back into its first consumer's file; move it into the resolution face (ADR-0088 P4 wave-0)")
		}
	}
	// 正向锚①：锚表零真命中 = 契约测试盲（扫描器失明或锚表整体悬空）。
	if hits == 0 {
		bad = append(bad, "no resolution-face anchor matched a real definition — the guard is blind, fix the scan or the anchor table")
	}
	sort.Strings(bad)
	return bad
}

// TestResolutionFaceHelpersLiveInResolutionFaceFile：解析器五族帮手的定义
// 位置钉（反扫真实 engine 包）。
func TestResolutionFaceHelpersLiveInResolutionFaceFile(t *testing.T) {
	scan, err := scanEngineMethodHomes(filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine")))
	if err != nil {
		t.Fatal(err)
	}
	// 防盲：扫描面文件数下限（目录漂移零扫描即红）+ 解析器文件必须在场
	//（消失/改名即 fatal——契约测试不得静默变哑）。
	if scan.files < 40 {
		t.Fatalf("scanned only %d engine files — guard is blind, fix the scan", scan.files)
	}
	if !scan.sawFace {
		t.Fatalf("resolution face file %s is missing from the engine package — the face must not be renamed or deleted without re-anchoring (ADR-0088 P4 wave-0)", resolutionFaceFile)
	}
	if len(scan.homes) < 200 {
		t.Fatalf("scanned only %d engine methods — guard is blind, fix the scan", len(scan.homes))
	}
	for _, v := range resolutionFaceViolations(scan) {
		t.Errorf("%s", v)
	}
}

// TestResolutionFaceAnchorShapes：归属判据的用例面（注入合成树）——
// 全量落位=绿 / 帮手落错文件=红 / 键消失（改名）=红 / 包级铸名函数落错
// 文件=红（wave-3b 裸名键腿）/ 解析器文件消失=防盲 fatal 腿（sawFace=false
// 由主测试消费）。
func TestResolutionFaceAnchorShapes(t *testing.T) {
	// synthTree 建一次性合成树（files 相对路径 → 源文本），返回树根。
	synthTree := func(files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, src := range files {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	faceSrc := `package engine

type Engine struct{ repos int }

func (e *Engine) projectTeamOf(p string) string { return "" }
func (e *Engine) projectNetworkFacts(c int, p string) ([]string, map[string]bool) { return nil, nil }
func (e *Engine) projectNetworkFactsNamesOnly(c int, p string) []string { return nil }
func (e *Engine) controlPlaneNode(c int) (string, error) { return "", nil }
func (e *Engine) ensureDatabaseVolume(c int, r int) error { return nil }
func (e *Engine) databasePassword(c int, r int) (string, error) { return "", nil }
func (e *Engine) registryEndpoint(c int) (int, error) { return 0, nil }
func (e *Engine) registryProjectEndpoints() int { return 0 }
func (e *Engine) registryEndpointFor(c int, p string) (int, error) { return 0, nil }
func (e *Engine) managedEndpoint(c int) (int, error) { return 0, nil }

func DBCredentialSecretName(n string) string { return "" }
func DatabaseDNSName(id string) string { return "" }
`

	t.Run("全量落位=绿", func(t *testing.T) {
		root := synthTree(map[string]string{"engine/" + resolutionFaceFile: faceSrc})
		scan, err := scanEngineMethodHomes(root)
		if err != nil {
			t.Fatal(err)
		}
		if !scan.sawFace {
			t.Fatalf("face file must be visible to the scan")
		}
		if v := resolutionFaceViolations(scan); len(v) != 0 {
			t.Fatalf("want no violations, got %v", v)
		}
	})

	t.Run("帮手落错文件=红", func(t *testing.T) {
		// projectTeamOf 漂回消费者文件 backup.go——必须红且只红这一条。
		root := synthTree(map[string]string{
			"engine/" + resolutionFaceFile: strings.Replace(faceSrc, "func (e *Engine) projectTeamOf(p string) string { return \"\" }\n", "", 1),
			"engine/backup.go":             "package engine\n\nfunc (e *Engine) projectTeamOf(p string) string { return \"\" }\n",
		})
		scan, err := scanEngineMethodHomes(root)
		if err != nil {
			t.Fatal(err)
		}
		v := resolutionFaceViolations(scan)
		if len(v) != 1 || !strings.Contains(v[0], "Engine.projectTeamOf") || !strings.Contains(v[0], "backup.go") {
			t.Fatalf("want exactly the misplaced-helper violation for Engine.projectTeamOf, got %v", v)
		}
	})

	t.Run("键消失(改名)=红", func(t *testing.T) {
		root := synthTree(map[string]string{
			"engine/" + resolutionFaceFile: strings.Replace(faceSrc, "projectTeamOf", "teamOfProject", -1),
		})
		scan, err := scanEngineMethodHomes(root)
		if err != nil {
			t.Fatal(err)
		}
		v := resolutionFaceViolations(scan)
		if len(v) != 1 || !strings.Contains(v[0], "Engine.projectTeamOf") {
			t.Fatalf("want exactly the missing-helper violation, got %v", v)
		}
	})

	t.Run("包级铸名函数落错文件=红", func(t *testing.T) {
		// DBCredentialSecretName 漂回首个消费者域文件 database.go——裸名键
		// 的落错腿必须红且只红这一条（wave-3b 铸名族入面的锚腿活体证明）。
		root := synthTree(map[string]string{
			"engine/" + resolutionFaceFile: strings.Replace(faceSrc, "func DBCredentialSecretName(n string) string { return \"\" }\n", "", 1),
			"engine/database.go":           "package engine\n\nfunc DBCredentialSecretName(n string) string { return \"\" }\n",
		})
		scan, err := scanEngineMethodHomes(root)
		if err != nil {
			t.Fatal(err)
		}
		v := resolutionFaceViolations(scan)
		if len(v) != 1 || !strings.Contains(v[0], "DBCredentialSecretName") || !strings.Contains(v[0], "database.go") {
			t.Fatalf("want exactly the misplaced package-level mint helper violation, got %v", v)
		}
	})

	t.Run("解析面文件消失=防盲腿", func(t *testing.T) {
		root := synthTree(map[string]string{
			"engine/backup.go": "package engine\n\nfunc (e *Engine) projectTeamOf(p string) string { return \"\" }\n",
		})
		scan, err := scanEngineMethodHomes(root)
		if err != nil {
			t.Fatal(err)
		}
		if scan.sawFace {
			t.Fatalf("face file must be absent in this shape")
		}
		// 主测试对 sawFace=false 直接 fatal——此处钉判据腿本身。
	})
}

package guards

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shellguard：生产代码进程执行面红线（P2 注入守卫，docs/design/
// 2026-10-03-optimization-proposals.md）。fleetly 的子进程一律 args 数组
// 形态（gitCloneArgs / railpack / doctor 探针），永不 shell 拼串——
// dokploy "bash 字符串当编排 IR" 的结构性反面（docs/research/
// 2026-10-03-competitor-architecture-deep-dive.md §3.4）。
//
// 红线两条，命中即红：
//   - exec.Command/CommandContext 的命令名（字面量）是 shell 解释器
//     （sh/bash/dash/zsh/cmd/powershell 系）；
//   - argv 中出现字面量 "-c"（解释器执行串标志位；`git -C`、
//     `go build -C` 等大写形态不受影响）。
//
// 非字面量命令名（变量传递）不在本守卫射程——gosec G204 已在 lint 门禁
// 扫变量子进程面，二者互补。豁免条目登记在 shellguardWhitelist，每条带
// 理由注释，条目不再命中即红（白名单双向保鲜惯例）。
//
// 挂账实录（F2.2，ADR-0039 决策 11）：dbtemplate 备份/恢复渲染落地形态
// = 纯 argv 数组 + 结构化材料文件（INI/JSON/pgpass），零 shell、零平台
// 生成 SQL（mysqldump 产物是数据不是平台生成 SQL；库/用户初始化走镜像
// env 面）——原文预期的"生成的 SQL"面不存在。P2 的解析器级注入断言以
// 渲染面注入家族落地（dbtemplate TestInjectionBackupRenders：敌意密码
// 三面全拒 + 敌意 host argv 形状不变性 + mysql INI 解析器恰一节断言 +
// mongo config JSON 往返 + redis conf 行级钉）。
var shellguardWhitelist = map[string]string{
	// 当前零豁免：args 数组形态是全仓唯一实践。
}

var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true,
	"cmd": true, "cmd.exe": true, "powershell": true, "powershell.exe": true,
	"pwsh": true, "pwsh.exe": true,
}

// stringLit 返回字符串字面量的去引号值（非字符串字面量返回 ok=false）。
func stringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	v := bl.Value
	if len(v) >= 2 {
		v = v[1 : len(v)-1]
	}
	return v, true
}

func TestShellGuardNoShellInvocation(t *testing.T) {
	root := repoRoot(t)
	hit := false
	for _, dir := range []string{"internal", "cmd"} {
		filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if _, exempt := shellguardWhitelist[rel]; exempt {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", rel, perr)
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
					return true
				}
				// 限定 os/exec 的调用（X 侧标识符名为 exec 的近似——仓内
				// 无同名包冲突，importguard 另行把守 import 面）。
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "exec" {
					return true
				}
				// CommandContext 的首参是 ctx；Command 从零参起即 argv。
				argv := call.Args
				if sel.Sel.Name == "CommandContext" && len(argv) > 0 {
					argv = argv[1:]
				}
				for i, a := range argv {
					v, ok := stringLit(a)
					if !ok {
						continue
					}
					if i == 0 && shellInterpreters[v] {
						t.Errorf("%s: shell interpreter %q invoked via exec — args-array only (whitelist with rationale if truly needed)", rel, v)
						hit = true
					}
					if v == "-c" {
						t.Errorf("%s: bare -c in argv (shell command string) — args-array only", rel)
						hit = true
					}
				}
				return true
			})
			return nil
		})
	}
	// 白名单双向保鲜：条目指向的文件必须存在（条目失效即红）。
	for path := range shellguardWhitelist {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Errorf("shellguard whitelist entry %s no longer exists", path)
		}
	}
	if hit {
		t.FailNow()
	}
}

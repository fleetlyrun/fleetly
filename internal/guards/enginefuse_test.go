package guards

// TestEngineCoreFuseBaseline：内核熔断线（ADR-0088 后果段/验收锚「P4 收官」
// ——基线于 P4 收官冻结〔2026-11-01，commit d4ad5f71 时点〕，P5 只加不减）。
//
// 判据：engine 包（internal/application/engine）全部非测试 .go 的 FuncDecl
// 计数（方法+包级函数，含应用模块 struct 方法——包级计数封死「挂已嵌入 struct
// 新方法」与「新 struct 自立」两条绕道，接收者字面计数在嵌入改形后失效）
// 超基线即红。基线值 579；上涨须 ADR 显式修订本契约测试并带理由（内核膨胀
// 20% 熔断语义的机械承载面；新域动词应落应用模块/新域——生长律，ADR-0088
// 决策 5）。
//
// 防盲：0 文件即 fatal；文件数下限 60（P4 收官 66 文件口径余量）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// engineCoreFuseBaseline 是 P4 收官冻结、随拆包波次允许降向重锚的
// FuncDecl 基线：579（P4 收官）→ 571（wave-1 logpipes）→ 518（wave-2
// execs/metrics）→ 465（wave-3 backups/browses）→ 440（wave-4 databases/
// manageds）→ 383（wave-5 末模块 intake：迁出 ~52 函数〔六文件全量 + 终态
// 副作用随应用模块 + build 铸名/载荷下沉 engine/contract〕，核净增门面 6 + 声明
// 委托 1 + 哨兵别名 2 var + events 别名对，见 golden）→ 381（wave-6
// diagnosis 出包：diagnose.go 四 FuncDecl 迁出、核净增门面 2——九个应用模块
// 全出后的纯内核口径）→ 383（ADR-0097 卷附件边界解析：volumes.go 两
// 内核原语〔resolveVolumeRefs/GuardVolumeCarriers〕入核——解析器/契约测试
// 面（resolutionface 同类），非域动词；应用模块经 Deps 端口消费）→ 384
// （ADR-0097 B1-2 补受理前置读门面 CheckVolumeOrigin〔B4c 起 kind 分派形〕——通则③
// 门面委托，真源在 databases 应用模块 origin.go）→ 388（B2d，ADR-0095 决策
// 6：dynamic 热应用 ensure cache 端口三签名 appliedRemember/appliedForget/
// appliedDigestOf 留核——与 ensureMemo 族同栖单锁 ensureMu 的锁协议
// 半边，ADR-0088 决策 4 同款裁决；enginefuncregistry 豁免面在案）
// ——只加不减律不因拆包放宽：上涨须 ADR 显式修订本契约测试并带理由（内核
// 膨胀 20% 熔断语义的机械承载面；新域动词应落应用模块/新域——生长律，
// ADR-0088 决策 5）。
const engineCoreFuseBaseline = 409 // B8（ADR-0096 决策 10）：+3 子资源实况探测门面（TopicLiveInspect/StreamLiveInspect/BucketLiveInspect——删除披露行的委托；enginefuncregistry 豁免面同批登记）。前值 406 = B5（ADR-0096）：+9 bucketstore 类型门面（B4b +6 pubsub 先例；397 → 406）

// engineCoreFuseMinFiles 是防盲下限（engine 非测试文件数；实测 66（P4
// 收官）→ 65（wave-1 logpipes 迁出）→ 59（wave-2 execs/metrics 迁出）
// → 55（wave-3 backups/browses 迁出 4 文件）→ 51（wave-4 databases/
// manageds 迁出 4 文件）→ 45（wave-5 intake 六文件迁出）→ 44（wave-6
// diagnosis 迁出，纯内核清单），阈值 38 给后续演进留余量——每波拆包
// 同批重测重锚。
const engineCoreFuseMinFiles = 38

func TestEngineCoreFuseBaseline(t *testing.T) {
	fset := token.NewFileSet()
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))
	count, files := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range file.Decls {
			if _, ok := decl.(*ast.FuncDecl); ok {
				count++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < engineCoreFuseMinFiles {
		t.Fatalf("engine package scan found only %d non-test files — guard is blind, fix the scan", files)
	}
	if count > engineCoreFuseBaseline {
		t.Errorf("engine core package holds %d FuncDecls, exceeding the frozen baseline %d (P4 close, 2026-11-01): the core may only grow via ADR amendment — new domain verbs belong in satellites/new domains (growth law, ADR-0088 decision 5)", count, engineCoreFuseBaseline)
	}
}

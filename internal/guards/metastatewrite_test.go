package guards

// 编排层禁直写内核能力私有状态契约测试（ADR-0088 决策 7 第二件 + 验收锚 P4
// 条，P4 wave-3 收官双契约测试之一，2026-11-01）。双表制：
//
//	① 内核能力私有状态字段表 metaStateFields——观测底座（observations 族
//	  六 map）+ 期望台账（expected）+ 确保缓存族（7 张 ensureMemo map 的字段
//	  名全集）+ Run 轨观测缓存（runObs/workloadRun——obs/expect 底座
//	  L0#7 的 Run 轨分家缓存；taskobs 钩子写点在白名单是其在保护面的
//	  活体证明）。与 ensuredledger 的 ledgerFields **对齐复刻**而非单源
//	  引用：两表判据不同（ensuredledger 执法全树的字段×函数白名单，
//	  本契约测试执法编排层的直写形态），字段面以测试内一致性断言互锁
//	  （ledgerFields ⊆ metaStateFields——漂移即红）。
//	② 编排层文件表 metaFileClass——68 文件逐一 内核/编排 归属（设计稿
//	  §2.5 五线 × §2.3 分层 合一）：元 = L0（loop/transition/ensure/
//	  observ/observdomain）+ L1（converge/resolve/lifecycle）+ 组合根
//	  （engine/construct/options/health/events/schemareg）；编排 = §2.5
//	  五线（受理/策略/看门狗/任务/平台，任务族全族随任务线）+ 9 应用模块
//	  23 文件（应用模块是编排住留地——禁直写约束面=应用模块+编排核文件）。
//
// 判据（只执法编排文件；元侧文件=登记函数/协议本体的实现居所，不在面）：
//
//	D1 直写：AssignStmt（任意赋值算符）/IncDecStmt/delete 以内核能力字段
//	  链为 LHS（选择子链任一段命中字段名——`e.obs.x=` / `x.expected[k]=`
//	  / `delete(b.ensure, id)`）即红（ensuredledger 判据核范式，字段 Sel
//	  名即契约）；
//	D2 整构：受保护 struct（observations/expects/tasks/deploys/proxies/
//	  converges/manageds/databases/browses）的复合字面量对受保护字段键
//	  赋值即红（`tasks{runObs: ...}` 形态的私造装配）；
//	D3 影子确保缓存：map[string]ensureMemo 的**新表本体**私造即红——字段声明
//	  编排文件内 var 声明（批 1 句柄化后 memo 表本体未导出，登记制退役）
//	  /make 即红（「第二套确保缓存」禁令，设计稿 §2.6/§2.2#4 勘误——ensure cache
//	  协议此前仅注释裁决，本契约测试补位）。协议端口字段的**签名**携带
//	  map 类型合法（browses 的 ensureFresh/Remember/Forget 函数字段），
//	  执法只看表本体（字段/变量/make）。
//	方法调用 = 合法入口：登记函数 record*/forget*/observationOf 族与
//	ensure cache 协议端口 ensureRemember/Fresh/Forget 不在执法面（调用形态无法
//	绕过锁协议——锁纪律是 D1 的语义根据）。
//
//	白名单 metaWriteExemptions（file:fn → 理由）：上行钩子 1（设计稿
//	§2.4：watch 扇出 Run 轨→task 状态机的 runObs 刷新）+ forget 族收口
//	（recycleObs/drainObs/TeardownApp——ADR-0082 §6 App 收口清理）+
//	workloadRun 归属登记（task 线 Ensure 事实）+ browses ensure cache 直删两条
//	形态债（browse 环单 goroutine 串行面，应改走 ensureForget 端口）。
//	条目不再命中真实写点即红（双向保鲜）。
//
// 已知局限（静态判据边界，评审承载）：
//	别名传递不判（`m := e.task.ensure; m[k]=v`）；编排文件持有
//	map[string]ensureMemo **参数**不判（端口实现面）；锁字段（mu/obsMu）
//	不在字段表——锁无赋值写点，锁入口纪律由 maintenanceguard/注释裁决
//	承载；drift 域签名缓存（sig/rolloutSig/stoppedSig）与 converge 域
//	自有状态（resolved/fails/replay/...）不在内核能力字段表——ADR 决策 7
//	的枚举面是 observations/expects/确保缓存族（+taskobs 钩子引出的 Run 轨
//	缓存），其余域内态由各自域文件自管。
//
// 防盲：编排文件数（≥45）/执法写点数（≥8）下限；字段表条目必须在包内
// 见到真实字段声明（改名即红）；ensure cache 登记表双向保鲜；上行钩子白名单锚
//（taskobs.go:handleRunObservation 必须在册且命中）；ledgerFields 对齐
//	断言；合成树 shapes（元侧写=绿/编排直写=红/方法调用=绿/白名单钩子=
//	绿/影子确保缓存=红/登记 ensure cache 声明=绿/归属漂移=红，ensuredledger
//	TestExpectedLedgerShape 范式）。

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

// metaClassMeta / metaClassDerived 是 ② 编排层文件表的两类归属值。
const (
	metaClassMeta    = "meta"
	metaClassDerived = "derived"
)

// metaFileClass 是 ② 编排层文件表（68 文件全量；设计稿 §2.5 五线 +
// §2.3 分层合一，归属口径见文件头）。新增/删除文件必须同批改表；
// 内核新增内核原语文件归内核、编排文件归编排——改判走评审同批改表。
var metaFileClass = map[string]string{
	// ---- 元（14）：L0 + L1 + 组合根 ----
	"loop.go": metaClassMeta, "transition.go": metaClassMeta, "ensure.go": metaClassMeta,
	"observ.go": metaClassMeta, "observdomain.go": metaClassMeta,
	"converge.go": metaClassMeta, "resolve.go": metaClassMeta, "volumes.go": metaClassMeta,
	"pubsub.go":       metaClassMeta, // B4b：pubsub 类型收敛步（应用模块内 database.go 同形）
	"bucketstore.go":  metaClassMeta, // B5：bucketstore 类型收敛步（应用模块内 pubsub.go 同形）
	"subresources.go": metaClassMeta, // B8：子资源对账段（dataservices 应用模块内——声明行 ensure/GC 的应用模块写面；engine 侧无此文件，契约测试报文以文件名词称）
	"lifecycle.go":    metaClassMeta,
	"engine.go":       metaClassMeta, "construct.go": metaClassMeta, "options.go": metaClassMeta,
	"health.go": metaClassMeta, "events.go": metaClassMeta, "schemareg.go": metaClassMeta,
	// 输入面契约包 engine/api（P3-7）：纯 DTO/哨兵/事件载荷构造与输入面
	// 原语，不持内核能力字段——归元侧（同 contract 叶子口径；基名键与
	// 根包同名文件 events.go 共用元归属）。
	"api_aliases.go": metaClassMeta, "contract.go": metaClassMeta, "deploy.go": metaClassMeta,
	"doc.go": metaClassMeta, "errors.go": metaClassMeta, "network.go": metaClassMeta, "tasks.go": metaClassMeta,
	// ---- 编排·受理线（9，§2.5；B1 票据临界区随受理线，升格裁定不入 L0）----
	"admission.go": metaClassDerived, "confirm.go": metaClassDerived, "deployspec.go": metaClassDerived,
	"retention.go":        metaClassDerived,
	"functionRoutes.go":   metaClassDerived,
	"wakeface.go":         metaClassDerived,
	"intakeface.go":       metaClassDerived, // IntakeBuildFace 窄端口适配（P2-3——核持接口适配面，wakeface 同族）
	"functionstats.go":    metaClassDerived, // 请求观测核门面（P2-4——只读观测投影，derived 面）
	"componentshealth.go": metaClassDerived, // 组件健康聚合单源（B2b——只读快照面，derived）
	"functions.go":        metaClassDerived,
	"predeploy.go":        metaClassDerived, "render.go": metaClassDerived, "projection.go": metaClassDerived,
	"materialize.go": metaClassDerived, "preview.go": metaClassDerived, "scaleprocess.go": metaClassDerived,
	// ---- 编排·策略线（4）----
	"bluegreen.go": metaClassDerived, "rollback.go": metaClassDerived,
	"restart.go": metaClassDerived, "teardown.go": metaClassDerived,
	// ---- 编排·看门狗（drift+drive，§1 蓝图）----
	"drift.go": metaClassDerived, "drive.go": metaClassDerived,
	// ---- 编排·任务线（8，§3 任务族全族随线；task/taskview §2.5 未单列，
	// 合一裁定随任务线——workloadRun 归属登记写点因此入白名单执法面）----
	"task.go": metaClassDerived, "taskverbs.go": metaClassDerived, "taskobs.go": metaClassDerived,
	"taskview.go": metaClassDerived, "minttask.go": metaClassDerived, "schedule.go": metaClassDerived,
	"processrestart.go": metaClassDerived, "componentrestart.go": metaClassDerived,
	// ---- 编排·平台面（7；networkrebuild 持 maintenanceGate 原语但文件
	// 本体是平台面编排——§2.5/§2.2#5 分置口径）----
	"routepublish.go": metaClassDerived, "apipublish.go": metaClassDerived, "console.go": metaClassDerived,
	"networkrebuild.go": metaClassDerived, "hygiene.go": metaClassDerived,
	"peers.go": metaClassDerived, "nodes.go": metaClassDerived,
	// ---- 编排·应用模块（应用模块是编排住留地；已拆包应用模块〔logpipes/
	// execs/metrics，P5 wave-1/2〕文件随 应用模块集（import 图派生） 扩容面
	// 继续在本表分类执法）----
	"module.go":      metaClassDerived, // 应用模块契约实现（Name/Loops；无内核能力私有状态写点，随编排面执法）
	"intake.go":      metaClassDerived, // intake 应用模块实例态/Deps/New（P5 wave-5 拆包新增壳文件）
	"gitmaterial.go": metaClassDerived, "materials.go": metaClassDerived, "buildsource.go": metaClassDerived,
	"builder.go": metaClassDerived, "buildlog.go": metaClassDerived, "registrycontent.go": metaClassDerived,
	"functionbuild.go": metaClassDerived, // Function 构建源消费面（P2-3：D-4 重拾/输入组装——intake 卫星）
	"backup.go":        metaClassDerived, "platformbackup.go": metaClassDerived,
	"metrics.go": metaClassDerived, "alerting.go": metaClassDerived, "channelnotify.go": metaClassDerived,
	"exposition.go": metaClassDerived, "crashnotify.go": metaClassDerived,
	"exec.go": metaClassDerived, "execrelay.go": metaClassDerived,
	// dataservices 应用模块（B2e，ADR-0096 决策 1 单应用模块包——自 databases 应用模块
	// 整包更名；dataservices.go 是应用模块壳〔实例态/Deps/New/分类型步〕）。
	"dataservices.go": metaClassDerived,
	"database.go":     metaClassDerived, "databaseenv.go": metaClassDerived, "rotate.go": metaClassDerived,
	"update.go": metaClassDerived, // B2d 双车道 Update（dataservices 应用模块；ADR-0095 决策 5/6）
	"origin.go": metaClassDerived, // ADR-0097 库卷异源标记门（dataservices 应用模块）
	"browse.go": metaClassDerived, "browseloop.go": metaClassDerived,
	"managed.go":  metaClassDerived,
	"logging.go":  metaClassDerived,
	"diagnose.go": metaClassDerived,
	// capacity 应用模块（Functions P2-2 决策环，ADR-0103：实例态/Deps/决策
	// 步——行写经 CommitWrite 端口，无内核能力私有状态写点；立环时漏分类，
	// 随 应用模块集（import 图派生） 补登记同批入册）。
	"capacity.go": metaClassDerived,
}

// metaStateFields 是 ① 内核能力私有状态字段表（字段 Sel 名 → 族属理由）。
// 改名/删除字段必须同批改表（测试内有「包内必须见到真实字段声明」的
// 保鲜断言）。
var metaStateFields = map[string]string{
	// 观测底座（observdomain.go observations struct；L0#7）。
	"observations": "obs hub: workloadID → latest event",
	"workloadApp":  "obs hub: workloadID → owner (routing)",
	"ensuredGen":   "obs ledger: workloadID → ensured generation",
	"ensuredSpec":  "obs ledger: workloadID → projected spec",
	"ensuredBy":    "obs ledger: workloadID → writing deployment",
	"exitSig":      "obs hub: terminal-exit dedupe signature",
	// 期望台账（observdomain.go expects struct；ADR-0082 §6）。
	"expected": "expect ledger: owner → expected generation",
	// 确保缓存族（设计稿 §2.2 #4：7 张 ensure cache map 分住域 struct，字段名全集）。
	"ensure":  "memo family: tasks/browses/manageds/dataservices ensureMemo tables",
	"release": "memo family: deploys(release) ensureMemo table",
	"pub":     "memo family: proxies(pub) ensureMemo table",
	"memo":    "memo family: converges(memo) ensureMemo table",
	// Run 轨观测缓存（taskobs.go tasks struct；L0#7 的 Run 轨分家，P1-7）。
	"runObs":      "task obs cache: runID → latest event",
	"workloadRun": "task obs cache: workloadID → taskID ownership",
}

// metaProtectedStructs 是持有受保护字段的 struct 类型名（D2 整构执法面）。
var metaProtectedStructs = map[string]bool{
	"observations": true, "expects": true, "tasks": true, "deploys": true,
	"proxies": true, "converges": true, "manageds": true, "databases": true,
	"browses": true,
}

// metaWriteExemptions 是编排文件直写白名单（file:fn → 理由）。方法调用
// 是天然合法入口、不在本表；本表只豁免「直写形态但经裁决合法」的写点。
// 条目不再命中真实写点即红（双向保鲜）。
var metaWriteExemptions = map[string]string{
	// 上行钩子 1（设计稿 §2.4：watch 扇出 verdict-owner 分发，Run 轨→task
	// 状态机）——观测缓存刷新是裁决入口第一拍，登记函数无法代位。
	"taskobs.go:handleRunObservation": "upstream hook 1 (design §2.4): runObs refresh is the verdict-entry first beat of the Run observation track",
	// forget 族收口（登记函数形态的清账写点）。
	"taskobs.go:recycleObs":   "forget family: per-Run terminal recycle (P1-7), tasks-domain registration function",
	"taskobs.go:drainObs":     "forget family: DeleteTask teardown double-clear (workloadRun+runObs)",
	"teardown.go:TeardownApp": "App teardown ledger clear (ADR-0082 §6; same adjudication as ensuredledger's TeardownApp entry — core-side consumer, not satellite)",
	// Run 轨归属登记（recordX 族形态：Task Ensure 成功后的观测路由登记，
	// task.obsMu 临界区内）。
	"task.go:ensureTaskWorkloads": "ownership registration (recordX family): workloadRun write after successful Task Ensure, inside task.obsMu",
}

// metaWriteResult 是一次直写扫描的产出。
type metaWriteResult struct {
	files          int
	diskFiles      map[string]bool
	derivedFiles   int
	writePoints    int // 包内受保护字段写点总数（含元侧登记函数；防盲计量面）
	policedWrites  int // 编排文件内的写点数（含白名单；实测锚）
	exemptUsed     map[string]bool
	violations     []string
	declaredFields map[string]bool // 包内见到的全部 struct 字段名（字段表保鲜）
}

// metaChainField 报告表达式选择子链上首个受保护字段名（穿透 Index/Paren；
// LHS 形态：e.obs.x / x.expected[k] / b.ensure）。
func metaChainField(expr ast.Expr) (string, bool) {
	found := ""
	ast.Inspect(expr, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if _, hit := metaStateFields[sel.Sel.Name]; hit {
				found = sel.Sel.Name
				return false
			}
		}
		return true
	})
	return found, found != ""
}

// isEnsureMemoMap 报告类型表达式是否 ensure cache 表形态：map[string]ensureMemo
// （engine 核五表）或 map[string]contract.Memo（P5 wave-3 起 ensure cache 类型
// 真源下沉 engine/contract——browses 应用模块的随域表用限定名；两形态同权入
// D3 登记制对账）。
func isEnsureMemoMap(expr ast.Expr) bool {
	mt, ok := expr.(*ast.MapType)
	if !ok {
		return false
	}
	key, ok := mt.Key.(*ast.Ident)
	if !ok || key.Name != "string" {
		return false
	}
	switch elt := mt.Value.(type) {
	case *ast.Ident:
		return elt.Name == "ensureMemo"
	case *ast.SelectorExpr:
		pkg, ok := elt.X.(*ast.Ident)
		return ok && pkg.Name == "contract" && elt.Sel.Name == "Memo"
	}
	return false
}

// scanMetaStateWrites 扫描 roots（P5 wave-2 扫描面扩容：engine 树 ∪
// 应用模块集（import 图派生 applicationModules）——应用模块文件离开 engine 树后仍在编排层
// 直写律射程，拆包不是执法豁免；四件 root=engine 契约测试同款。exempt 注入：
// 真实树用包级表，shapes 用局部表）。
func scanMetaStateWrites(root string, moreRoots []string, classTable map[string]string, exempt map[string]string) (*metaWriteResult, error) {
	fset := token.NewFileSet()
	res := &metaWriteResult{
		diskFiles:      map[string]bool{},
		exemptUsed:     map[string]bool{},
		declaredFields: map[string]bool{},
	}
	violate := func(base string, pos token.Pos, fn, detail string) {
		res.violations = append(res.violations, fmt.Sprintf(
			"%s:%d %s — %s (ADR-0088 decision 7: the derived layer must reach meta-capability private state only through registration functions/ports)",
			base, fset.Position(pos).Line, fn, detail))
	}
	walk := func(root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "contract" {
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
			base := filepath.Base(path)
			res.files++
			res.diskFiles[base] = true
			class, registered := classTable[base]
			if !registered {
				res.violations = append(res.violations, fmt.Sprintf(
					"engine file %s has no meta/derived classification — register it in metaFileClass in the same commit (双向保鲜, ADR-0088 P4)", base))
				class = metaClassDerived // 未注册文件按编排执法（保守面）。
			}
			policed := class == metaClassDerived
			if policed {
				res.derivedFiles++
			}

			// 包内字段声明面（①字段表保鲜 + ③ensure cache 登记制对账，全树收集）。
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok || st.Fields == nil {
						continue
					}
					// memo 表本体字段随句柄合法化（批 1①a）：表本体未导出于
					// 句柄结构内（ensureCache/appliedCache.memo）——结构字段
					// 形态不再执法，影子表执法收口在裸 map 声明与 make 形态。
					for _, f := range st.Fields.List {
						for _, name := range f.Names {
							res.declaredFields[name.Name] = true
						}
					}
				}
			}

			// D3 影子确保缓存（表本体）：包级 var 声明（编排文件）。
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || !policed {
						continue
					}
					if isEnsureMemoMap(vs.Type) {
						violate(base, vs.Pos(), "<package var>", "declares a bare map[string]ensureMemo — a second memo table outside the frozen registry is a shadow primitive (design §2.6)")
					}
				}
			}

			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				// 局部 var 声明（编排文件）的影子表。
				if policed {
					ast.Inspect(fd.Body, func(n ast.Node) bool {
						if ds, ok := n.(*ast.DeclStmt); ok {
							if gd, ok := ds.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
								for _, spec := range gd.Specs {
									if vs, ok := spec.(*ast.ValueSpec); ok && isEnsureMemoMap(vs.Type) {
										violate(base, vs.Pos(), fd.Name.Name, "declares a local map[string]ensureMemo — shadow memo table (design §2.6)")
									}
								}
							}
						}
						return true
					})
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.AssignStmt:
						for _, lhs := range x.Lhs {
							if field, hit := metaChainField(lhs); hit {
								res.writePoints++
								if policed {
									res.policedWrites++
									if _, ok := exempt[base+":"+fd.Name.Name]; ok {
										res.exemptUsed[base+":"+fd.Name.Name] = true
									} else {
										violate(base, x.Pos(), fd.Name.Name, "direct write to meta-capability field "+field)
									}
								}
							}
						}
					case *ast.IncDecStmt:
						if field, hit := metaChainField(x.X); hit {
							res.writePoints++
							if policed {
								res.policedWrites++
								if _, ok := exempt[base+":"+fd.Name.Name]; ok {
									res.exemptUsed[base+":"+fd.Name.Name] = true
								} else {
									violate(base, x.Pos(), fd.Name.Name, "direct inc/dec on meta-capability field "+field)
								}
							}
						}
					case *ast.CallExpr:
						id, ok := x.Fun.(*ast.Ident)
						if !ok || id.Name != "delete" || len(x.Args) < 2 {
							// D3：编排文件内的 make(map[string]ensureMemo)。
							if id != nil && id.Name == "make" && policed && len(x.Args) >= 1 && isEnsureMemoMap(x.Args[0]) {
								violate(base, x.Pos(), fd.Name.Name, "make(map[string]ensureMemo) in the derived layer — memo tables are born only in the composition root / meta init")
							}
							return true
						}
						if field, hit := metaChainField(x.Args[0]); hit {
							res.writePoints++
							if policed {
								res.policedWrites++
								if _, ok := exempt[base+":"+fd.Name.Name]; ok {
									res.exemptUsed[base+":"+fd.Name.Name] = true
								} else {
									violate(base, x.Pos(), fd.Name.Name, "direct delete on meta-capability field "+field)
								}
							}
						}
					case *ast.CompositeLit:
						// D2：受保护 struct 的整构装配。
						tn := ""
						switch t := x.Type.(type) {
						case *ast.Ident:
							tn = t.Name
						case *ast.StarExpr:
							if id, ok := t.X.(*ast.Ident); ok {
								tn = id.Name
							}
						}
						if !policed || !metaProtectedStructs[tn] {
							return true
						}
						for _, elt := range x.Elts {
							kv, ok := elt.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							if id, ok := kv.Key.(*ast.Ident); ok && metaStateFields[id.Name] != "" {
								res.writePoints++
								if _, ok := exempt[base+":"+fd.Name.Name]; ok {
									res.policedWrites++
									res.exemptUsed[base+":"+fd.Name.Name] = true
								} else {
									violate(base, x.Pos(), fd.Name.Name, "composite-literal assembly of protected struct "+tn+" field "+id.Name)
								}
							}
						}
					}
					return true
				})
			}
			return nil
		})
	}
	for _, r := range append([]string{root}, moreRoots...) {
		if err := walk(r); err != nil {
			return nil, err
		}
	}
	sort.Strings(res.violations)
	return res, nil
}

// TestMetaStateWriteDiscipline：真实 engine 树 + 已拆应用模块包（P5 wave-2
// 扫描面扩容：应用模块集（import 图派生 applicationModules）——五件 root=engine 契约测试的
// 漏计件补位）的编排层禁直写执法（双表双向保鲜 + 防盲 + 白名单锚）。
func TestMetaStateWriteDiscipline(t *testing.T) {
	root := filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine"))
	res, err := scanMetaStateWrites(root, satellitePackageRoots(t), metaFileClass, metaWriteExemptions)
	if err != nil {
		t.Fatal(err)
	}
	// 防盲：扫描面下限（67 文件/53 编排/8 写点实测量级——P5 wave-2 起扫描
	// 面=engine 树 ∪ 已拆应用模块包，条数随拆包回升；红时先重测再调值）。
	if res.files < 60 {
		t.Fatalf("scanned only %d production engine files — guard is blind, fix the scan", res.files)
	}
	if res.derivedFiles < 45 {
		t.Fatalf("only %d derived files were policed — guard is blind, fix the scan or metaFileClass", res.derivedFiles)
	}
	if res.writePoints < 40 {
		t.Fatalf("only %d protected-field write points observed package-wide — guard is blind, fix the scan", res.writePoints)
	}
	if res.policedWrites < 6 {
		t.Fatalf("only %d protected-field write points observed in derived files — guard is blind, fix the scan or metaFileClass", res.policedWrites)
	}
	for _, v := range res.violations {
		t.Errorf("%s", v)
	}
	// ① 字段表保鲜：每个字段名必须在包内见到真实 struct 字段声明。
	for field := range metaStateFields {
		if !res.declaredFields[field] {
			t.Errorf("meta state field %s is not declared as any struct field in the engine package — renamed/removed? update metaStateFields (双向保鲜)", field)
		}
	}
	// ② 文件分类表保鲜：metaFileClass 的每个条目必须对应扫描面上真实存在的
	// 生产文件基名。diskFiles 此前只收不读——条目对应的文件搬走/改名后不会
	// 红，表只会长不缩（2026-11-24 二期设计批 0 补；设计稿 A 稿发现）。
	for base, class := range metaFileClass {
		if !res.diskFiles[base] {
			t.Errorf("metaFileClass entry %s (%s) no longer matches a production file on the scan surface — remove the entry (双向保鲜)", base, class)
		}
	}
	// ① 与 ensuredledger 的对齐互锁：ledgerFields ⊆ metaStateFields。
	for field := range ledgerFields {
		if _, ok := metaStateFields[field]; !ok {
			t.Errorf("ensuredledger field %s is missing from metaStateFields — the two field tables have drifted apart", field)
		}
	}
	// 白名单双向保鲜 + 上行钩子锚（钩子条目必须在册且命中）。
	for key, reason := range metaWriteExemptions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("meta write exemption %s must carry a reason", key)
		}
		if !res.exemptUsed[key] {
			t.Errorf("meta write exemption %s no longer matches a real write point — remove the entry (双向保鲜)", key)
		}
	}
	if _, ok := metaWriteExemptions["taskobs.go:handleRunObservation"]; !ok {
		t.Fatalf("upstream hook 1 whitelist entry taskobs.go:handleRunObservation is missing — the §2.4 hook adjudication must stay anchored")
	}
	t.Logf("metawrite scan: %d files (%d derived policed), %d protected write points (%d in derived files), %d/%d exemptions matched",
		res.files, res.derivedFiles, res.writePoints, res.policedWrites, len(res.exemptUsed), len(metaWriteExemptions))
}

// TestMetaStateWriteShapes：判据分辨力锚（合成树，ensuredledger
// TestExpectedLedgerShape 范式）。
func TestMetaStateWriteShapes(t *testing.T) {
	synth := func(t *testing.T, files map[string]string) *metaWriteResult {
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
		res, err := scanMetaStateWrites(root, nil, shapeMetaClass, shapeExemptions)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	metaSrc := `package engine

type observations struct{ observations map[string]int }

func (o *observations) record(x int) { o.observations[x] = 1 }
`
	derivedSrc := `package engine

type tasks struct{ runObs map[string]int; ensure map[string]ensureMemo }

type ensureMemo struct{ sig string }

func (t *tasks) observation(id string) int { return t.runObs[id] }
`
	cases := []struct {
		name    string
		files   map[string]string
		wantRed string // 期望命中的判据子串；空 = 必须全绿
	}{
		{
			name: "元侧登记函数直写=绿",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc,
			},
		},
		{
			name: "派生文件直写=红",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (e *Engine) seed(id string, v int) { e.obs.observations[id] = v }\n" + engineDeclForShapes,
			},
			wantRed: "direct write to meta-capability field observations",
		},
		{
			name: "派生文件 delete=红",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (e *Engine) clear(id string) { delete(e.expect.expected, id) }\n" + engineDeclForShapes,
			},
			wantRed: "direct delete on meta-capability field expected",
		},
		{
			name: "方法调用=合法入口（不命中）",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (e *Engine) record(id string, v int) { e.obs.record(v) }\n" + engineDeclForShapes,
			},
		},
		{
			name: "白名单钩子=绿",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (e *Engine) handleRunObservation(id string, v int) { e.task.runObs[id] = v }\n" + engineDeclForShapes,
			},
		},
		{
			name: "非白名单同形写点=红（白名单是函数粒度）",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (e *Engine) seedRunObs(id string, v int) { e.task.runObs[id] = v }\n" + engineDeclForShapes,
			},
			wantRed: "direct write to meta-capability field runObs",
		},
		{
			// 批 1①a 退役原「影子备忘字段=红（第 8 张表）」：memo 表本体
			// 字段随句柄合法化——表本体未导出于句柄结构内（ensureCache/
			// appliedCache.memo），结构字段形态即句柄本身的形态；影子表
			// 执法收口在裸 map 声明（包级/局部 var）与 make 形态（上/下例）。
			name: "句柄形态的 memo 表本体字段=绿（表本体随句柄未导出）",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\ntype memoHandle struct{ mu *sync.Mutex; memo map[string]ensureMemo }\n",
			},
		},
		{
			name: "登记 memo 声明=绿（表本体声明不在执法面）",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc,
			},
		},
		{
			name: "派生文件 make 影子表=红",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc + "\nfunc (t *tasks) privMemo() { t.ensure = make(map[string]ensureMemo) }\n",
			},
			wantRed: "make(map[string]ensureMemo)",
		},
		{
			name: "文件无元/派生归属=红",
			files: map[string]string{
				"engine/observdomain.go": metaSrc,
				"engine/taskobs.go":      derivedSrc,
				"engine/ghost.go":        "package engine\n\nfunc ghost() {}\n",
			},
			wantRed: "no meta/derived classification",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := synth(t, tc.files)
			if tc.wantRed == "" {
				if len(res.violations) != 0 {
					t.Fatalf("fixture must stay clean, got %v", res.violations)
				}
				return
			}
			hit := false
			for _, v := range res.violations {
				if strings.Contains(v, tc.wantRed) {
					hit = true
				}
			}
			if !hit {
				t.Fatalf("fixture must be flagged with %q, got %v", tc.wantRed, res.violations)
			}
		})
	}
}

// shapeMetaClass / shapeExemptions 是 shapes 合成树的局部表（与真实表
// 同构的最小面）。
var shapeMetaClass = map[string]string{
	"observdomain.go": metaClassMeta,
	"taskobs.go":      metaClassDerived,
}

var shapeExemptions = map[string]string{
	"taskobs.go:handleRunObservation": "hook shape",
}

// engineDeclForShapes 给合成树补 Engine 与 obs/expect/task 域字段形态
// （真实树中它们在 engine.go/observdomain.go，合成树按需内联）。
const engineDeclForShapes = `
type expects struct{ expected map[string]int }

type Engine struct {
	obs   observations
	expect expects
	task  tasks
}
`

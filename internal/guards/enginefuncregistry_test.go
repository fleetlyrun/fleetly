package guards

// engine 函数基线登记簿守卫（ADR-0087 验收锚第 3 条前件，P3a 批级守卫批
// 2026-11-01）。立法来源：docs/adr/0087-domain-rules-and-single-write-primitive.md
// 验收锚「P3a：engine 函数基线登记簿守卫上线」——裁决规则归域（决策 1/2：
// 「domain 学会裁决，application 保留执刑」）后，「规则继续在 application
// 落户」必须从静默变成红灯。
//
// 机制（writeclassguard 范式：基线快照 + 登记表双向保鲜 + 防盲锚）：
//
//	基线  internal/application/engine 全部非测试 .go 的函数全集（Engine
//	      方法 + 自由函数 + 域 struct 方法），key 形如 文件名:Recv.方法 或
//	      文件名:函数名（接收者限定，避免同名方法互撞），冻结为
//	      testdata/engine-func-baseline.golden；
//	判据  超基线的新函数若函数体**零 I/O 直接引用** → 命中，必须在
//	      engineFuncRegistry 登记去向（如 "→ domain/telemetry/alertrule.
//	      Evaluate"）或落 engineFuncExemptions 豁免（带理由）；
//	保鲜  登记/豁免条目不再命中（函数消失/改名/进了基线）即红；基线函数
//	      消失**不追**——基线只在再生成时刷新：
//	      go test ./internal/guards -run TestEngineFunctionBaselineRegistry -update
//	      （新函数入基线前先在登记/豁免面清账，或随评审裁定同 commit 刷新）。
//
// I/O 载体字段表（判据面，ADR-0087 决策 2「纯度 = 零 I/O 引用」的静态化）：
// Engine 的端口/存储/外呼字段 repos/runtime/tx/log/alertRules/channels/
// objectStore/utility/execFace/logging/metrics/registry/proxy/cipher/
// ghClient/builders（engineIOCarrierFields）+ self 根上 emit*/transit*/
// commit* 前缀方法调用（e.emitObservation/e.transit/e.commitWrite 族）；
// 聚合 repo 的 db/tx 两绑定形态段名单源引用 transitionguard 的
// transitionRepoFields（e.repos.Tasks.Get 与 s.Projects.Get 同权——顶层
// transact.Stores 形参与 Tx.Do 闭包形参一并入 self 集，ADR-0080）。
//
// 已知局限（直接引用启发式，同 transitionguard 口径——**间接 I/O 不判**：
// 「调本包 I/O 包装函数」的函数按本判据可判纯）：
//   - 函数体只调本包其他函数（那些函数内部做 I/O）→ 判纯、进登记面——
//     守卫职责是把下沉裁决摆在台面（红），不是证明语义纯度；
//   - clock 端口不在载体表：读 e.clock.Now() 的函数判纯是**故意的**——
//     时间参数化（决策 2「时间作参数传入」）正是下沉前动作，domain 侧
//     的 time.Now 调用禁由 domainpurity 同批执法；
//   - 非 *Engine 的域 struct 形参（func f(x *execs)）不在 self 集，其载体
//     引用不可见 → 同样落登记/豁免裁决面（封堵由评审承载）。
//
// 防盲锚：①基线条目数下限（见 TestEngineFunctionBaselineRegistry 实测锚）；
// ②扫描到 0 文件/0 函数即 fatal；③正向锚 = testdata/enginefuncanchor/
// 合成样本（零 I/O 裁决函数必须命中、持载体函数必须不命中——判据有
// 分辨力的活体证明，扫描器失明/过宽/过窄任一漂移都红）。

import (
	"flag"
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

// engineFuncBaselineUpdate 重写基线 golden（ADR-0087 P3a：基线只在守卫
// 再生成时刷新，刷新动作见文件头注释）。
var engineFuncBaselineUpdate = flag.Bool("update", false, "rewrite the engine function baseline golden")

// engineFuncBaselineGolden 是基线快照路径（一行一个 key，# 起始行为注释）。
const engineFuncBaselineGolden = "internal/guards/testdata/engine-func-baseline.golden"

// engineIOCarrierFields 是 I/O 载体字段段名集（ADR-0087 验收锚 3 判据面，
// Engine 字段的机械枚举；聚合 repo 段名单源引用 transitionRepoFields，
// 禁止在此复制一份）。
var engineIOCarrierFields = map[string]bool{
	"repos": true, "runtime": true, "tx": true, "log": true,
	"alertRules": true, "channels": true, "objectStore": true,
	"utility": true, "execFace": true, "logging": true, "metrics": true,
	"registry": true, "proxy": true, "cipher": true, "ghClient": true,
	"builders": true,
}

// engineFuncRegistry 是超基线命中函数的去向登记表（key → 下沉/将下沉的
// 域位置）。条目不再命中即红（双向保鲜）；本批零真实条目——首两单元
// （告警评估 → domain/telemetry/alertrule.Evaluate、Run 裁决表 →
// domain/automation/run.Adjudicate）已先于本守卫落地，engine 侧对应
// 函数在基线内。唯一在册条目是合成正向锚（见文件头防盲锚③）。
var engineFuncRegistry = map[string]string{
	"anchor.go:anchorEngine.pureVerdict": "positive anchor (synthetic fixture in testdata/enginefuncanchor/, not real engine code): proves the zero-I/O predicate actually fires — keep the fixture and this entry in lockstep",
}

// engineFuncExemptions 是超基线命中函数的豁免面（key → 理由：为何以纯
// 函数形态留在 engine）。条目不再命中即红。
var engineFuncExemptions = map[string]string{
	// dynamic 热应用 memo 端口三签名（B2d，ADR-0095 决策 6）：与 ensureMemo
	// 族（ensureFresh/Remember/Forget——基线内先例）同栖单锁 ensureMu 留核
	// ——锁与 map 配对拆散即拆散单写原语（ADR-0088 决策 4 的 applied 半边；
	// map 值操作零 I/O，裁决已在案）。
	"ensure.go:Engine.appliedDigestOf": "memo port lock protocol stays in the engine core (B2d, ADR-0095 decision 6): AppliedDigest read half shares ensureMu with the ensureMemo family (ADR-0088 decision 4) — zero I/O map access under lock, like the baselined ensureFresh/Remember/Forget",
	"ensure.go:Engine.appliedForget":   "memo port lock protocol stays in the engine core (B2d, ADR-0095 decision 6): AppliedDigest forget half shares ensureMu with the ensureMemo family (ADR-0088 decision 4) — zero I/O map access under lock, like the baselined ensureFresh/Remember/Forget",
	"ensure.go:Engine.appliedRemember": "memo port lock protocol stays in the engine core (B2d, ADR-0095 decision 6): AppliedDigest write half shares ensureMu with the ensureMemo family (ADR-0088 decision 4) — zero I/O map access under lock, like the baselined ensureFresh/Remember/Forget",
	// dataservices 卫星门面委托两枚（通则③：gRPC 消费点零改——I/O 在卫星，
	// 核侧是纯委托；同形的 TeardownDatabase/RotateDatabasePassword 在基线
	// 内）。B2e 前两函数经 `e.databases.X` 段名与 transitionRepoFields 的
	// databases **repo 字段**撞名而被判 I/O（偶然命中，非裁决）；归一为
	// `e.dataservices.X` 后段名不再撞，纯委托如实进豁免面（ADR-0096 决策 1
	// 明示漂移清单条目③的守卫侧实录）。
	"engine.go:Engine.UpdateDatabase":    "facade delegation to the dataservices satellite (B2d, ADR-0095 decision 5; rule-of-thumb ③): the dual-lane execution and its utility I/O live in dataservices/update.go — the core method only forwards; previously judged I/O by the accidental databases repo-field name collision (B2e, ADR-0096 decision 1)",
	"engine.go:Engine.CheckVolumeOrigin": "facade delegation to the dataservices satellite (ADR-0097 decision 4 B1-2; B4c family-neutral form: one facade dispatches database/pubsub inside origin.go; rule-of-thumb ③): the read-only marker probe and its utility I/O live in dataservices/origin.go — the core method only forwards; previously judged I/O by the accidental databases repo-field name collision (B2e, ADR-0096 decision 1)",
	// pubsub 族门面委托（B4b，ADR-0096 决策 1：database 族同形——I/O 在
	// dataservices/pubsub.go，核侧纯委托/kick/声明委托/owner 铸造）。
	"engine.go:Engine.KickPubSubs":          "loop kick for the pubsub family ring (B4b, ADR-0096 decision 1): a Loop.Kick forward like KickDatabases — the ring stays in the core, zero I/O",
	"engine.go:Engine.TeardownPubSub":       "facade delegation to the dataservices satellite (B4b, ADR-0096 decision 1; rule-of-thumb ③): the Remove I/O lives in dataservices/pubsub.go — the core method only forwards",
	"engine.go:Engine.RotatePubSubPassword": "facade delegation to the dataservices satellite (B4b, ADR-0096 decision 4; rule-of-thumb ③): the declarative rotation and Secret rewrite live in dataservices/pubsub.go — the core method only forwards",
	"engine.go:Engine.UpdatePubSub":         "facade delegation to the dataservices satellite (B4b, ADR-0095 decision 5 single static lane; rule-of-thumb ③): the row write and kick live in dataservices/pubsub.go — the core method only forwards",
	"engine.go:IsPubSubCredentialSecret":    "declaration delegation to dataservices (B4b): pure prefix predicate for the gRPC PutSecret guard, IsDatabaseCredentialSecret precedent",
	"engine.go:PubSubConnMaterial":          "declaration delegation to dataservices (B4b; renamed with the B6a dual-dialect ConnMaterial face): pure formula (template ConnMaterial + RenderConnMaterial + DNS name), BucketStoreConnMaterial precedent",
	"observdomain.go:pubsubOwner":           "workloadOwner constructor for the pubsub axis (B4b, ADR-0096 decision 2): zero-I/O value constructor like databaseOwner/browseOwner — domain discrimination stays in the core",
	// bucketstore 族门面委托（B5，ADR-0096 决策 1：pubsub 族同形——I/O 在
	// dataservices/bucketstore.go，核侧纯委托/kick/声明委托/owner 铸造）。
	"engine.go:Engine.KickBucketStores":          "loop kick for the bucketstore family ring (B5, ADR-0096 decision 1): a Loop.Kick forward like KickPubSubs — the ring stays in the core, zero I/O",
	"engine.go:Engine.TeardownBucketStore":       "facade delegation to the dataservices satellite (B5, ADR-0096 decision 1; rule-of-thumb ③): the Remove I/O lives in dataservices/bucketstore.go — the core method only forwards",
	"engine.go:Engine.RotateBucketStorePassword": "facade delegation to the dataservices satellite (B5, ADR-0096 decision 4; rule-of-thumb ③): the declarative rotation and Secret rewrite live in dataservices/bucketstore.go — the core method only forwards",
	"engine.go:Engine.UpdateBucketStore":         "facade delegation to the dataservices satellite (B5, ADR-0095 decision 5 single static lane; rule-of-thumb ③): the row write and kick live in dataservices/bucketstore.go — the core method only forwards",
	// 子资源实况探测门面（B8，ADR-0096 决策 10：删除披露行〔PreviewDelete*〕
	// 的受理面消费——admin 工具容器 I/O 在 dataservices/subresources.go，核侧
	// 纯委托）。
	"engine.go:Engine.TopicLiveInspect":       "facade delegation to the dataservices satellite (B8, ADR-0096 decision 10; rule-of-thumb ③): the admin-tool inspect I/O lives in dataservices/subresources.go — the core method only forwards",
	"engine.go:Engine.StreamLiveInspect":      "facade delegation to the dataservices satellite (B8, ADR-0096 decision 10; rule-of-thumb ③): the admin-tool inspect I/O lives in dataservices/subresources.go — the core method only forwards",
	"engine.go:Engine.BucketLiveInspect":      "facade delegation to the dataservices satellite (B8, ADR-0096 decision 10; rule-of-thumb ③): the admin-tool inspect I/O lives in dataservices/subresources.go — the core method only forwards",
	"engine.go:IsBucketStoreCredentialSecret": "declaration delegation to dataservices (B5): pure prefix predicate for the gRPC PutSecret guard, IsPubSubCredentialSecret precedent",
	"engine.go:BucketStoreConnMaterial":       "declaration delegation to dataservices (B5): pure formula (template ConnMaterial + DNS name), PubSubConnectionURL precedent",
	"observdomain.go:bucketStoreOwner":        "workloadOwner constructor for the bucketstore axis (B5, ADR-0096 decision 2): zero-I/O value constructor like pubsubOwner — domain discrimination stays in the core",
}

// engineFuncInventory 是一次函数清单扫描的产出。
type engineFuncInventory struct {
	keys      []string // 全部函数 key（基线面）
	pure      []string // 零 I/O 直接引用的函数 key（判据命中面）
	sawEngine bool     // 是否见到 Engine 类型声明（防盲）
}

// scanEngineFunctionInventory 解析 root 下全部非测试 .go（跳过 testdata/），
// 产出函数 key 全集与零 I/O 直接引用子集。self 集解析复用
// transitionguard 机制（engineSelfIdents + engineStoresIdents）并加一层：
// 顶层 transact.Stores 形参同权入集（commitTransition 等持 s 形态）。
func scanEngineFunctionInventory(t *testing.T, root string) engineFuncInventory {
	t.Helper()
	fset := token.NewFileSet()
	inv := engineFuncInventory{}

	// 第一遍：包内命名类型全集 + 缓存解析结果（单次 WalkDir）。
	engineTypes := map[string]bool{}
	type parsedFile struct {
		file *ast.File
		rel  string // 相对 root 的斜杠路径
	}
	var parsed []parsedFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
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
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					engineTypes[ts.Name.Name] = true
					if ts.Name.Name == "Engine" {
						inv.sawEngine = true
					}
				}
			}
		}
		parsed = append(parsed, parsedFile{file: file, rel: filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 防盲②：扫描到 0 文件即 fatal（守卫不得静默通过）。
	if len(parsed) == 0 {
		t.Fatalf("engine function scan found no production .go under %s — guard is blind, fix the scan", root)
	}

	// 第二遍：逐函数解析 self 集并按载体段/发射前缀归类。
	for _, pf := range parsed {
		for _, decl := range pf.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := pf.rel + ":" + engineFuncKey(fd)
			inv.keys = append(inv.keys, key)

			self := engineSelfIdents(fd, engineTypes)
			for name := range engineStoresIdents(fd) {
				self[name] = true
			}
			for _, param := range fd.Type.Params.List {
				if isTransactStoresType(param.Type) {
					for _, name := range param.Names {
						self[name.Name] = true
					}
				}
			}

			ioDirect := false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				base, segs := selectorChain(sel)
				if base == nil || !self[base.Name] {
					return true
				}
				for _, seg := range segs {
					if engineIOCarrierFields[seg] || transitionRepoFields[seg] {
						ioDirect = true
						return false
					}
				}
				// e.emit*/e.transit*/e.commit* 方法调用（前缀判据，发射/
				// 迁移/落账三族的 engine 侧执行面）。
				if name := sel.Sel.Name; strings.HasPrefix(name, "emit") ||
					strings.HasPrefix(name, "transit") || strings.HasPrefix(name, "commit") {
					ioDirect = true
					return false
				}
				return true
			})
			if !ioDirect {
				inv.pure = append(inv.pure, key)
			}
		}
	}
	// 防盲②：0 函数即 fatal。
	if len(inv.keys) == 0 {
		t.Fatalf("engine function scan found no functions under %s — guard is blind, fix the scan", root)
	}
	sort.Strings(inv.keys)
	sort.Strings(inv.pure)
	return inv
}

// engineFuncKey 生成函数的稳健 key：方法带接收者基类型限定（文件内
// Engine.close 与 execs.close 不互撞），自由函数裸名。
func engineFuncKey(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	if base := baseIdent(fd.Recv.List[0].Type); base != nil {
		return base.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// isTransactStoresType 报告类型表达式是否 transact.Stores 字面形态。
func isTransactStoresType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "transact" && sel.Sel.Name == "Stores"
}

// loadEngineFuncBaseline 载入基线 golden（# 起始行与空行跳过）。
func loadEngineFuncBaseline(t *testing.T) map[string]bool {
	t.Helper()
	baseline := map[string]bool{}
	for _, line := range strings.Split(readFileLF(t, engineFuncBaselineGolden), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		baseline[line] = true
	}
	if len(baseline) == 0 {
		t.Fatalf("baseline golden %s holds no keys — guard is blind, regenerate with -update (see file header)", engineFuncBaselineGolden)
	}
	return baseline
}

// writeEngineFuncBaseline 重写基线 golden（-update 通道）。
func writeEngineFuncBaseline(t *testing.T, keys []string) {
	t.Helper()
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("# engine 函数基线登记簿（ADR-0087 验收锚 3）——冻结快照，一行一个 key\n")
	b.WriteString("#（文件名:Recv.方法 / 文件名:函数名）。基线函数消失不追；超基线新函数\n")
	b.WriteString("# 零 I/O 直接引用时必须在 engineFuncRegistry 登记去向或落\n")
	b.WriteString("# engineFuncExemptions 豁免（enginefuncregistry_test.go）。\n")
	b.WriteString("#\n# 再生成：go test ./internal/guards -run TestEngineFunctionBaselineRegistry -update\n")
	for _, k := range sorted {
		b.WriteString(k)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(repoRoot(t), filepath.FromSlash(engineFuncBaselineGolden)), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestEngineFunctionBaselineRegistry：engine 函数基线登记簿执法（ADR-0087
// 验收锚 3）。超基线且零 I/O 直接引用的新函数必须登记去向/豁免；登记与
// 豁免条目不再命中即红（双向保鲜）；防盲锚三件（条目数下限 + 0 扫描
// fatal 已在扫描器内 + 合成正负锚）。
func TestEngineFunctionBaselineRegistry(t *testing.T) {
	inv := scanEngineFunctionInventory(t, filepath.Join(repoRoot(t), filepath.Join("internal", "application", "engine")))

	// -update 通道：重写基线后返回（刷新动作见文件头注释）。
	if *engineFuncBaselineUpdate {
		writeEngineFuncBaseline(t, inv.keys)
		t.Logf("rewrote %s with %d keys — re-run without -update to enforce", engineFuncBaselineGolden, len(inv.keys))
		return
	}

	// 诊断面：零 I/O 函数在基线内被冻结（不追），计数只供对账。
	t.Logf("engine inventory: %d functions, %d zero-I/O (all frozen inside the baseline)", len(inv.keys), len(inv.pure))
	baseline := loadEngineFuncBaseline(t)

	// 防盲①：基线条目数下限。实测 2026-11-01 = 594（P4 收官）→ 519
	//（wave-2 execs/metrics 迁出）→ 465（wave-3 backups/browses 迁出
	// 53 函数+指纹/尾部帮手 var 别名化 4 项，净增核门面 10+Kick 两枚）
	// → 440（wave-4 databases/manageds 迁出 ~33 函数 + ProjectDatabase/
	// 探测常量随域，核净增 databases 门面 2 + Kick 两枚归位 + 声明委托
	// 2 + gensFor 归位 ensure.go）→ 383（wave-5 末星 intake 迁出六文件
	// ~52 函数 + 终态副作用随卫星 transitBuildRelease + build 铸名/载荷
	// 下沉 engine/contract，核净增 intake 门面 6 + 声明委托 1，见 golden），
	// 阈值 360 给 wave-6 收官批（守卫降级/裁决表）留余量——红时先重测
	// 再调值。
	if len(baseline) < 360 {
		t.Fatalf("baseline golden holds only %d keys — guard is blind, fix the scan or regenerate the baseline", len(baseline))
	}
	// 防盲①补充：Engine 声明必须可见；单写原语前门必须在基线
	//（transition.go 的 transit 本体，ADR-0087 决策 3 的冻结面）——
	// 扫描器漏掉它即失明。
	if !inv.sawEngine {
		t.Fatalf("engine function scan saw no Engine type declaration — guard is blind, fix the scan")
	}
	if !baseline["transition.go:Engine.transit"] {
		t.Fatalf("baseline golden lacks transition.go:Engine.transit — guard is blind, fix the scan or regenerate the baseline")
	}

	// 判据面：超基线且零 I/O 直接引用 → 必须在登记/豁免面出现。
	anchor := scanEngineFunctionInventory(t, filepath.Join(repoRoot(t), "internal", "guards", "testdata", "enginefuncanchor"))
	hits := map[string]bool{}
	for _, k := range inv.pure {
		if baseline[k] {
			continue
		}
		hits[k] = true
	}
	// 正向锚（防盲③）：合成样本的零 I/O 函数与真实面同权进命中集。
	for _, k := range anchor.pure {
		hits[k] = true
	}
	for _, k := range sortedBoolKeys(hits) {
		if _, done := engineFuncRegistry[k]; done {
			continue
		}
		if _, ok := engineFuncExemptions[k]; ok {
			continue
		}
		t.Errorf("engine function %s is beyond the frozen baseline and has zero direct I/O references — a decision rule landing in application must be adjudicated (ADR-0087): sink it to domain and register the destination in engineFuncRegistry (e.g. \"→ domain/telemetry/alertrule.Evaluate\"), or register engineFuncExemptions with a reason", k)
	}

	// 双向保鲜：登记/豁免条目不再命中即红；条目键落回基线（函数进了
	// 冻结面）同样红——刷新基线时必须同批清账。
	for k, dest := range engineFuncRegistry {
		if dest == "" {
			t.Errorf("engineFuncRegistry entry %s must carry a destination", k)
		}
		if baseline[k] {
			t.Errorf("engineFuncRegistry entry %s is inside the frozen baseline — baseline functions need no registration; remove the entry (or re-adjudicate before refreshing the baseline)", k)
			continue
		}
		if !hits[k] {
			t.Errorf("engineFuncRegistry entry %s no longer matches a beyond-baseline zero-I/O hit (function gone/renamed/now touches I/O) — remove the entry", k)
		}
	}
	for k, reason := range engineFuncExemptions {
		if reason == "" {
			t.Errorf("engineFuncExemptions entry %s must carry a reason", k)
		}
		if baseline[k] {
			t.Errorf("engineFuncExemptions entry %s is inside the frozen baseline — remove the entry", k)
			continue
		}
		if !hits[k] {
			t.Errorf("engineFuncExemptions entry %s no longer matches a beyond-baseline zero-I/O hit — remove the entry", k)
		}
	}
}

// TestEngineFuncRegistryDetectorDiscrimination：判据分辨力锚（防盲③，
// testdata/enginefuncanchor/anchor.go 合成样本）。三函数分别钉判据的三条
// 路径：零 I/O 必须命中（纯）、载体段引用必须不命中、commit* 前缀方法
// 调用必须不命中——扫描器失明、过宽（纯函数也判 I/O）、过窄（什么都
// 判纯）任一漂移都红。
func TestEngineFuncRegistryDetectorDiscrimination(t *testing.T) {
	inv := scanEngineFunctionInventory(t, filepath.Join(repoRoot(t), "internal", "guards", "testdata", "enginefuncanchor"))
	if len(inv.keys) != 3 {
		t.Fatalf("anchor fixture must hold exactly 3 functions (pureVerdict/commitAnchor/ioVerdict), found %d (%v) — fix the fixture", len(inv.keys), inv.keys)
	}
	pure := map[string]bool{}
	for _, k := range inv.pure {
		pure[k] = true
	}
	if !pure["anchor.go:anchorEngine.pureVerdict"] {
		t.Errorf("anchor pureVerdict (zero direct I/O references) was not detected as pure — the predicate is over-broad or the scan is blind; fix enginefuncregistry_test.go, not the fixture")
	}
	if pure["anchor.go:anchorEngine.commitAnchor"] {
		t.Errorf("anchor commitAnchor (touches the log carrier field) was detected as pure — the carrier-segment predicate lost its teeth; fix enginefuncregistry_test.go, not the fixture")
	}
	if pure["anchor.go:anchorEngine.ioVerdict"] {
		t.Errorf("anchor ioVerdict (reaches I/O only through a commit*-prefixed method call) was detected as pure — the emit*/transit*/commit* prefix predicate lost its teeth; fix enginefuncregistry_test.go, not the fixture")
	}
}

// sortedBoolKeys 返回 bool 值 map 键的有序切片（map 迭代序不确定，报错
// 必须确定性；string 值形态的 sortedKeys 已住 terminalcounters_test.go）。
func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

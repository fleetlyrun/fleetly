package engine

import (
	"sync"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// observations 是观测枢纽的域内组件（P15/ADR-0048 决策 4：归属解析/
// 期望缓存/Route 后端解析从 Engine 汇聚结构独立——自有锁、自有快照面；
// 此前"全部收敛环共写同一结构"的锁竞争面收口在域内，加环/加域不再横穿
// Engine）。行为住本文件，Engine 持域值并在各域文件就近消费。
//
// 零行为变更锚（P15 裁决：结构裁量非性能修复）：拆分前后锁序、写入面
// 与读取语义逐字节等价——全测试绿 + golden 零漂移即验收；本批不得以
// 性能为由扩射程。
//
// P3 收官批 8 收编：observDomain → observations（观测复数名词，ADR-0075
// 命名法则）；Engine 字段名 obs 保持不变；观测归属（ownerDomain/
// workloadOwner）与期望锚（expects）同批自 engine.go 归位本文件。
type observations struct {
	mu           sync.RWMutex
	observations map[string]capability.WorkloadEvent // workloadID → 最新观测
	workloadApp  map[string]workloadOwner            // workloadID → 归属（观测路由/drift 跳过面）
	ensuredGen   map[string]uint64                   // workloadID → 最近 Ensure 的 Generation（就绪门集合界定）
	ensuredSpec  map[string]capability.Workload      // workloadID → 最近 Ensure 的投影 spec（ADR-0022 spec 对照 drift）
	// ensuredBy 是账本的写入部署归属（workloadID → Deployment ID）：drift
	// 期望侧只采用写入者 == 当前 LatestSucceeded 的条目（ADR-0082 §6）。
	// 写点当前只有 recordSpecs（Ensure 路径，materialize 单序列）；D3 收口
	// 时 detect 的账本重建（recordProjectedSpecs，写入者=基线行）将成为
	// 第二个写点。受管/Database 域不写（不参与 App 对照面）。
	ensuredBy map[string]string
}

// init 建域内缓存（Engine.New 装配期一次）。
func (o *observations) init() {
	o.observations = make(map[string]capability.WorkloadEvent)
	o.workloadApp = make(map[string]workloadOwner)
	o.ensuredGen = make(map[string]uint64)
	o.ensuredSpec = make(map[string]capability.Workload)
	o.ensuredBy = make(map[string]string)
}

// observe 写一条载体观测（Watch 消费单点；last-write-wins）。
func (o *observations) observe(ev capability.WorkloadEvent) {
	o.mu.Lock()
	o.observations[ev.WorkloadID] = ev
	o.mu.Unlock()
}

// recordSpecs 记录 Ensure 事实的完整面：归属 + per-Workload Generation 与
// 投影 spec + 账本归属（写入部署 ID）+ （调用方另落 expect 域的 App 级
// 锚）。App 部署链消费（spec 对照 drift 需要 ensuredSpec）。
//
// 期望账本纪律（ADR-0082 §6 钉死）：ensuredSpec/ensuredGen/ensuredBy 只由
// Ensure 路径写入（recordEnsured ← materialize 单序列），禁止观测播种——
// 受管域的 gen 观测播种先例（managed.go seedFromRuntime）不得照抄到 App
// 域，否则手动热修被播种成期望态，静默击穿 controller-wins。语义修靠
// converge 域：漂移的修复只经收敛动作（materialize→Ensure）回写本账本。
func (o *observations) recordSpecs(owner workloadOwner, deploymentID string, gen uint64, ws []capability.Workload) {
	o.mu.Lock()
	for _, w := range ws {
		o.workloadApp[w.ID] = owner
		o.ensuredGen[w.ID] = gen
		o.ensuredSpec[w.ID] = w
		o.ensuredBy[w.ID] = deploymentID
	}
	o.mu.Unlock()
}

// recordOwners 只登记归属与 Generation（受管域/Database 域：不参与 spec
// 对照，ensuredSpec 不落）。owner 逐 Workload 派生（受管域 = per-Process
// 系统锚；Database 域 = 行 ID）。
func (o *observations) recordOwners(gen uint64, ws []capability.Workload, ownerOf func(capability.Workload) workloadOwner) {
	o.mu.Lock()
	for _, w := range ws {
		o.workloadApp[w.ID] = ownerOf(w)
		o.ensuredGen[w.ID] = gen
	}
	o.mu.Unlock()
}

// ownerOf 返回载体归属（drift 跳过面/事件载荷）。
func (o *observations) ownerOf(wid string) (workloadOwner, bool) {
	o.mu.RLock()
	owner, ok := o.workloadApp[wid]
	o.mu.RUnlock()
	return owner, ok
}

// observationOf 返回最新观测（Database 域看门狗等单点读）。
func (o *observations) observationOf(wid string) (capability.WorkloadEvent, bool) {
	o.mu.RLock()
	ev, ok := o.observations[wid]
	o.mu.RUnlock()
	return ev, ok
}

// ensuredSpecOf 返回该载体最近 Ensure 的投影 spec（spec 对照 drift 的
// 期望侧单点读）。
func (o *observations) ensuredSpecOf(wid string) (capability.Workload, bool) {
	o.mu.RLock()
	w, ok := o.ensuredSpec[wid]
	o.mu.RUnlock()
	return w, ok
}

// ensuredByOf 返回该载体账本条目的写入部署 ID（drift 期望侧的归属采用
// 判据：写入者必须 == 当前 LatestSucceeded 行，ADR-0082 §6）。
func (o *observations) ensuredByOf(wid string) (string, bool) {
	o.mu.RLock()
	id, ok := o.ensuredBy[wid]
	o.mu.RUnlock()
	return id, ok
}

// forgetOwner 清某归属名下的全部缓存面，返回清掉的 workloadID 集（调用
// 方同步清 drift/稳态签名——签名只按 workloadID 键，跨 App 不得误删）。
func (o *observations) forgetOwner(owner workloadOwner) []string {
	o.mu.Lock()
	var wids []string
	for wid, o2 := range o.workloadApp {
		if o2 == owner {
			wids = append(wids, wid)
		}
	}
	for _, wid := range wids {
		delete(o.workloadApp, wid)
		delete(o.ensuredGen, wid)
		delete(o.ensuredSpec, wid)
		delete(o.ensuredBy, wid)
		delete(o.observations, wid)
	}
	o.mu.Unlock()
	return wids
}

// forget 清单载体缓存面（Database 拆载体等域内收口）。
func (o *observations) forget(wid string) {
	o.mu.Lock()
	delete(o.workloadApp, wid)
	delete(o.ensuredGen, wid)
	delete(o.ensuredSpec, wid)
	delete(o.ensuredBy, wid)
	delete(o.observations, wid)
	o.mu.Unlock()
}

// readyGen 是 L1 健康门的域内实现：某 App 在指定 Generation 下发的全部
// Workload（ensuredGen 界定）均观测到 running@gen。空集 = 本代零滚动
// （纯 scale / 全跳过——内容锚定等价下的合法形态，ADR-0064）：
// vacuously ready。调用序保证查询前 materialize 已入账（重启冷缓存由
// 首拍全量物化重建——C17 备忘同进程内存态，冷启动首拍必全量物化），
// 空集不可能是缓存未建。
func (o *observations) readyGen(appID string, gen uint64) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	for wid, owner := range o.workloadApp {
		if owner.domain != ownerApp || owner.id != appID || o.ensuredGen[wid] != gen {
			continue
		}
		ev, seen := o.observations[wid]
		// 就绪门：全部成员必须已观测且 running@gen（未观测 = 未就绪，
		// 与看门狗语义相反——后者未观测不咬合）。
		if !seen || ev.State != capability.WorkloadRunning || uint64(ev.Generation) != gen {
			return false
		}
	}
	return true
}

// scanGen 遍历某 App 在指定 Generation 下发的 Workload 集，返回（咬合
// 描述, 集合大小）。pred 返回非空即咬合（短路）；未观测的 Workload 不
// 咬合（等待/超时路径处理）。
func (o *observations) scanGen(appID string, gen uint64, pred func(capability.WorkloadEvent) string) (string, int) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	count := 0
	for wid, owner := range o.workloadApp {
		if owner.domain != ownerApp || owner.id != appID || o.ensuredGen[wid] != gen {
			continue
		}
		count++
		ev, seen := o.observations[wid]
		if !seen {
			continue
		}
		if msg := pred(ev); msg != "" {
			return msg, count
		}
	}
	return "", count
}

// routeExpectations 是 Route 后端解析的期望集快照——蓝绿代次解析的显式
// 接口（ADR-0048 决策 4/P15：resolveBackend 消费代次化地址，不再是观测
// 缓存"不参与决策"口径的例外）。过滤规则按进程组收窄：仅当某进程在
// 缓存里有多个成员（双代窗两代并存——BG 旧代可能是代次化或 rolling
// 时代的稳定名）且 App 有在服代（servingGen > 0）时，保留在服代成员；
// 单成员进程恒通过——rolling 发布中（替换即同 ID 更新，缓存单成员）与
// 无在途稳态零行为漂移。servingGen == 0 = 无在途：全通过（stale 条目
// 无活载体——Addresses 以最近 Ensure 存活集为真源，不产端点）。
func (o *observations) routeExpectations(appID string, servingGen uint64) []capability.Workload {
	o.mu.RLock()
	defer o.mu.RUnlock()
	type member struct {
		wid string
		w   capability.Workload
		gen uint64
	}
	byProcess := map[string][]member{}
	for wid, owner := range o.workloadApp {
		if owner.domain != ownerApp || owner.id != appID {
			continue
		}
		w, ok := o.ensuredSpec[wid]
		if !ok {
			continue
		}
		byProcess[w.Process] = append(byProcess[w.Process], member{wid: wid, w: w, gen: o.ensuredGen[wid]})
	}
	var out []capability.Workload
	for _, ms := range byProcess {
		keep := ms
		if servingGen != 0 && len(ms) > 1 {
			var serving []member
			for _, m := range ms {
				if m.gen == servingGen {
					serving = append(serving, m)
				}
			}
			if len(serving) > 0 {
				keep = serving // 窗内在服代圈定；全部不匹配（窗口未记账）保守全通过
			}
		}
		for _, m := range keep {
			out = append(out, m.w)
		}
	}
	return out
}

// ensuredGenOf 返回载体最近 Ensure 的 gen（Drift 逐载体对照锚——双代窗
// 两代并存时 owner 级单值无法对照两代，ADR-0048 决策 2）。
func (o *observations) ensuredGenOf(wid string) (uint64, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	gen, ok := o.ensuredGen[wid]
	return gen, ok
}

// observSnapshot 是观测槽与归属的锁内一致快照（漂移扫描等巡检面的
// 域快照面——锁外消费，无锁内回调）。
type observSnapshot struct {
	observations map[string]capability.WorkloadEvent
	workloadApp  map[string]workloadOwner
}

// snapshot 取观测槽与归属的一致快照（副本——调用方修改不回写）。
func (o *observations) snapshot() observSnapshot {
	o.mu.RLock()
	defer o.mu.RUnlock()
	s := observSnapshot{
		observations: make(map[string]capability.WorkloadEvent, len(o.observations)),
		workloadApp:  make(map[string]workloadOwner, len(o.workloadApp)),
	}
	for wid, ev := range o.observations {
		s.observations[wid] = ev
	}
	for wid, owner := range o.workloadApp {
		s.workloadApp[wid] = owner
	}
	return s
}

// crossProjectRefs 聚合 App 域期望缓存中的跨域网络附件（per-App；隔离
// 不变式巡检的输入快照——锁内聚合、锁外复核批准态）。
func (o *observations) crossProjectRefs() map[string][]capability.NetworkRef {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := map[string][]capability.NetworkRef{}
	for wid, ensured := range o.ensuredSpec {
		if len(ensured.NetworkRefs) == 0 {
			continue
		}
		owner := o.workloadApp[wid]
		if owner.domain != ownerApp {
			continue
		}
		out[owner.id] = append(out[owner.id], ensured.NetworkRefs...)
	}
	return out
}

// ---- 观测归属与期望锚（P3 收官批 8 自 engine.go 归位；类型体与注释
// 逐字随迁，仅头注释补收编范式）----

// ownerDomain 是观测归属的域判别。
type ownerDomain uint8

const (
	ownerApp      ownerDomain = iota // App 进程载体
	ownerDatabase                    // Database 载体（ADR-0029）
	ownerSystem                      // 受管域进程载体（fleetly/system）
	ownerBrowse                      // Browse 会话载体（ADR-0051）
)

// workloadOwner 是 workloadID 与 Drift 期望锚的类型化归属（域 + 域内锚
// ID）。取代三套字符串前缀词汇（appID 直填 / database/<id> /
// fleetly/system/<process>）——观测路由与看门狗按域分派不再前缀匹配；
// workload.stopped 载荷的 app_id 字段仅 App 域载体携带（词汇回归
// CONTEXT.md：app_id 是 App 行 ID，不是内部路由键——架构评审第二轮
// 候选 5 行为批，2026-10-03）。
type workloadOwner struct {
	domain ownerDomain
	id     string // App 行 ID / Database 行 ID / 受管进程名
}

func appOwner(appID string) workloadOwner      { return workloadOwner{ownerApp, appID} }
func databaseOwner(id string) workloadOwner    { return workloadOwner{ownerDatabase, id} }
func systemOwner(process string) workloadOwner { return workloadOwner{ownerSystem, process} }

// expects 是 Drift 对照锚（类型化归属 → 最近 Ensure 的 Generation）。
// P3 收官批 8 收编：expectDomain → expects（复数普通名词零后缀，ADR-0075
// 命名法则）；Engine 字段名 expect 保持不变。
type expects struct {
	mu       sync.Mutex
	expected map[workloadOwner]uint64
}

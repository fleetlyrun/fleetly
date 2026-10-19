package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// anchoringPollInterval 是 Watch 循环里节点锚定扫描的节拍（节点加入检出
// 上限 ≈ 该间隔；事件流覆盖即时路径）。
const anchoringPollInterval = 10 * time.Second

// Ensure 幂等下发期望状态（架构 §5：唯一写动词）。
//
// 域内收敛语义：以 ns 选择器列出 fleetly 管辖的现存服务，逐 Workload
// create-or-update（update 以 spec 全量替换 + 版本号），不在期望集内的
// 域内服务移除——同 Generation 重放安全（领域模型场景 1：发布中途被杀，
// 重启后按 Generation 幂等重下发）。
//
// 稳态短路两道闸（受管域每拍重放，写面必须收敛到零）：
//   - no-op 断路器（内存账本，lastIssued）：期望 canonical 与最近一次确认
//     服务端持有的完全一致即跳过 update 判定链——daemon 版本漂移新增物化
//     字段时（inspect 回读多出平台不管理的字段）serviceSpecEqual 恒不等，
//     无此闸会每拍重发 update 自激复燃（update 产事件 → Kick → 再
//     update）。已知边界：平台侧手工改动载体在期望不变时不再自愈（spec
//     对照 drift 经 InspectWorkloads 观测面仍可见，ADR-0022）。
//   - serviceSpecEqual（inspect 回读比对）：修复前的既有闸，兜住账本作废
//     （失败重置）后的第一拍。
func (p *Provider) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (report capability.EnsureReport, err error) {
	// 期望集载体名 + 碰撞前置拒绝（N1 收尾批 B9）：不同 Workload 折叠成同
	// 载体名（进程名仅差特殊字符经 sanitize 同形，如 web.1 / web-1）会让
	// 后者静默覆盖前者——一进程无声丢失。spec 侧字符集白名单是主防线，此
	// 处对存量 Revision 与绕过路径 fail-closed；纯检先于任何材料/服务副
	// 作用（同 Generation 重放安全不变：同 Workload 重复出现不算碰撞）。
	desired := make(map[string]string, len(ws))
	for _, w := range ws {
		name := workloadServiceName(ns, w, gen)
		if prev, ok := desired[name]; ok && prev != w.ID {
			return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: workloads %s and %s both resolve to service name %q (carrier name collision; process names must be distinct DNS labels)", ns, prev, w.ID, name)
		}
		desired[name] = w.ID
	}

	// 断路器账本整拍作废（C19-1）：Ensure 失败 = 拍内收敛未完成，本拍期
	// 望集的落账条目可信度作废，下拍保守重发（重发先过 serviceSpecEqual
	// 门，代价一次 inspect 读，无风暴面）。
	defer func() {
		if err != nil {
			p.forgetLastIssued(sortedKeys(desired)...)
		}
	}()

	// 材料先行（ADR-0014）：网络 create-or-get + Secret 载体落盘，再翻译
	// 载体 spec（引用载体名）。逐载体材料覆写（ADR-0048 双代窗）：旧代
	// 成员持基线材料——secret 引用与拉取凭证按各自材料面解析，调用级
	// 材料不再全域改写（旧代零扰动锚）；nil = 调用级（域默认，既有语义）。
	if err := p.ensureNetworks(ctx, ns, ws); err != nil {
		return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, err)
	}
	callCarriers, err := p.ensureSecrets(ctx, m)
	if err != nil {
		return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, err)
	}
	callConfigs, err := p.ensureConfigs(ctx, m)
	if err != nil {
		return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, err)
	}
	carrierSets := map[*capability.Materials]map[string]secretCarrier{&m: callCarriers}
	configSets := map[*capability.Materials]map[string]secretCarrier{&m: callConfigs}
	for i := range ws {
		pm := ws[i].Materials
		if pm == nil {
			continue
		}
		if _, seen := carrierSets[pm]; !seen {
			c, cerr := p.ensureSecrets(ctx, *pm)
			if cerr != nil {
				return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, cerr)
			}
			carrierSets[pm] = c
		}
		if _, seen := configSets[pm]; !seen {
			c, cerr := p.ensureConfigs(ctx, *pm)
			if cerr != nil {
				return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, cerr)
			}
			configSets[pm] = c
		}
	}
	materialsOf := func(w capability.Workload) capability.Materials {
		if w.Materials != nil {
			return *w.Materials
		}
		return m
	}

	existing, err := p.listNsServices(ctx, ns)
	if err != nil {
		return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: list existing: %w", ns, err)
	}

	// 网络目标解析的 per-Ensure 备忘（C19-3）：拍内多 Workload 引用同名
	// 网络只探一次 daemon；Ensure 序结束随局部变量废弃。
	resolvedNets := map[string]netResolve{}
	report.Gens = make(map[string]capability.Generation, len(ws))
	// wireGen 落报告：跳过/scale 分支必须回传现存代戳（引擎按 wire-gen
	// 记观测归因锚，缺席 = 按调用 gen——create/full-update 分支）。
	wireGen := func(wid string, svc swarm.Service) {
		if g := serviceWireGen(svc); g > 0 {
			report.Gens[wid] = capability.Generation(g)
		}
	}
	// changed 落变更集（ADR-0082 §2 第 3 条）：审计/事件分流的唯一事实源
	// ——只有真实写穿内容等价判定的分支才计入（内容寻址 skip 不计）。
	changed := func(wid string) {
		report.Changed = append(report.Changed, wid)
	}
	for _, w := range ws {
		carriers := callCarriers
		if w.Materials != nil {
			carriers = carrierSets[w.Materials]
		}
		configs := callConfigs
		if w.Materials != nil {
			configs = configSets[w.Materials]
		}
		spec := toServiceSpec(ns, w, gen, carriers, configs)
		// 网络引用名→ID 先行解析（服务端对名字输入会改写为 ID——发送
		// ID 使回读形态与发送形态一致，no-op 比对的前提）。
		spec, nerr := p.resolveNetworkTargets(ctx, spec, resolvedNets)
		if nerr != nil {
			return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, nerr)
		}

		auth, err := p.registryAuthFor(ctx, w.Image, materialsOf(w))
		if err != nil {
			return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: %w", ns, err)
		}

		// 期望 spec canonical 只算一次（C19-4）：断路器比对、等价判定、
		// 落账三处共用（此前等价判定内部双算 desired 侧）。
		desiredJSON := canonicalSpecJSON(spec)

		inspect, err := p.cli.ServiceInspect(ctx, spec.Name, client.ServiceInspectOptions{})
		if err != nil {
			// 视 404 与其余错误：NotFound → create；其他错误直接上抛。
			if !isNotFound(err) {
				return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: inspect %s: %w", ns, spec.Name, err)
			}
			if _, err := p.cli.ServiceCreate(ctx, client.ServiceCreateOptions{
				Spec:                spec,
				EncodedRegistryAuth: auth,
			}); err != nil {
				return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: create %s: %w", ns, spec.Name, err)
			}
			p.recordLastIssued(spec.Name, desiredJSON) // create 即入账：服务端已持有该 spec
			changed(w.ID)                              // create = 真实变更（新载体）
			continue                                   // wire-gen = 调用 gen（create 落的就是新戳）
		}
		svc := inspect.Service
		if desiredJSON != "" && desiredJSON == p.lastIssuedOf(spec.Name) {
			// no-op 断路器（C19-1）：期望与最近一次确认下发一致 → update
			// 判定链整体短路。服务级 inspect 已完成，服务意外缺失仍会在
			// 下一拍由 404 分支重建（账本只在写确认时落，不豁免存在性检查）。
			resumed, rerr := p.resumeStalledRollout(ctx, ns, spec, svc, auth, desiredJSON)
			if rerr != nil {
				return report, rerr
			}
			if resumed {
				// resume 是一次真实更新下发（ForceUpdate 自增重滚）：计入变更集。
				changed(w.ID)
			} else {
				wireGen(w.ID, svc)
			} // resume 已重发：wire-gen = 本拍 gen（报告缺席）
			continue
		}
		if serviceSpecEqualJSON(desiredJSON, svc.Spec) {
			// 语义等价即跳过 update——受管域收敛环每拍重放 Ensure，
			// 无条件 update 会让 identical spec 也滚替任务（update 产
			// 事件 → Kick → 再 update 的自激环；staging 真机实证：
			// 受管库载体 20s 内 49 次版本推进、postgres 反复优雅退出
			// 永不停稳，2026-10-02）。代戳已从等价判定剥离（ADR-0064）：
			// 跨代未变载体走本分支，服务保留旧代戳零触碰。
			p.recordLastIssued(spec.Name, desiredJSON) // 服务端现态即期望：回填账本（作废后重建）
			resumed, rerr := p.resumeStalledRollout(ctx, ns, spec, svc, auth, desiredJSON)
			if rerr != nil {
				return report, rerr
			}
			if resumed {
				// resume 是一次真实更新下发（ForceUpdate 自增重滚）：计入变更集。
				changed(w.ID)
			} else {
				wireGen(w.ID, svc)
			}
			continue
		}
		if canonicalContentJSON(spec) == canonicalContentJSON(svc.Spec) {
			// scale-only（ADR-0064 决策 2）：内容等价（modulo 代戳与副本
			// 数）仅副本数不同——保留在服任务模板与服务级标签原样回发，
			// swarm 语义下任务模板未变 = 存量任务零替换、只增删任务；代
			// 戳与模板恒同移（未变内容不换代），观测归因锚不漂。
			scaleSpec := spec
			scaleSpec.TaskTemplate = svc.Spec.TaskTemplate
			scaleSpec.Labels = svc.Spec.Labels // 内嵌 Annotations 直取（staticcheck QF1008）
			if uerr := p.updateServiceCAS(ctx, ns, scaleSpec, svc, auth); uerr != nil {
				return report, uerr // update 失败：defer 整拍作废账本，下拍重发（C19-1）
			}
			p.recordLastIssued(spec.Name, desiredJSON) // 账本记期望内容（modulo gen + 新副本数）
			changed(w.ID)                              // 仅副本数也是真实更新下发（只增删任务）
			wireGen(w.ID, svc)
			continue
		}
		if uerr := p.updateServiceCAS(ctx, ns, spec, svc, auth); uerr != nil {
			return report, uerr // update 失败：defer 整拍作废账本，下拍重发（C19-1）
		}
		p.recordLastIssued(spec.Name, desiredJSON)
		changed(w.ID) // 内容变更整更新 = 真实变更（滚动替换）
	}

	// 域内收敛：期望集之外的 fleetly 管辖服务移除。
	for _, svc := range existing {
		if _, ok := desired[svc.Spec.Name]; !ok {
			if _, err := p.cli.ServiceRemove(ctx, svc.ID, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
				return capability.EnsureReport{}, fmt.Errorf("swarm ensure %s: remove stale %s: %w", ns, svc.Spec.Name, err)
			}
			p.forgetLastIssued(svc.Spec.Name) // 服务删除即清账（不复活、不泄漏）
			// 被移除者没有期望 Workload ID（ADR-0082 §2 第 4 条）：用载体名。
			report.Removed = append(report.Removed, "service/"+svc.Spec.Name)
		}
	}
	sort.Strings(report.Changed)
	report.Changed = slices.Compact(report.Changed)
	sort.Strings(report.Removed)
	report.Removed = slices.Compact(report.Removed)
	return report, nil
}

// updateServiceCAS 以版本号 CAS 更新服务，冲突时有界重试。引擎事件驱动
// Kick 在滚动替换期会把幂等重 Ensure 压到近零间隔连拍，inspect 读到的
// 版本可能落后 store 可见性（docker 29 真机实证："update out of
// sequence"，发布中重部署即触发）——重取版本再试；同 spec 重放本身幂等，
// 重试即正确（服务端拒绝的更新未生效）。
func (p *Provider) updateServiceCAS(ctx context.Context, ns capability.NamespaceRef, spec swarm.ServiceSpec, current swarm.Service, auth string) error {
	const maxAttempts = 3
	svc := current
	for attempt := 1; ; attempt++ {
		_, err := p.cli.ServiceUpdate(ctx, svc.ID, client.ServiceUpdateOptions{
			Version:             svc.Version,
			Spec:                spec,
			EncodedRegistryAuth: auth,
			QueryRegistry:       false,
			Rollback:            "",
		})
		if err == nil {
			return nil
		}
		if !isUpdateOutOfSequence(err) || attempt >= maxAttempts {
			return fmt.Errorf("swarm ensure %s: update %s: %w", ns, spec.Name, err)
		}
		inspect, ierr := p.cli.ServiceInspect(ctx, spec.Name, client.ServiceInspectOptions{})
		if ierr != nil {
			return fmt.Errorf("swarm ensure %s: re-inspect %s: %w", ns, spec.Name, ierr)
		}
		svc = inspect.Service
	}
}

// isUpdateOutOfSequence 识别 swarmkit 版本冲突：swarmkit 以 code=Unknown
// 返回，跨 API 边界无类型化哨兵，按其稳定文案匹配。
//
// 已验证 daemon 版本（E30 注释钉死）：docker 29.x 真机（staging 实证锚
// 2026-09-30 见 docker 29.8——节点 label 过滤盲区同批；"update out of
// sequence" 于发布中重部署在 docker 29 真机复现，C19 批）。文案匹配的
// 失效形态是 daemon 升级后改写错误文案（重试退化为单发失败）——升级
// daemon 时须真机重验本匹配与 isNodeVersionRace 的两文案同族。
func isUpdateOutOfSequence(err error) bool {
	return err != nil && strings.Contains(err.Error(), "update out of sequence")
}

// serviceSpecEqualJSON 判定期望 spec（canonical JSON，调用方单算传入，
// C19-4：与断路器/落账共用同一份）与现存服务 spec 语义等价：JSON 投影
// canonical 化（map 键排序 + 服务端归一化产物剥离）后逐字节比对。等价 =
// update 是 no-op，调用方据此跳过。比对失败（desiredJSON 为空 = marshal
// 异常）一律视为不等（保守：多一次 update 无害，漏 update 才有害）。
func serviceSpecEqualJSON(desiredJSON string, current swarm.ServiceSpec) bool {
	return desiredJSON != "" && desiredJSON == canonicalSpecJSON(current)
}

// no-op 断路器账本（C19-1）：服务名 → 最近一次确认服务端持有的期望
// canonical spec JSON。账本写入只发生在服务端确认持有该 spec 的时刻
// （create 成功 / update 成功 / inspect 等价回读），清账三时点：update 或
// Ensure 失败（整拍作废，下拍保守重发）、服务删除（域内收敛与 Remove）。
// 键是全局服务名（命名公式内嵌 ns 四元组，跨 ns 天然不撞）；账本单进程
// 内存态，重启即冷（首轮由 serviceSpecEqual 门兜住，无正确性依赖）。
// Ensure 可来自不同部署单写者环并发执行，账本互斥保护。

// recordLastIssued 落账。
func (p *Provider) recordLastIssued(name, canonical string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	if p.lastIssued == nil {
		p.lastIssued = map[string]string{}
	}
	p.lastIssued[name] = canonical
}

// lastIssuedOf 读账。
func (p *Provider) lastIssuedOf(name string) string {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	return p.lastIssued[name]
}

// forgetLastIssued 批量清账。
func (p *Provider) forgetLastIssued(names ...string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	for _, n := range names {
		delete(p.lastIssued, n)
	}
}

// pausedResumeWindow 是滚动停摆 resume 的时间窗护栏（ADR-0064 决策 5）：
// 同一服务窗内至多一次 force-resume。窗宽 > L1 ReleaseTimeout（120s）——
// 单次部署窗内 resume 恰一拍；boot 崩溃类应用的持续失败由 L1/回滚门超时
// 接管（回滚在内容锚定等价下是最小动作），resume 不 flap。
const pausedResumeWindow = 5 * time.Minute

// resumeStalledRollout（ADR-0064 决策 5）：期望 spec 与服务现态内容等价
// （modulo 代戳——两个跳过分支都会路过）但 UpdateStatus=paused = 平台
// 自己的滚动更新停摆（swarm FailureAction=pause + 新任务 boot-fatal；
// staging 两次致害：N/1 收案与 2026-10-11 双门超时案）。此前 Ensure 对
// 等价 spec 短路 = 永不 resume，停摆只能人工 docker service update
// --force 解卡。本路径以 ForceUpdate 自增重发（--force 等价语义；restart
// 面同款惯用），完成停摆滚动的收敛；时间窗护栏防 flap。
//
// resumed=false 且 err=nil = 无停摆或窗内已 resume 过（调用方按跳过处理，
// 报告 wire-gen = 现存代戳）。
func (p *Provider) resumeStalledRollout(ctx context.Context, ns capability.NamespaceRef, spec swarm.ServiceSpec, svc swarm.Service, auth, desiredJSON string) (resumed bool, err error) {
	if svc.UpdateStatus == nil || svc.UpdateStatus.State != swarm.UpdateStatePaused {
		return false, nil
	}
	now := time.Now()
	p.ledgerMu.Lock()
	if t, ok := p.resumedAt[spec.Name]; ok && now.Sub(t) < pausedResumeWindow {
		p.ledgerMu.Unlock()
		return false, nil
	}
	if p.resumedAt == nil {
		p.resumedAt = map[string]time.Time{}
	}
	p.resumedAt[spec.Name] = now
	p.ledgerMu.Unlock()
	resume := spec
	resume.TaskTemplate.ForceUpdate++
	if uerr := p.updateServiceCAS(ctx, ns, resume, svc, auth); uerr != nil {
		return false, fmt.Errorf("swarm ensure %s: resume stalled rollout %s: %w", ns, spec.Name, uerr)
	}
	p.recordLastIssued(spec.Name, desiredJSON) // canonical 剥离 ForceUpdate：账本值不变
	return true, nil
}

// canonicalSpecJSON 把 spec 归一为可比较的 JSON 字节串。
//
// 服务端归一化产物（staging 真机 inspect 对照实录，2026-10-02）在两侧一致剥离——
// 只比较平台受管字段：
//   - TaskTemplate.ContainerSpec.DNSConfig（空对象服务端物化）
//   - TaskTemplate.ContainerSpec.Isolation（"default" 缺省）
//   - TaskTemplate.Resources（零值对象服务端物化）
//   - TaskTemplate.RestartPolicy 的 Delay/MaxAttempts/Window（服务端缺省
//     节律；平台只管 Condition）
//   - TaskTemplate.Runtime（"container" 缺省）
//   - TaskTemplate.ForceUpdate（服务端回滚自增）
//   - RollbackConfig 整体（服务端缺省物化，平台不管理）
//   - UpdateConfig 的 Monitor/MaxFailureRatio（服务端缺省，平台不管理）
//
// **代戳剥离（ADR-0064 内容锚定等价）**：fleetly.generation 是观测归因锚
// 而非内容成分——Annotations.Labels 与 TaskTemplate.ContainerSpec.Labels
// 两处同源剥离。剥离前，任何新部署（全员盖新代戳）都让等价判定失配、
// 全部服务滚动替换任务（staging 实证 2026-10-11：单副本调整触发六服务
// 替换 → DNS 断档 → 健康门双超时）。未变载体由此跳过 update、保留其
// 现存代次（EnsureReport 回传 wire-gen）。
//
// Networks[].Target 的名字→ID 服务端解析在 Ensure 侧先行解析（发 ID 而非
// 名，服务端不再改写）。
func canonicalSpecJSON(spec swarm.ServiceSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	tt, _ := m["TaskTemplate"].(map[string]any)
	if tt != nil {
		tt["ForceUpdate"] = 0
		if rt, ok := tt["Runtime"].(string); ok && (rt == "" || rt == "container") {
			delete(tt, "Runtime")
		}
		if rp, ok := tt["RestartPolicy"].(map[string]any); ok {
			delete(rp, "Delay")
			delete(rp, "MaxAttempts")
			delete(rp, "Window")
		}
		if cs, ok := tt["ContainerSpec"].(map[string]any); ok {
			if d, ok := cs["DNSConfig"].(map[string]any); ok && len(d) == 0 {
				delete(cs, "DNSConfig")
			}
			if iso, ok := cs["Isolation"].(string); ok && (iso == "" || iso == "default") {
				delete(cs, "Isolation")
			}
			if labels, ok := cs["Labels"].(map[string]any); ok {
				delete(labels, labelGeneration)
				normalizeRestartSeqLabel(labels)
			}
		}
		if res, ok := tt["Resources"].(map[string]any); ok && len(res) == 0 {
			delete(tt, "Resources")
		}
	}
	// 服务级标签（ServiceSpec 内嵌 Annotations——marshal 后 Labels 平铺
	// 在顶层，无 "Annotations" 包裹层）。
	if labels, ok := m["Labels"].(map[string]any); ok {
		delete(labels, labelGeneration)
		normalizeRestartSeqLabel(labels)
	}
	delete(m, "RollbackConfig")
	if uc, ok := m["UpdateConfig"].(map[string]any); ok {
		delete(uc, "Monitor")
		delete(uc, "MaxFailureRatio")
	}
	// EndpointSpec.Mode：服务端对空物化为 vip（防御性双侧剥离——endpointSpec
	// 已显式 vip，此处兜住历史行与未来方言差）。
	if es, ok := m["EndpointSpec"].(map[string]any); ok {
		if mode, _ := es["Mode"].(string); mode == "" || mode == "vip" {
			delete(es, "Mode")
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

// normalizeRestartSeqLabel 把重启序数标签归一到缺失语义（ADR-0083 步骤
// 4：缺失 ≡ 0——平台只铸非零序数，"0" 值只可能来自环外/历史形态；比对
// 前剥除使「存量无标签 × 期望 0」与「带 0 标签 × 期望 0」同判 skip，
// 升级首拍零滚动的 canonical 半边）。
func normalizeRestartSeqLabel(labels map[string]any) {
	if v, ok := labels[labelRestartSeq].(string); ok && v == "0" {
		delete(labels, labelRestartSeq)
	}
}

// canonicalContentJSON 在 canonicalSpecJSON 之上再剥离副本数：等价 =
// 「内容等价且仅副本数不同」——Ensure 的 scale-only 决策依据（ADR-0064：
// 纯副本数变化走 swarm scale 语义，保留在服任务模板即零任务替换）。
func canonicalContentJSON(spec swarm.ServiceSpec) string {
	base := canonicalSpecJSON(spec)
	if base == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		return ""
	}
	if mode, ok := m["Mode"].(map[string]any); ok {
		if rep, ok := mode["Replicated"].(map[string]any); ok {
			delete(rep, "Replicas")
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

// serviceWireGen 读现存服务的代戳（观测归因真源——服务级 Labels 与任务
// 模板 Labels 由本批约定恒同移，读服务级即可）。无戳（0）= 非平台管
// 辖形态或历史行，调用方按缺席处理。
func serviceWireGen(svc swarm.Service) uint64 {
	gen, _ := strconv.ParseUint(svc.Spec.Labels[labelGeneration], 10, 64)
	return gen
}

// netResolve 是网络引用的 per-Ensure 解析备忘值（C19-3）。
type netResolve struct {
	id      string // 解析到的 swarm 网络 ID
	missing bool   // 404 哨兵：载体不存在，名字原样保留
}

// resolveNetworkTargets 把 TaskTemplate.Networks[].Target 从平台网络载体
// 名解析为 swarm 网络 ID——服务端对 create/update 的名字输入会改写为 ID，
// 先行解析使发送形态与回读形态一致（no-op 比对的前提）。
//
// resolved 是 per-Ensure 备忘（键=载体名）：同拍内多 Workload 引用同名网
// 络只探一次 daemon，Ensure 序结束随局部变量废弃。解析分诊（Q-20，
// network.go ensureNetworks 同款先例）：NotFound → 载体缺失，名字原样保
// 留交给服务端名字级错误如实暴露（ensureNetworks 已 create-or-get，404 =
// 引用与载体脱节的 bug 形态）；其余错误上抛带原因——inspect 失败 ≠ 不存
// 在，吞掉会把权限/连接类故障伪装成"无需解析"，推迟到服务端误导性报错
// （修复前任何错误都 continue 吞掉）。
func (p *Provider) resolveNetworkTargets(ctx context.Context, spec swarm.ServiceSpec, resolved map[string]netResolve) (swarm.ServiceSpec, error) {
	nets := spec.TaskTemplate.Networks
	for i := range nets {
		name := nets[i].Target
		res, ok := resolved[name]
		if !ok {
			n, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
			switch {
			case err == nil:
				res = netResolve{id: n.Network.ID}
			case isNotFound(err):
				res = netResolve{missing: true}
			default:
				return swarm.ServiceSpec{}, fmt.Errorf("resolve network %s: %w", name, err)
			}
			resolved[name] = res
		}
		if res.missing {
			continue // 保留名字原样：服务端给出名字级错误（404 语义不变）
		}
		nets[i].Target = res.id
	}
	return spec, nil
}

// Remove 拆除隔离域内全部载体（幂等）。
func (p *Provider) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return fmt.Errorf("swarm remove %s: %w", ns, err)
	}
	for _, svc := range services {
		if _, err := p.cli.ServiceRemove(ctx, svc.ID, client.ServiceRemoveOptions{}); err != nil && !isNotFound(err) {
			return fmt.Errorf("swarm remove %s: %w", ns, err)
		}
		p.forgetLastIssued(svc.Spec.Name) // 服务删除即清账（断路器账本不复活）
	}
	return nil
}

// Addresses 返回隔离域可达地址（overlay VIP / 服务 DNS 名；平台无关形态）。
// 端点端口取自期望集的声明端口（engine 注入——载体原生不承载声明端口，
// 期望集取代端口 label 平行编码，架构评审第二轮候选 7）：服务按平台
// Workload ID 标记（labelWorkload——Provider 搬运平台标记的本职）匹配
// 期望集成员，未匹配的服务不计入端点。ID 匹配（而非 process 名）是
// 蓝绿双代窗的消歧锚（ADR-0048 决策 1.4：resolveBackend 消费代次化地址
// ——两代同 process 各自独立载体，engine 以期望集圈定服务代）；单代
// 形态 ID↔process 一一对应，语义不变。
func (p *Provider) Addresses(ctx context.Context, ns capability.NamespaceRef, expected []capability.Workload) ([]capability.Endpoint, error) {
	byID := make(map[string]capability.Workload, len(expected))
	for _, w := range expected {
		byID[w.ID] = w
	}
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("swarm addresses %s: %w", ns, err)
	}
	var endpoints []capability.Endpoint
	for _, svc := range services {
		w, ok := byID[svc.Spec.Labels[labelWorkload]]
		if !ok {
			continue // 非期望集成员（stale 载体/期望未重放）不计入
		}
		addr := svc.Spec.Name // overlay DNS 名（网络内可解析）
		for _, vip := range svc.Endpoint.VirtualIPs {
			if vip.Addr.IsValid() {
				addr = vip.Addr.Addr().String()
				break
			}
		}
		for _, port := range w.Ports {
			endpoints = append(endpoints, capability.Endpoint{
				Addr:    addr,
				Process: w.Process,
				Port:    port.Port,
			})
		}
	}
	return endpoints, nil
}

// DescribeCluster 返回集群观测视图（节点缓存；权威归属判定永远查平台表）。
func (p *Provider) DescribeCluster(ctx context.Context) (capability.ClusterView, error) {
	var view capability.ClusterView
	if err := p.anchorNodes(ctx, nil); err != nil {
		// 锚定失败不阻断观测（下次扫描重试）；错误如实带出。
		return view, err
	}
	list, err := p.cli.NodeList(ctx, client.NodeListOptions{})
	if err != nil {
		return view, fmt.Errorf("swarm describe cluster: %w", err)
	}
	for _, node := range list.Items {
		view.Nodes = append(view.Nodes, capability.NodeView{
			NodeID:    node.Spec.Labels[labelNodeID],
			CarrierID: node.ID,
			Hostname:  node.Description.Hostname,
			// Addr 是 advertise 地址观测（受管采集端点寻址锚，ADR-0041）。
			Addr:      node.Status.Addr,
			Role:      string(node.Spec.Role),
			Available: node.Status.State == swarm.NodeStateReady,
			Labels:    node.Spec.Labels,
		})
	}
	return view, nil
}

// Enrollment 生成节点加入材料（worker 加入命令；节点零平台安装物）。
// rotate=true 先作废全部现有 join token（SwarmUpdate 带 token 轮换旗标）
// 再取新材料（C3：泄漏处置路径）。管理面地址取可达 manager 的广播地址
// （N1 HA 扩容再扩展 manager 命令）。中继材料不在本动词（H10）：join
// token 不再承担 relay 鉴权，节点中继凭证按已锚定节点定向铸造，见
// NodeRelayCommand（RuntimeRelayMaterial 子面）。
func (p *Provider) Enrollment(ctx context.Context, rotate bool, o capability.EnrollmentOptions) (capability.EnrollKit, error) {
	if rotate {
		cur, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
		if err != nil {
			return capability.EnrollKit{}, fmt.Errorf("swarm rotate: inspect: %w", err)
		}
		if _, err := p.cli.SwarmUpdate(ctx, client.SwarmUpdateOptions{
			Version:            cur.Swarm.Version,
			Spec:               cur.Swarm.Spec,
			RotateWorkerToken:  true,
			RotateManagerToken: true,
		}); err != nil {
			return capability.EnrollKit{}, fmt.Errorf("swarm rotate: update: %w", err)
		}
	}
	inspect, err := p.cli.SwarmInspect(ctx, client.SwarmInspectOptions{})
	if err != nil {
		return capability.EnrollKit{}, fmt.Errorf("swarm enrollment: %w", err)
	}
	managerAddr, err := p.managerAddr(ctx)
	if err != nil {
		return capability.EnrollKit{}, fmt.Errorf("swarm enrollment: %w", err)
	}
	workerToken := inspect.Swarm.JoinTokens.Worker
	managerToken := inspect.Swarm.JoinTokens.Manager
	return capability.EnrollKit{
		Command:        fmt.Sprintf("docker swarm join --token %s %s", workerToken, managerAddr),
		ManagerCommand: fmt.Sprintf("docker swarm join --token %s %s", managerToken, managerAddr),
	}, nil
}

// managerAddr 取可达 manager 的广播地址（首个带 ManagerStatus.Addr 的
// 节点；Addr 形如 10.0.0.3:2377）。nodeList 注入缝（p.nodes）供 hermetic
// 单测。
func (p *Provider) managerAddr(ctx context.Context) (string, error) {
	list, err := p.nodes(ctx)
	if err != nil {
		return "", fmt.Errorf("manager address: %w", err)
	}
	for _, node := range list.Items {
		if node.ManagerStatus != nil && node.ManagerStatus.Addr != "" {
			return node.ManagerStatus.Addr, nil
		}
	}
	return "", fmt.Errorf("no reachable manager advertise address")
}

// NodeRelayCommand 实现 RuntimeRelayMaterial（H10 §4.7）：按已锚定节点
// 渲染中继装载脚本——脚本只含该节点个体凭证（平台铸造的明文），不含
// join token。本面不持久化、不读库（sha256 由调用方落
// node_relay_credentials）。
func (p *Provider) NodeRelayCommand(ctx context.Context, credential string, o capability.EnrollmentOptions) (string, error) {
	if o.GatewayPort == "" {
		return "", fmt.Errorf("relay material needs the gateway port")
	}
	addr, err := p.managerAddr(ctx)
	if err != nil {
		return "", fmt.Errorf("swarm relay material: %w", err)
	}
	return nodeRelayScript("http://"+managerHost(addr)+":"+o.GatewayPort, credential), nil
}

// managerHost 从 manager 广播地址取主机段（Addr 形如 10.0.0.3:2377）。
func managerHost(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// nodeRelayScript 生成节点中继装载脚本（幂等：先清旧中继容器再
// 重建；curl/wget 双通道——载体镜像 busybox:1.37 与 e2e 预载清单同钉版）。
// 下载/拨号凭证 = 节点个体 relay 凭证（H10：不再含 join token——脚本
// 泄漏只影响单节点 relay 身份，不是集群成员权）。
// 形态约束：纯单引号（JSON 转义恒等——消费方可原样 sed 抽取执行，e2e
// 与 runbook 同口径）；mktemp 路径无空格，免引号展开安全。
func nodeRelayScript(baseURL, credential string) string {
	argv := fmt.Sprintf("chmod +x /tmp/fleetlyd && exec /tmp/fleetlyd relay --manager %[1]s --relay-credential %[2]s", baseURL, credential)
	return fmt.Sprintf(
		`t=$(mktemp) && { curl -sfL -H 'Authorization: Bearer %[2]s' -o $t %[1]s/v1/platform/binary || wget -q --header 'Authorization: Bearer %[2]s' -O $t %[1]s/v1/platform/binary; } && `+
			`docker rm -f fleetly-relay >/dev/null 2>&1; `+
			`c=$(docker create --name fleetly-relay --restart unless-stopped -v /var/run/docker.sock:/var/run/docker.sock --entrypoint sh %[3]s -c '%[4]s') && `+
			`docker cp $t $c:/tmp/fleetlyd && docker start $c && rm -f $t`,
		baseURL, credential, relayCarrierImage, argv)
}

// relayCarrierImage 是节点代理的载体镜像（钉版 tag；e2e 预载清单同源）。
const relayCarrierImage = "busybox:1.37"

// InspectWorkloads 实现 RuntimeInspector 子面（ADR-0022 spec 对照 drift）：
// ServiceList 快照 + 标记还原平台身份 + spec 读取（镜像/副本）。状态取
// 期望副本面（观测状态以 Watch 流/任务轮询为权威，此处仅 spec 对照用）。
func (p *Provider) InspectWorkloads(ctx context.Context, ns capability.NamespaceRef) ([]capability.WorkloadObservation, error) {
	services, err := p.listNsServices(ctx, ns)
	if err != nil {
		return nil, fmt.Errorf("swarm inspect %s: %w", ns, err)
	}
	out := make([]capability.WorkloadObservation, 0, len(services))
	for _, svc := range services {
		labels := svc.Spec.Labels
		if labels[labelManaged] != "true" {
			continue
		}
		gen, _ := strconv.ParseUint(labels[labelGeneration], 10, 64)
		obs := capability.WorkloadObservation{
			WorkloadID: labels[labelWorkload],
			Generation: capability.Generation(gen),
			Image:      svc.Spec.TaskTemplate.ContainerSpec.Image,
			// 入口覆盖命令回读（ADR-0022 spec 对照：人工 docker service
			// update --command 改载体须出 drift）。
			Command:  svc.Spec.TaskTemplate.ContainerSpec.Command,
			Replicas: 1,
			State:    capability.WorkloadRunning,
		}
		if svc.Spec.Mode.Replicated != nil && svc.Spec.Mode.Replicated.Replicas != nil {
			obs.Replicas = int64(*svc.Spec.Mode.Replicated.Replicas) //nolint:gosec // 副本计数域内（swarm 上限远小于 2^63）
		}
		// 滚动停摆透传（runbook 记录·二十七）：ServiceUpdate 被接受后
		// 滚动可能因新 task 失败进入 paused（translate 层 FailureAction=
		// pause + start-first 旧 task 不退役）——spec 已是新版、spec 对照
		// 无感，停摆事实只能从 UpdateStatus 读出（drift 面发事件用）。
		if svc.UpdateStatus != nil && svc.UpdateStatus.State == swarm.UpdateStatePaused {
			obs.RolloutStalled = true
			obs.RolloutDetail = svc.UpdateStatus.Message
		}
		if obs.WorkloadID != "" {
			out = append(out, obs)
		}
	}
	return out, nil
}

// listNsServices 列出隔离域内 fleetly 管辖的服务。
func (p *Provider) listNsServices(ctx context.Context, ns capability.NamespaceRef) ([]swarm.Service, error) {
	filters := client.Filters{}.Add("label", labelSelectorArgs(ns)...)
	res, err := p.cli.ServiceList(ctx, client.ServiceListOptions{Filters: filters})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// labelSelectorArgs 生成 label 全等过滤参数（label=k=v 形态）。
func labelSelectorArgs(ns capability.NamespaceRef) []string {
	args := make([]string, 0, 4)
	for k, v := range nsSelector(ns) {
		args = append(args, k+"="+v)
	}
	sort.Strings(args)
	return args
}

// registryAuthFor 解析镜像引用对应仓库的拉取凭证并编码（无匹配凭证返回
// 空串 = 匿名拉取；凭证不落载体，ADR-0014）。
func (p *Provider) registryAuthFor(ctx context.Context, image string, m capability.Materials) (string, error) {
	if len(m.RegistryAuth) == 0 {
		return "", nil
	}
	host := capability.ImageRegistryHost(image)
	cred, ok := m.RegistryAuth[host]
	if !ok {
		return "", nil
	}
	return capability.EncodeRegistryAuth(cred)
}

// imageRegistryHost 与 encodeRegistryAuth 已单源化至 capability
//（2026-10-03 架构评审候选 5：engine/swarm/builders 三面各持一份的收口）。

// stripCIDRPrefix 已随 netip.Prefix 迁移退役（VIP 直接取 Addr().String()）。

// isNotFound 报告错误是否对象不存在（幂等 Remove/收敛用）。
func isNotFound(err error) bool {
	return errdefs.IsNotFound(err)
}

# ADR-0083: 声明式重启序数 restart_seq——重启双面立法

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-18 | 设计母本 `docs/design/2026-10-18-declarative-model.md` §5（唯一规格源）；**推翻 ADR-0069 决策 6**（「App 级整体重启不做专用动词」——CLI `apps restart` 缺省走声明面即 App 级声明式重启，2026-10-18 用户裁决）；**部分推翻 ADR-0069 决策 1**（「重启不进部署叙事」——声明面重启入 lineage，动作面零 lineage 不动）；ADR-0064（内容锚定等价——seq 变更即内容变更，走 full CAS update 滚动替换）；ADR-0030（preDeploy 游标与 PreDeployDone）；ADR-0002（Spec IR 与 Revision 冻结）；ADR-0071/0072（投影/渲染单源与冻结咽喉）；ADR-0048（蓝绿双代窗）；ADR-0052/0053（k3s 账本 canonical 键形态） |

## 背景

ADR-0069 补了排障动线的动作半边：Process 级 restart = runtime-only 强制重排、零 lineage。但 P3 之问仍在——重启这类操作在声明式模型中如何建模？「想要进程重启」本身可以是一种期望态吗？ansible 的参考价值边界在哪？

答案：**双面**。声明面给 App 域补上「重启」的期望态表达（`restart_seq` 落 Spec、经部署管线滚动生效）；动作面保留 ADR-0069 零 lineage 强制重排作逃逸通道。ansible 只取三样——幂等模块律、check-mode（→ Wait/观测面既有语义）、handler-notify（→ 将来配置面的编排语义另议）——不取 playbook 过程序，更不取 `state: restarted` 这类过渡冒充状态（过渡是 ACTION，ADR-0081）。

## 决策

### 1. 机制链（八步）

1. **`ProcessSpec.restart_seq` 字段**（uint32、非负；命名已避禁词）。**单调性是调用方约定，平台保证最终态 = 最后执行的声明**——受理不加执法（并发受理下的序数警告语义模糊，文档化即可；对抗审查 B12 裁决）。
2. **SubmitSpec 冻结覆盖**：digest 覆盖 restart_seq——seq 变更即新 Revision（内容寻址去重语义不变）。
3. **投影**：`projectRevision` 投影 → `capability.Workload.RestartSeq`（新增 typed 字段；Workload 无 Labels/Annotations 泛型槽，窄面端口演进）。
4. **载体标签**：swarm 铸 `fleetly.restart_seq` 载体标签。**seq=0 不铸标签、canonical 对照对缺失标签按 0 归一**——存量载体升级首拍零滚动（bb5259d「label 消失 + 方言归一 → 全量滚替 → torchwood-pg WAL 损坏」P0 前车，runbook:113-121）；k3s 账本 canonical key 同批纳入（动工核在役形态）。
5. **内容等价三路**：seq 变 → full CAS update → 滚动替换 = 声明式滚动重启（k8s rollout restart 同构，provider 语义零新增）；seq 未变 → no-op（账本冷启动由既有等价 JSON 兜底比对，已验证成立）。
6. **seq-only diff 规则（判定延迟到执行时）**：驱动器拾取时对**实际最新 succeeded 基线**重算 To/From 树 diff，diff ⊆ restart_seq 才标 `kind=restart` + `PreDeploy=PreDeployDone`（镜像 rollback 处理，`admission.go:214-219`；树 diff 助手下沉 `domain/shared/spec`）。受理时判定的 From 锚可能已被更早排队的部署推进——受理时判定会让「回退中间变更却免跑迁移」的组合静默通过（对抗审查 B8）。
7. **审计入 lineage**：Deployment 行 + kind=restart——与 ADR-0069 的张力显式记录：决策 1「重启不入部署叙事」**部分推翻**（声明面入叙事；动作面零 lineage 不动）；决策 6「App 级整体重启动词不做」**推翻**（CLI 缺省面即 App 级声明式重启，见 §3）。
8. **迁移不得因重启重跑**：PreDeployDone 语义即此（ADR-0030 决策 5 同款）。

### 2. 边界裁决

- **Rollback 回落低 seq 显式豁免单调约定**（操作者意图优先——回滚本就是声明性回归，ADR-0081）。
- **蓝绿策略进程按 BG 舞步重启**（双代并存窗语义不因 restart_seq 改变）。
- **compareSpecs 不对照 restart_seq**：意图字段非可观测态——同一字段在内容等价（收敛面）与 spec 对照（漂移面）两比对器的分际是**显式规则**，两个守卫测试以此为共同法源。
- **compose 方言 v1 不暴露**：spec 级字段，入口 = spec_file / API / console Spec 编辑器 / 将来配置面。
- **Component restart 维持纯动作**：受管域无用户 spec 面（ADR-0081 已钉 RestartComponent = ACTION）。

### 3. 双面操作者契约（2026-10-18 用户裁决）

`fleetly apps restart` **缺省走声明面**：对全部进程 bump restart_seq → 正常部署管线（有 lineage、跳迁移、内容等价语义护航）；`--runtime` 旗标走 ADR-0069 零 lineage 动作面作逃逸。两动词并存、语义各自诚实：声明面是「我要求以新序数重跑进程」的期望态变更，动作面是「立即就地重排载体」的过渡动作。

## 后果

- 「重启」成为可声明、可回滚、可审计的期望态变更：seq 悬挂在 Revision 上，回滚连 seq 一起回——声明幂等性（未变重放零滚动）覆盖重启。
- 升级首拍零滚动由 seq=0 归一裁决保证：存量载体无标签 ≡ 期望 0，e2e:upgrade 与 bb5259d 前车双保险；若现役载体带非零 seq 形态则一次性收敛为预期红，需人工判读（收尾批预记两分支）。
- ADR-0069 决策 2/3/4/5（子面复用、存在性先验、冻结归族、无 audit/event）对动作面继续成立；声明面有自己的审计行（kind=restart）与冻结执法（Deploy 同款，变更型动词）。
- console Spec 编辑器与 CLI 双入口同批换装（dist 是提交进仓的生成物，`console:gen && console:build` 同 commit）。

## 验收锚

- [ ] contentequiv 四用例（swarm + k3s 双侧）：seq 变 → update / 同 seq → skip / 缺失 → create / **存量无标签 × 期望 0 → skip（升级零滚）**
- [ ] seq-only 执行时判定钉测：From 锚已被更早部署推进的场景，执行时重算后正确判向（免迁移判定不静默通过）
- [ ] 迁移不因重启重跑钉测（kind=restart + PreDeployDone）
- [ ] compareSpecs 不对照 restart_seq 钉测（漂移面零假阳性，与内容等价守卫共享法源注释）
- [ ] CLI golden 族：`apps restart` 双形态（缺省声明面 / --runtime 动作面）+ `selfdescription.golden.json` + schema/explain golden + `revisions get` / `apps workloads` seq 非零夹具
- [ ] `console:gen && console:build` dist 同 commit；console Spec 编辑器对 restart_seq 的控件/校验影响评估在案
- [ ] e2e:upgrade 升级首拍零滚断言（预记两分支判读口径）
- [ ] skills restart 双面指引同批（ADR-0031 能力面）

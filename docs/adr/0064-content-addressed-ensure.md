# ADR-0064: 内容锚定等价收敛（Content-Addressed Ensure）

日期：2026-10-11；状态：已采纳（staging 2026-10-11 双门超时事故驱动，同批落地）。

## 背景

staging 实证事故（runbook 记录·三十七，2026-10-11）：用户经 Processes tab 调整**单个 process 的副本数**，引发**全部六服务任务滚动替换**——滚动 churn 窗内 swarm DNS 的 `redis` 别名断档，依赖 redis 的 server 新任务 boot 门秒崩（exit 1），swarm `FailureAction=pause` 停摆，L1 健康门 120s 超时；自动回滚走 `NextGeneration` 重放、全员再替换，撞同一堵墙——回滚门也超时，双门失败终态。

根因是 **Generation 戳的职责耦合**：`fleetly.generation` 标签同时承担「观测归因锚」（L1/drift/看门狗按它把运行任务归因到部署代）与「spec 内容成分」（进任务模板 Labels，参与 Ensure 的等价比对）。每次部署经 `stampDeploymentGenerations` 全员盖新代戳（ADR-0048 决策 1.2），跨代等价判定恒失配——即使内容零变化也全员 ServiceUpdate、任务模板变化即全部滚动替换。replicas-only 变更本可走 scale 语义（不换任务），被代戳拖成 full replace；停摆（paused）后平台对等价 spec 短路、永不 resume，只能人工 `docker service update --force` 解卡（N/1 收案与本案两次致害）。

## 决策

1. **内容等价判定（modulo 代戳）**：等价比对剥离 `fleetly.generation`——swarm `canonicalSpecJSON` 剥服务级 Labels（ServiceSpec 内嵌 Annotations，marshal 后平铺顶层）与任务模板 ContainerSpec.Labels 两处；k3s 账本 A 键升 modulo-gen canonical（对象 meta 与 pod 模板两层）。等价语义从「字节等价」升为「内容等价」：跨代未变载体零触碰，服务/对象保留旧代戳。
2. **Ensure 三分决策**：内容等价且副本同 → **跳过**（零写）；内容等价仅副本异 → **scale-only**（swarm：在服任务模板与服务级标签原样回发——模板未变即存量任务零替换；k3s：写代钉 wire-gen——pod 模板字节不动，仅 Deployment 副本数推进）；内容异 → **滚动替换**（新代戳只随内容落地）。**代戳与内容恒同移**：观测归因锚只在内容变更时推进。
3. **EnsureReport 下发回执**：`capability.Runtime.Ensure` 签名改 `(EnsureReport, error)`——报告 = 逐载体实际 wire 代次（跳过/scale = 现存代戳；滚动/create = 调用 gen；`WireGen` 回落序：回执 → 载体自身戳（BG 双代窗新代成员）→ 调用 gen）。跳过信息此前埋在 Provider 内不上抛，引擎无法知道「哪个载体实际在哪个代」。
4. **引擎归因回路**：`materialize`/`blueGreenWindowMaterialize` 在 Ensure 后按报告覆写逐载体 Generation 再 `recordEnsured`——L1 门 `readyGen` 按 gen 过滤天然只圈真下发新代的成员；未变载体不再以部署 gen 记账等一个永远不会到来的 running@部署gen。**空集 = vacuously ready**（纯 scale/全跳过是合法形态；调用序保证查询前 materialize 已入账，空集不可能是缓存未建）。回滚零特改：`NextGeneration` 保留（行序审计），未变载体自动跳过——回滚从「全员再替换」变「最小动作」，回滚门可收敛。
5. **滚动停摆单次自动 resume**（swarm）：Ensure 的两个跳过分支检测「期望内容等价 + UpdateStatus=paused」（= 平台自己的更新停摆）→ `TaskTemplate.ForceUpdate++` 重发一次（`--force` 等价语义，restart 面同款惯用）；5min 时间窗护栏（> L1 120s，单部署窗恰一拍，boot 崩溃类应用不 flap），窗内不重复，重启即冷再许一次。仍停摆由 L1/回滚门超时接管。k3s 无 pause 形态（Deployment 控制器自带重试），不适用。
6. **k3s 免服务端比对**：k8s 服务端对入站写自带 defaulting 归一，客户端深比对会因 defaulted 字段恒不等——k3s 用**账本双键**（A = modulo-gen canonical；B = 再剥副本数）+ **写代钉住**（账本热 = issuedGen 旧值；冷 = 读现存对象 meta 代戳）实现同款三义：跨代未变零写、replicas-only 模板字节不动、冷账本重放钉服务端代戳（内容未变则服务端归一后 no-op——重启零滚动）。`issuedGen` 收紧为 wire-gen 真源（Watch drift 对照与载体模板代戳一致，跳过载体不再假 drift）。

## 不变量

- 代戳与内容恒同移：服务级与任务模板两处锚一致；未变载体跨任意多代保留其下发代戳。
- 观测归因三方一致：pod/任务标签 = EnsureReport = 引擎 ensuredGen。
- 纯 scale 部署的 L1 = vacuous pass；部署行 Generation 语义不变（仍是单调审计序）。
- 同代幂等重放语义不变（C19-1 断路器 / putObject 账本门照常）。

## Consequences

- 单 process 副本调整 = 仅该服务动作（纯扩零替换），其余服务零触碰——DNS 断档放大器在 scale 场景消失；显式内容变更的滚动窗 churn 属 swarm/k8s 生态常态。
- 回滚成本与失败部署的爆炸半径解耦：回滚只回放差异，不再全员替换。
- 停损能自愈（单次 resume）；持续失败由门超时收敛，回滚廉价后闭环。
- 受管域/Database 域同受 modulo-gen 收益（等价收敛更稳）；ADR-0045 模板 digest 变更仍强制滚动的语义保留（内容变更路径不变）。
- 修订 ADR-0048 决策 1.2 的「全员逐载体 gen 锚」表述：gen 锚仍是逐载体的，但只在内容变更时推进（本 ADR 决策 1/2）。

### 诚实边界

- **scale-only 新任务的观测不进门**：新增任务带旧代戳（模板保序的必然），L1/L2 对它不咬合——swarm RestartPolicy 与事件面（exit code）兜底；副本级就绪门是后续批候选（ADR-0025 Instance 字段已在）。
- **跨进程依赖序/DNS churn**：显式变更的全员滚动窗内别名断档仍在（torchwood boot 门秒崩是应用侧韧性——对 redis 不可达的重试窗属应用责任；平台观测面已暴露 rollout_stalled + exit code）。依赖图模型超射程挂账。
- **k3s 冷账本 + 内容已变的窄边**：钉服务端代戳后内容实际已变 → 该载体滚动但代戳停旧值——归因自洽（三方一致），L1 按报告圈集不阻塞；spec drift 面照常纠错。
- **swarm resume 对 boot 崩溃应用**：每 5min 至多一次重试窗内的 flap——有界，事件面可见。

## 验收锚

- [x] swarm：跨代未变载体零 update（fakeDaemon 计数断言，账本短路与等价回读两路径）——`TestEnsureContentEquivalenceSkipsUnchangedAcrossGen`。
- [x] swarm：replicas-only 恰一次 update 且任务模板代戳保留（零任务替换的 spec 侧证据）——`TestEnsureReplicasOnlyScalesPreservingTemplate`。
- [x] swarm：真内容变更滚动照常、双锚推进——`TestEnsureContentChangeRollsWithNewGen`。
- [x] swarm：停摆 resume 恰一次 + ForceUpdate 落账 + 窗内护栏不再发——`TestEnsureResumesStalledRolloutOnce`。
- [x] 引擎：跨代部署未变载体按 wire-gen 记账、L1 只等已变载体——`TestWireGenAttributionUnchangedWorkloadsDontBlockL1`。
- [x] 引擎：纯 scale 部署一拍直落 observing → succeeded（空集 vacuous pass）——`TestPureScaleDeploymentPassesL1`。
- [x] k3s：跨代零写（ResourceVersion 不动）/ replicas-only 模板字节稳定 / 真变更双锚推进 / 冷账本钉服务端代戳——`runtime_contentequiv_test.go` 四锚。
- [x] e2e：dind-smoke 场景 2b 真 swarm 断言——replicas-only 部署目标服务 2 Running + 0 Shutdown、未触碰服务 1 Running + 0 Shutdown（旧行为必红）。
- [x] staging 活体复现用例回归（cd052e4-adr64，2026-10-11 14:21 实录）：server 副本 1→2 → 旧任务在服 + 新任务加入（**纯扩零替换**，两任务 gen 标签皆 13——模板钉住）；其余五服务任务行零变化；部署 10s 过 L1（空集 vacuous）→ 60s 观察窗 → succeeded 零错误。旧代码残留（5/1、6/1、2/1 paused + drift 风暴）在新 daemon 3 分钟内自动收敛（modulo-skip + paused resume），风暴止息。〔runbook 记录·四十〕

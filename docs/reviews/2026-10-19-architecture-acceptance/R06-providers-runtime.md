# R06 Runtime Providers：swarm 与 k3s 评审报告

> 评审基线：`PROFILE.md`（2026-10-19，HEAD `35257f18`）+ `MODULES.md` §R06。
> 全程只读；本机无法跑 e2e（Docker daemon 不可达，见 PROFILE §1.1），真机结论以代码 + RBAC/k8s 语义推演并如实标注。

## 1. 覆盖清单

### 完整读过（生产文件，逐文件全文）

**swarm（15/15 生产文件，约 4.2k 行）**
`provider.go`、`runtime.go`、`translate.go`、`watch.go`、`exec.go`、`noderelay.go`、`logs.go`、`network.go`、`maintenance.go`、`orphan.go`、`orphan_volume.go`、`admin.go`、`utility.go`、`nodecontainers.go`、`restart.go`

**k3s（15/15 生产文件，约 4.0k 行）**
`provider.go`、`runtime.go`、`translate.go`、`netisolate.go`、`network.go`、`rbac.go`、`exec.go`、`noderelay.go`、`logs.go`、`inspector.go`、`admin.go`、`nodecontainers.go`、`utility.go`、`hygiene.go`、`restart.go`

**契约与调用方（排除误读用）**
`internal/domain/shared/capability/{runtime.go,exec.go,faces.go,nodeinspect.go}`
engine 消费面：`observ.go`、`observdomain.go`、`drift.go`、`converge.go`、`nodes.go`、`teardown.go`、`logging.go`、`materials.go`、`projection.go`（§Project/ProjectTask）、`managed.go`（受管收敛/观测播种段）、`processrestart.go`、`componentrestart.go`
装配面：`internal/assembly/{provides.go(SetNodeIDMinter 段),gateway_exec.go}`
材料来源：`internal/application/composeintake/intake.go`（configs → config_mounts 段）、`internal/infrastructure/providers/victorialogs/provider.go`（LogFrame 消费）
文档：ADR-0052 全文；ADR-0014/0046/0053/0055/0064/0067/0083 按需定位段落。

### 抽样读过

- swarm 测试：`runtime_contentequiv_test.go`、`runtime_test.go`（InspectWorkloads/停摆段）、`fakedaemon_test.go`（结构）、`translate_test.go`（用例清单）、`exec_script_test.go`
- k3s 测试：`runtime_contentequiv_test.go`、`runtime_ensure_test.go`（Ensure/Inspect/headless/utility 段）、`runtime_watch_test.go`、`rbac_test.go`、`netisolate_test.go`（形状漂移收敛段）、`exec_test.go`（用例清单）、`translate_test.go`（用例清单）
- e2e：`e2e/dind-k3s.sh`（RBAC can-i / netpol / egress 段）、`e2e/dind-k3s-two-node.sh`（relay/exec/卷钉住段）、`e2e/dind-smoke.sh`（exec/内容等价段）、`e2e/dind-runtimeswitch.sh`（k3s 段 grep）
- 实测：`go test -race -count=1 ./internal/infrastructure/providers/{swarm,k3s}/...` → 双包 **ok**（无竞态）；`go test` 常规亦绿。

### 未覆盖/无法覆盖

- 真机/ e2e 未复跑（Docker daemon 不可达，PROFILE §1.1 明示）；所有"真机会 403/丢数据"结论为代码 + 平台语义推演，已逐条标注置信度。
- k3s `hygiene_test.go`、`utility_test.go`、`exec_test.go` 细节，swarm `logs_test.go`/`watch_resume_test.go`/`maintenance_test.go`/`admin_test.go`/`orphan*_test.go` 未逐行精读（只读用例清单与关键断言）。
- swarm/k3s 的 `fakedaemon_test.go` 路由实现、e2e 脚本全文未逐行读。
- 未验证 docker daemon 侧 node 事件是否携带 labels（L2 的待复核点，模块缓存无 swarmkit 源码）。

## 2. 模块画像与边界

- **职责**：两者都是 `capability.Runtime` 的实现（核心 6 方法 Ensure/Remove/Watch/Addresses/DescribeCluster/Enrollment + 可选子面 Logs/Admin/Inspector/Hygiene/Utility/NetworkMaintenance/Exec/NodeInspect），把平台无关 IR（Workload/Materials/NamespaceRef）翻译为编排器原语：swarm = Service/Secret/Config/Network + docker exec；k3s = Namespace/Deployment/DaemonSet/Pod/Service/PVC/NetworkPolicy/Secret/ConfigMap + apiserver SPDY exec。载体命名与 `fleetly.*` 标记全部 Provider 私有，平台不解析（`swarm/translate.go:19-60`、`k3s/translate.go:21-78`）。
- **依赖方向**：`providers/{swarm,k3s}` 只 import `domain/shared/capability` + 编排器 SDK（moby/k8s），互不 import，无 engine/application 依赖；编译期契约断言在 `provider.go`。与架构 §2/§11、ADR-0075 一致（亲验 import 块）。
- **双 Provider 语义面**：swarm 弱隔离（无 NetworkPolicy，Notes 声明 `swarm/provider.go:123-129`）；k3s 强隔离（网络成员资格 netpol + egress deny + peer grant，`k3s/netisolate.go` + `k3s/translate.go:689-736`）。k3s 故意不实现 NetworkMaintenance（ADR-0053 决策 2），Describe Notes 诚实声明（`k3s/provider.go:164-174`）；swarm 无 peer grant 概念（happy no-op，`swarm/orphan.go:112-118`）。
- **与 ADR 声明的偏差**（详见 R06-L3/M1）：ADR-0052 决策 3 映射表写 `Healthcheck → readiness+liveness probe`，实现只配 readiness（`k3s/translate.go:309-315` 注释说明有意偏离）；映射表未列 `Materials.ConfigFiles`/`ConfigMounts`，而 Provider 有一半实现（建 ConfigMap 但不挂载）。

## 3. 优点（带证据）

1. **ADR-0083 restart_seq 的双 Provider 内容等价是"测试钉死"的**：swarm 在 canonical 比对中把 `"0"` 值标签按缺失归一（`swarm/runtime.go:438-446`），k3s 在 modulo-gen canonical 中做同款归一（`k3s/runtime.go:962-980`），两侧各有一组四用例测试（seq 变滚动 / 同 seq skip / 缺失 create / 存量无标签×期望0 skip）——`swarm/runtime_contentequiv_test.go:160-225`、`k3s/runtime_contentequiv_test.go:115-175`。这是 ADR-0083"存量升级首拍零滚"承诺的直接承载，且覆盖了 bb5259d WAL 事故的回归面，质量高于本仓平均。
2. **swarm 稳态短路双闸设计成熟**：内存账本 no-op 断路器 + `serviceSpecEqual` 深比对（inspect 回读 canonical 化、剥离服务端归一化产物与代戳），失败整拍作废账本、scale-only 分支保留在服任务模板（`swarm/runtime.go:166-215`、`:354-470`）。staging 自激环（20s 49 次版本推进）被结构性堵住，且测试覆盖账本短路/冷账本/scale-only/内容变更四路径。
3. **k3s 成员资格隔离的收敛判据经得起推敲**：policy 删除判据 = 本拍意图键 ∪ 活 pod 成员键（`k3s/netisolate.go:246-271`），既防"新部署批内自噬"又防"跨域误删"（注释记录两次真机实锤）；`putNetpol` 做语义等价的 create-or-update 幂等（`k3s/netisolate.go:159-184`），且有形状漂移收敛测试（`k3s/netisolate_test.go:362-391`）。
4. **Watch 观测面韧性做足**：swarm 事件水位锚跨轮续传 + panic recover 重开 + ctx 感知退避（`swarm/watch.go:40-142`）；k3s 同款外层循环 + 首轮锚定降级 + 10s 全量对账兜底（`k3s/runtime.go:543-617`），并有防静默/防 panic/退避 ctx 感知的专项测试（`k3s/runtime_watch_test.go:73-204`）。降级都有 Warn 日志，不静默。
5. **资源清理的"宁可漏扫不可误删"纪律到位**：swarm Secret 清扫三重判据 + 宽限窗 + maxDelete 确定性排序（`swarm/orphan.go:39-102`），匿名卷五重判据（`swarm/orphan_volume.go:43-88`）；k3s Secret 现役引用集真扫（Deployment/DaemonSet/Pod 的 secret/projected/envFrom/valueFrom/imagePullSecrets 全引用面，`k3s/hygiene.go:42-146`），peer grant 三信号 + 删除前 TOCTOU 复查（`k3s/hygiene.go:174-240`）。工具容器 `RemoveVolumes: true` 一手堵住了 11.6 万匿名卷泄漏（`swarm/utility.go:163-172`）。
6. **k3s RBAC 自举的身份收敛架构正确**：自举 admin 凭证只用于收敛 SA/ClusterRole/Binding，工作客户端整体换 SA token 并清空证书面（`k3s/provider.go:101-147`），规则集合用归一化比较做漂移更新（`k3s/rbac.go:170-193`），e2e 有 in-role/out-of-role 双向 can-i 断言（`e2e/dind-k3s.sh:324-339`）——架构方向正确，问题只在表内容滞后（见 H1）。

## 4. 发现清单

### R06-H1｜k3s RBAC 最小权限表落后于实际调用面：configmaps 整组缺失、networkpolicies 缺 update、pods 缺 deletecollection
- 维度：安全 / 测试与守卫
- 严重度：**H**（校准：三条**用户可用动线**在真实 k3s 集群会被 apiserver 403 拒绝——①任何带 `Materials.ConfigFiles` 的部署（compose `configs:` / 预部署 job bootstrap SQL）；②平台升级导致的 netpol 形状漂移收敛；③进程/组件重启（ADR-0069/IA v3）。这是 ADR-0055 已记录历史真机坑"新增 API 调用漏扩 RBAC 单测永绿"的同型复发；fake clientset 不执法 RBAC、e2e can-i 只抽样 6 条动词，门禁从结构上不可能拦住。）
- 位置：`internal/infrastructure/providers/k3s/rbac.go:41-65`（权限表）；调用面 `k3s/network.go:121-151`、`k3s/netisolate.go:164-184`、`k3s/restart.go:15-19`；守卫面 `k3s/rbac_test.go:139-157`、`e2e/dind-k3s.sh:331-336`
- 证据：

```go
// k3s/rbac.go:45-63（当前最小权限表节选：无 configmaps 规则；pods 无 deletecollection；networkpolicies 无 update）
{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"create", "delete", "get", "list", "watch"}},
...
{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"create", "delete", "get", "list"}},
// k3s/network.go:132-141（ensureConfigMaps：Create/Get/Update 三个动词，表里整组缺席）
_, err := p.cli.CoreV1().ConfigMaps(nsName).Create(ctx, cm, metav1.CreateOptions{})
...
if _, uerr := p.cli.CoreV1().ConfigMaps(nsName).Update(ctx, cur, metav1.UpdateOptions{}); uerr != nil {
// k3s/netisolate.go:179（形状漂移收敛：Update 需 update 动词）
_, err = p.cli.NetworkingV1().NetworkPolicies(nsName).Update(ctx, pol, metav1.UpdateOptions{})
// k3s/restart.go:17（DeleteCollection 需 deletecollection 动词，不是 delete）
if err := p.cli.CoreV1().Pods(namespaceName(ns)).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: sel.String()}); err != nil {
```

- 影响：
  - real k3s 上 `Ensure` 在 `ensureConfigMaps` 即 403 → 带 Config 材料的 App/预部署 job 部署**直接失败**；
  - 平台升级改 netpol 放行集（ADR-0055 已发生过一次）时 `putNetpol` update 403 → `reconcileNetIsolation` 报错 → 域内 Ensure 失败，升级收敛被阻断/降级；
  - `fleetly apps restart` / `fleetly components restart`（RuntimeRestart）在 k3s 恒 403；
  - 反方向：`rbac_test.go:139-157` 把表"钉死"在自身，`netisolate_test.go:362-391` 用 fake clientset 验证 update 收敛（fake 不执法 RBAC），e2e can-i 抽样不含新动词——守卫全部绿而真机必红。
- 建议：小改（S：三条规则补全 + e2e can-i 扩充）；**机制层**（M，推荐）：在 `internal/guards` 或 k3s 包内加"调用面 ↔ RBAC 表"静态守卫（AST 扫描 `CoreV1()/AppsV1()/NetworkingV1()/PolicyV1()` 调用 → (resource, verb) 集合，断言 ⊆ `desiredClusterRole` 规则并集），否则每次新增调用都要靠人记得扩表；同批把 e2e can-i 清单由表驱动生成。
- 状态：已亲验（代码 + RBAC verb 语义；本机无 k3s 无法真机复跑 403）

### R06-M1｜k3s ConfigMounts 静默丢弃：`configFiles` 全链透传但翻译层从不挂载，ConfigMap 孤建
- 维度：功能 / 架构一致性
- 严重度：**M**（校准：capability 契约明文承诺 Provider 按 `Workload.ConfigMounts` 挂载、`Materials.ConfigFiles` 下发（`capability/runtime.go:349-351,548-557`），k3s 侧无错误、无 honesty 声明地忽略；当前被 H1 的 403 掩盖为显式失败，**只修 RBAC 不同批修此条会把显式失败换成静默丢配置**（更危险），故两处必须同批。）
- 位置：`internal/infrastructure/providers/k3s/translate.go:322-385`（toContainer 未消费 configFiles）、`:455-530`（podTemplate 卷只有 PVC/hostPath/secrets）；`k3s/runtime.go:83,114-124,165,172-179`（configFiles 一路透传）；`k3s/network.go:119-151`（ConfigMap 只建不引用）
- 证据：

```go
// k3s/translate.go:322（签名收了 configFiles，函数体（:322-385）无任何消费点）
func toContainer(w capability.Workload, secretFiles map[string]string, configFiles map[string]string) corev1.Container {
	...
	if len(secretFiles) > 0 { c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: secretsVolumeName, MountPath: "/run/secrets", ReadOnly: true}) }
	return c   // 无 ConfigMounts / ConfigMap 卷挂载
}
// k3s/network.go:119-121（注释甚至声称"Provider 按 Workload.ConfigMounts 的目标路径挂载"）
// ensureConfigMaps 把 Materials.ConfigFiles 落为 ConfigMap 对象（平台名 → 对象名；
// Provider 按 Workload.ConfigMounts 的目标路径挂载）。
```

- 影响：k3s 上 `config_mounts` 声明的文件在容器内不存在——compose `services.*.configs`（`composeintake/intake.go:506-519`）与**预部署 job 的 bootstrap SQL 等材料**（`projection.go:68-80,119,221` 投影到 App/Task 两域；`engine/materials.go:56-60,124-127` 解析）全部失效；应用可能以默认配置启动（config 缺失而非报错），job 可能静默少跑初始化。另每次 Ensure 建出的 ConfigMap 无任何引用、无清扫面（见 M7 同族）。
- 建议：翻译层补 projected ConfigMap 卷（按 `ConfigMount.Target` 逐文件投影，语义对齐 swarm config target 与 `/run/secrets` 同密级）；补双侧单测（当前 swarm/k3s 均**无** ConfigMount 测试，grep 全仓 `*_test.go` 无 ConfigFiles/ConfigMount 断言）；成本 S/M。
- 状态：已亲验

### R06-M2｜k3s `InspectWorkloads` 副本数硬编码 1：双 Provider 观测面语义分叉，replicas≠1 的 App 每次重启后产生假 drift + 假收敛审计
- 维度：功能 / 可靠性
- 严重度：**M**（校准：`WorkloadObservation.Replicas` 是引擎 spec 对照的决策字段（`engine/drift.go:300-310` 逐字段比对），k3s 恒报 1 而 swarm 读真实副本数（`swarm/runtime.go:705-711`）；后果是稳定可复现的假 `workload.drift_detected` + 假 `workload.converged` + 审计行（ADR-0082 收敛动作），不是纯观测噪声；未到 H 是因为 Ensure 内容等价会跳过，不产生实际滚动。）
- 位置：`internal/infrastructure/providers/k3s/inspector.go:60-74`；对照 `swarm/runtime.go:705-711`、`engine/drift.go:308`、`engine/projection.go:120-125`
- 证据：

```go
// k3s/inspector.go:62-74（Deployment/DaemonSet 共用；从不读 d.Spec.Replicas）
func observeFromPodSpec(lb map[string]string, spec *corev1.PodSpec) capability.WorkloadObservation {
	...
	obs := capability.WorkloadObservation{ WorkloadID: lb[labelWorkload], Generation: capability.Generation(gen), State: capability.WorkloadRunning, Replicas: 1 }
	if len(spec.Containers) > 0 { obs.Image = spec.Containers[0].Image; obs.Command = spec.Containers[0].Command }
	return obs
}
// engine/drift.go:308（副本数是 spec 对照面）
case o.Replicas != want.Replicas:
	mismatch = fmt.Sprintf("replicas %d != expected %d", o.Replicas, want.Replicas)
```

- 影响：任何 `replicas: 2+` 的 k3s App（swarm 双节点 e2e 已用 replicas=2，平台能力面公开）在 fleetlyd 重启/收敛状态重置后，driftScan 一拍即判 spec drift → converge → 成功 → 发假 `workload.drift_detected`（"有人改了载体"语义）+ 假 `workload.converged` + source=system 审计行；之后每 60s 慢通道重放一次无谓 materialize。此外这是双 Provider 契约语义不等价的直接实例（观测字段一真一假）。
- 建议：k3s 侧读 `d.Spec.Replicas`（Deployment）/按节点数/0（DaemonSet 语义需与受管域 `Replicas=1` 期望对齐后再定），并把测试从"replicas=1 恰好通过"补成显式 replicas=3 断言（`k3s/runtime_ensure_test.go:213-228` 现断言 `EqualValues(1)`，对硬编码无鉴别力）；成本 S。
- 状态：已亲验

### R06-M3｜k3s Watch 逐 pod 事件不聚合：多副本下 degraded 被"名字排序最后一个 pod"系统性覆盖（L1 假就绪 / L2 看门狗失明）
- 维度：可靠性 / 功能
- 严重度：**M**（校准：swarm 已因同类事故专门实现聚合（`swarm/watch.go:169-259`，注释记录 staging 2026-10-16 假 drift 风暴与假通过实锤），k3s 侧同构问题未修；影响面是 replicas≥2 的 k3s App 的健康判定与退化可见性，非数据损坏。）
- 位置：`internal/infrastructure/providers/k3s/runtime.go:673-698`（mapPodEvent 每 pod 一条，无聚合）、`:761-779`（reconcileAllPods 按 pod 名字典序逐条发送）；对照 `swarm/watch.go:227-259`、`engine/observdomain.go:37-42,136-151`
- 证据：

```go
// k3s/runtime.go:683-696（每个 pod 直接映射成同 WorkloadID 的事件；无 per-workload 聚合）
gen, _ := parseUint(lb[labelGeneration])
ev := &capability.WorkloadEvent{ WorkloadID: lb[labelWorkload], Generation: capability.Generation(gen), Instance: pod.Name, Node: p.platformNodeID(pod.Spec.NodeName) }
state, exitCode, reason := podWorkloadState(pod)
...
// engine/observdomain.go:38-41（观测槽 last-write-wins，单 workload 单槽）
func (o *observations) observe(ev capability.WorkloadEvent) { o.observations[ev.WorkloadID] = ev }
// engine/observdomain.go:143-148（就绪门只看这个单槽）
ev, seen := o.observations[wid]
if !seen || ev.State != capability.WorkloadRunning || uint64(ev.Generation) != gen { return false }
```

- 影响（确定性部分）：10s 全量对账按 pod 名排序发送，**名字排序最后的 pod 的状态每拍覆盖其它副本**——若坏副本名排序靠前，平台永远观测不到 degraded（L2 稳态 stopped/degraded 事件缺失、Console 状态健康）；watch 实时路径同样存在同槽互覆。L1：2 副本 App 只要有一个 running@gen 事件最后落槽即 `readyGen` 通过，另一副本 pending/crash 被吞 → 部署可判 succeeded（概率/排序依赖，需 e2e 复现）。Task 域 one-shot 不受影响（每 Run 一个 pod）。
- 建议：把 swarm `aggregateWorkloadEvents` 的等价实现下沉到共享或 k3s 侧（per-workload 聚合，状态取"最重非 running"），补 k3s 多 pod 同 workload 的单测；成本 S/M。跨 Provider 一致性测试建议见 §5。
- 状态：已亲验（代码机制已验；L1 假就绪的时序概率性未真机复现，标机制推演）

### R06-M4｜k3s 日志帧不等价：时间戳=采集时刻、Node 恒空、`until` 窗口静默忽略（VL 数据面被污染）
- 维度：可观测性
- 严重度：**M**（校准：日志帧的 `Time`/`Node` 直接成为 VictoriaLogs 的 `_time` 与 `fleetly_node` 标签（`victorialogs/provider.go:266-292`），时间线错位与节点归因缺失是可检索数据面的确定缺陷；采集环游标以帧时间为推进锚（`engine/logging.go:270-291`），补窗漏行为机制推演。）
- 位置：`internal/infrastructure/providers/k3s/logs.go:90-124`；对照 `swarm/logs.go:133-153,291-318`、`engine/logging.go:270-291`、`interfaces/grpc/fleetlygrpc/contexts.go:666-682`（API 面 until 有效）
- 证据：

```go
// k3s/logs.go:91-99（PodLogOptions 无 Timestamps，也无 Until 可映射——swarm 侧 Timestamps/Since/Until 全开）
opts := &corev1.PodLogOptions{Follow: q.Follow}
if q.TailLines > 0 { t := q.TailLines; opts.TailLines = &t }
if !q.Since.IsZero() { s := metav1.NewTime(q.Since); opts.SinceTime = &s }
// k3s/logs.go:111-120（每一行的时间=读取时刻；Node 恒空；container=pod 名）
frame := capability.LogFrame{ WorkloadID: workloadID, Container: podName, Node: "", Time: time.Now().UTC(), Line: []byte(trimmed), ... }
```

- 影响：①所有 k3s 日志的 `_time` 是采集侧墙钟（批量补读历史行时全部挤到"现在"，时间线失真、排障与告警窗口判断错误）；②`fleetly_node` 标签恒空，日志面失去节点归因（swarm 从 daemon details 解析）；③`fleetly logs --until` 在 k3s 静默返回窗口外行（API 受理了参数、Provider 不实现、无 honest 声明——与 Notes 的诚实边界文化不一致）。游标链路：`nsBatch` 以帧时间推进游标，VL 断续/背压积累后重开流时 `Since=游标`，生产时间早于游标但在途未读的行会被跳过——**待复核**（机制推演，需真机 VL 故障演练）。
- 建议：k3s 侧开 `Timestamps: true` 并解析 RFC3339 前缀（k8s 日志行形如 `<time> <msg>`）；从 pod 标记还原平台节点 ID 填 `Node`；`Until` 不支持时在 Notes/Describe 明示或客户端侧过滤；补 logs 单测（当前 k3s `logs.go` 无测试）。成本 S。
- 状态：已亲验（代码与消费面）；游标漏行为待复核

### R06-M5｜swarm `ExecWorkload` 在 stdin EOF 后进入无退避忙轮询：每次 `ExecInspect` 紧接下一次，持续到容器退出
- 维度：性能 / 可靠性
- 严重度：**M**（校准：非交互长命令（stdin 立即 EOF 是 CI/脚本/`echo|` 常态）会在整个命令执行期以 daemon HTTP RTT 级频率轮询 `/exec/{id}/json`，单会话可打满控制面 CPU/daemon 请求；会话并发上限 8 会放大。TTY 交互态不受影响。）
- 位置：`internal/infrastructure/providers/swarm/exec.go:152-172`
- 证据：

```go
// swarm/exec.go:152-172：stdinDone 是"已关闭"通道，一旦关闭该 case 恒就绪，
// select 每次立即选中它，150ms 节拍（execInspectPoll）失效 → 无间隔热循环。
for {
	insp, err := p.cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	...
	select {
	case <-ctx.Done():     return 0, fmt.Errorf("swarm exec: %w", ctx.Err())
	case <-stdinDone:      // 关闭后恒就绪
	case <-time.After(execInspectPoll):
	}
}
```

- 影响：`echo | fleetly exec app/web -- <long-running cmd>`、CI 中用 heredoc 关闭 stdin 的 exec、以及所有经 relay 的会话在 `RelayFrameStdinEOF` 之后；表现为 daemon API 请求风暴与 fleetlyd CPU 空转（无日志、无指标，难察觉）。
- 建议：`stdinDone` 置 nil（或用 `sync.Once` + 局部变量）后再进入轮询，让 150ms 节拍生效；顺手把节拍改为可注入以写单测。成本 S。**测试缺口**：swarm 包对 `ExecWorkload/RunNodeRelay/ExecInstances` **零单测**（k3s 同构实现有 `exec_test.go` 覆盖 relay 帧流），见 §6。
- 状态：已亲验

### R06-M6｜k3s egress deny NetworkPolicy 仍是 create-only：同文件集内 `putNetpol` 已修"形状漂移锁死"，此路径漏修 → 平台升级后旧隔离形态永久残留
- 维度：架构一致性 / 可靠性
- 严重度：**M**（校准：`netisolate.go` 注释自证 create-only 会让存量 policy 永锁旧形态（staging 真机实证），成员/grant policy 已改 create-or-update，egress deny 未改；升级后若 egress 放行集/拒绝语义变化，旧 policy 不收敛——安全方向可能变成过度放行（漏隔离）或过度拒绝（断服务发现），取决于升级内容；当前版本形态恒定所以未爆。）
- 位置：`internal/infrastructure/providers/k3s/network.go:36-57`；对照 `k3s/netisolate.go:159-184`
- 证据：

```go
// k3s/network.go:52-56（need=true 时 create，AlreadyExists 即当作收敛完成——不比较/不更新存量 policy）
_, err := p.cli.NetworkingV1().NetworkPolicies(nsName).Create(ctx, toEgressNetpol(), metav1.CreateOptions{})
if err != nil && !apierrors.IsAlreadyExists(err) { return fmt.Errorf("ensure egress netpol: %w", err) }
// k3s/netisolate.go:159-163（同文件集的反面注释：create-only 会被真机咬）
// putNetpol 是 netpol 的 create-or-update 落盘：... create-only 会让存量 policy 永锁旧形态
// （staging 真机实证：新 FROM 规则不落地）。
```

- 影响：升级收敛缺口（ADR-0052/0055 的"升级后隔离面自愈"承诺在 egress deny 上不成立）；与 H1 联动——即使补 update 也要同批补 RBAC update 才真收敛。
- 建议：`reconcileEgressNetpol` 改走 `putNetpol`（同一收敛原语单源），补形状漂移测试；成本 S。
- 状态：已亲验

### R06-M7｜swarm Config 载体无清扫面：内容寻址版本化的 config 每次值变更永久累积
- 维度：可靠性（资源泄漏）
- 严重度：**M**（校准：与 E29 修复的 Secret 载体泄漏同族（当时积压 1300+ 孤儿后专门补 `SweepOrphanSecrets`），config 载体用同一 `secretCarrierName` 版本化公式（`fleetly-sec-<name>-<fp8>`）却不在任何清扫面；k3s 侧 ConfigMap 同样孤建（M1）。泄漏速率低（Config 版本更新/模板刷新才产生），无界增长。）
- 位置：`internal/infrastructure/providers/swarm/network.go:100-113,128-158`；`swarm/orphan.go:39-64`（只 SecretList）；`capability/runtime.go:109-131`（Hygiene 三扫）；全仓 grep `ConfigList|ConfigRemove` **零命中**
- 证据：

```go
// swarm/network.go:130-136（config 与 secret 共用不可变版本化命名）
func (p *Provider) ensureConfigs(ctx context.Context, m capability.Materials) (map[string]secretCarrier, error) {
	for name, value := range m.ConfigFiles {
		carrier := secretCarrierName(name, fingerprintHex(value))
		if res, err := p.inspectConfigCarrier(ctx, carrier); err == nil { ... }   // 旧版本从不复查/删除
// swarm/orphan.go:62-64（清扫面只列 Secret）
res, err := p.cli.SecretList(ctx, client.SecretListOptions{
	Filters: client.Filters{}.Add("label", labelManaged+"=true"),
})
```

- 影响：config 载体（swarm config 对象）随每次 Config 内容变更净增一枚，永不回收；平台长期运行后 config 对象数无界增长（与 E29 前 Secret 同形态），且 `SweepOrphanSecrets` 的现役集只认 `ContainerSpec.Secrets`，天然不含 config，无法顺带收口。
- 建议：`RuntimeHygiene` 或现有 Secret 清扫扩展 config 面（`ConfigList` + `ConfigRemove`，判据复用 Secret 三重防误删：managed 标签 + 前缀 + 现役引用集 `ContainerSpec.Configs` + 出生宽限窗）；补测试（`orphan_test.go` 现只覆盖 Secret）；成本 S。
- 状态：已亲验

### R06-M8｜中继写队列满时静默丢帧，且 ack/exit/error 控制帧与控制数据共用这条可丢队列
- 维度：可靠性
- 严重度：**M**（校准：`post` 的 default 分支丢帧是既有注释承认的"有界丢弃"语义，但控制帧（ack/exit/error）同样走该队列——客户端慢/网络抖动下 exit 帧可被丢，manager 侧会话只能等超时收口（用户感知为会话假挂死）；stdout 丢帧则静默截断输出。双 Provider 各有一份相同实现。）
- 位置：`swarm/noderelay.go:299-314`、`swarm/noderelay.go:247-297`（ack/exit/error 经 `post`）；k3s 同构 `k3s/noderelay.go:362-377,338-358`
- 证据：

```go
// swarm/noderelay.go:300-313（全部帧共用；满即丢，无错误帧/计数/日志）
func (a *relayConn) post(b []byte) {
	...
	select {
	case a.send <- b:
	case <-a.done:
	default:
		// 写队列满：有界丢弃（既有语义，写超时兜底）
	}
}
// noderelay.go:294-295（exit 帧也走 post → 可被 default 丢掉）
a.post(capability.MarshalRelayFrame(capability.RelayFrameExit, open.SessionID, capability.EncodeRelayJSON(capability.RelaySessionExit{Code: code})))
```

- 影响：exec 会话在慢消费端/大输出时：①stdout 部分丢失且无任何提示（用户以为命令输出就这些）；②exit/ack/error 帧丢失 → 会话不终结（等 30min 会话上限超时），资源占用与体验损害。
- 建议：控制帧与数据帧分队列（控制帧阻塞写或独立高优先队列；数据帧丢弃时置位并有界上报（RelayFrameError/日志计数））；k3s/swarm 两份实现应共享同一份仲裁逻辑；成本 S/M。
- 状态：已亲验（丢帧路径）；会话超时兜底时长为推演（engine 会话限额另见 R04）

### R06-L1｜可选子面协商"单点"FacesOf 不含 RuntimeRestart；双 Provider 编译期断言清单也缺 Restart（swarm 还缺 Exec）
- 维度：架构一致性 / 测试与守卫
- 严重度：L（校准：`capability/faces.go` 注释宣称 duck-typing 单点收拢，但 RuntimeRestart 由 engine 直接类型断言（`processrestart.go:52-55`、`componentrestart.go:51-55`），Provider 侧 `provider.go` 的"全契约"断言清单（swarm 8 条、k3s 8 条）也不含 RuntimeRestart；接口签名若漂移，双 Provider 静默退化为"不支持重启"，无红灯。）
- 位置：`internal/domain/shared/capability/faces.go:12-37`、`internal/infrastructure/providers/swarm/provider.go:84-93`、`k3s/provider.go:85-94`；对照 `swarm/{exec.go,restart.go}`、`k3s/{exec.go,restart.go}`
- 证据：

```go
// capability/faces.go:13-27（无 Restart 字段——可选子面枚举单源的例外）
type ProviderFaces struct {
	Logs RuntimeLogs; Admin RuntimeAdmin; Inspector RuntimeInspector; Hygiene RuntimeHygiene; Utility RuntimeUtility
	NetworkMaintenance RuntimeNetworkMaintenance; Exec RuntimeExec; NodeInspect RuntimeNodeInspect
	...
}
// swarm/provider.go:84-93（注释"全契约"，实际缺 RuntimeExec 与 RuntimeRestart 断言）
var (
	_ capability.Runtime = (*Provider)(nil)
	_ capability.RuntimeLogs = (*Provider)(nil)
	... _ capability.RuntimeUtility = (*Provider)(nil)
	_ capability.RuntimeNetworkMaintenance = (*Provider)(nil)
	_ capability.RuntimeNodeInspect = (*Provider)(nil)
)
```

- 影响：可选子面能力清单不可发现（装配期 faces 日志/doctor 不显示 restart）；接口演进时的静默降级缺口。
- 建议：ProviderFaces 增 `Restart capability.RuntimeRestart`，engine 两处改走 FacesOf；双 Provider 断言补齐 RuntimeExec/RuntimeRestart；成本 S。
- 状态：已亲验

### R06-L2｜swarm `mapEvent` 把一切 node 事件映射为 `NodeJoined`（不区分 join/leave），离开事件实际由 DescribeCluster 对账兜底
- 维度：功能 / 架构一致性
- 严重度：L（校准：contract 注释称 Watch 返回"节点加入/离开事件"，实现不区分动作；engine 的 node.left 走周期对账（`engine/nodes.go:9-16`），最终一致但窗口内语义错误——若 docker node 事件携带 labels，离开事件会把节点 upsert 成 `Available=true`（`engine/observ.go:224-231`），窗口内 `firstAvailableNode` 可能选中已离开节点。）
- 位置：`internal/infrastructure/providers/swarm/watch.go:426-435`、`engine/observ.go:224-242`、`engine/nodes.go:9-16`
- 证据：

```go
// swarm/watch.go:426-435（node 类型：不分 Action、不看 Available/Status，一律 NodeJoined，Minted=false）
case "node":
	return capability.WorkloadEvent{
		NodeJoined: &capability.NodeJoined{
			NodeID:    msg.Actor.Attributes[labelNodeID],
			CarrierID: msg.Actor.ID,
		},
	}, msg.Actor.Attributes[labelNodeID] != ""
// engine/observ.go:227-229（收到即按"在场"upsert）
return s.Nodes.Upsert(ctx, &node.Node{PlatformID: nj.NodeID, CarrierID: nj.CarrierID, Available: true, ...})
```

- 影响：节点 leave/update 事件在窗口内被当作"节点仍可用"；真正 node.left 依赖 DescribeCluster 对账拍（受管环节拍量级），`node.joined` 事件语义被稀释。
- 建议：node 事件分支判 `msg.Action`（leave/remove）→ 新增 NodeLeft 事件形态或跳过；或 contract 文本改写为"加入事件 + 离开经对账"（R01/R15 对齐）。成本 S。
- 状态：待复核（需确认 docker daemon node 事件 actor attributes 是否带 `fleetly.node.id`；不带则该分支恒 false，仅"契约文本≠实现"的 L）

### R06-L3｜ADR-0052 载体映射表与 k3s 实现两处漂移（liveness、ConfigFiles）
- 维度：文档一致性
- 严重度：L（校准：ADR-0052 决策 3 映射表写 `Healthcheck → readiness+liveness probe`，实现只有 readiness（代码注释说明有意偏离）；映射表缺 `Materials.ConfigFiles` 行，实现半落地（M1）——ADR 是验收真源，漂移会让后续读者/守卫误判"已覆盖"。）
- 位置：`docs/adr/0052-k3s-second-runtime-pilot.md:49-55`；`k3s/translate.go:309-315,355-356`
- 证据：

```go
// k3s/translate.go:313-315（实现注释与 ADR 表相反，属有意设计）
//   - 只配 readiness 探针、不配 liveness（k8s liveness 失败自动重启载体，
//     与平台语义自治冲突——L1 健康门/L2 看门狗在平台层；不健康在 Watch 流
//     呈现为 degraded，处置动作归平台，ADR-0052 决策 3）。
// ADR-0052:49（映射表原文）：| `Healthcheck` | readiness+liveness probe（http/tcp/exec 三形态原生） |
// ADR-0052:54-55（Materials 只有 SecretFiles/RegistryAuth 两行，无 ConfigFiles）
```

- 建议：ADR-0052 补注"liveness 有意不做"与 ConfigFiles 落地状态；M1 修复时同批更新映射表；R15 可一并核。
- 状态：已亲验

### R06-L4｜k3s `Addresses` 不校验载体存在，按期望集直接合成端点（swarm 侧以实际服务列表为准）
- 维度：功能 / 架构一致性
- 严重度：L（校准：contract 说"未匹配到期望集成员的服务不计入端点"——swarm 实现为列真实服务后按 workload 标记匹配（`swarm/runtime.go:545-576`）；k3s 直接从 expected 集拼 DNS 名，Service 缺席/被删时仍返回"可达地址"，engine 的路由发布失去"解析不到即跳过"的降级面（Route 指向不存在后端 → 502 而非预检跳过/诊断）。
- 位置：`internal/infrastructure/providers/k3s/runtime.go:781-804`；对照 `swarm/runtime.go:545-576`
- 证据：

```go
// k3s/runtime.go:786-800（不 List Service、不校验存在性——纯按 expected 合成）
for _, w := range expected {
	for _, a := range w.Addressing {
		host := serviceCarrierName(a.Name) + "." + nsName + ".svc"
		for _, port := range w.Ports { endpoints = append(endpoints, capability.Endpoint{Addr: host, Process: w.Process, Port: port.Port}) }
	}
}
```

- 影响：Ensure 未跑/失败、Service 被外部删除、Addresses 被并发调用时，route publish 仍能解析出端点；用户可见为 502 且事件面缺失"backend unresolved"诊断。
- 建议：先按 ns 选择器 List Services，按 addressing label 与 expected 交集出端点（与 swarm 同构）；补缺 Service 用例；成本 S。
- 状态：已亲验

## 5. 交叉观察（不在本模块但值得其他模块注意）

1. **载体网络无人回收（R02/R13）**：全仓 `RemoveNetwork` 只有 ADR-0046 重建路径调用（`engine/networkrebuild.go:120`），Project 删除/网络声明移除没有任何 carrier 清理；swarm 的 per-Project overlay 会随项目删除永久残留（k3s 靠 ns 删除顺带收口）。Provider 侧的 `Runtime.Remove` 契约只覆盖"域内载体"，网络回收本应在 engine 关网络时显式 `RemoveNetwork`——建议 R02/R13 评估该泄漏面并决定归属。
2. **双 Provider 缺共享一致性测试（R12）**：本报告的 M1/M2/M3/M4 都是"两侧语义分叉但各自单测全绿"的形态；且 capability 契约观察字段（Replicas/Time/Node/Aggregation 语义）没有跨 Provider conformance 用例。建议 R12 建 `testruntime` 驱动的 Provider 契约一致性套件（对 fake/两真实 Provider 跑同一批断言），这类分叉才能在 CI 变红。
3. **capability.Watch 契约文本与 engine 实现不一致（R01/R02）**：`capability/runtime.go:33-36` 写"含节点加入/离开事件"，engine 的 node.left 实际来自 DescribeCluster 周期对账（`engine/nodes.go:9-16`）——词汇/契约文本的诚实边界应两处对齐（并与 L2 同批）。
4. **ADR-0055 验收锚"putNetpol 形状漂移收敛…各带单测或 e2e 锚"偏乐观（R15）**：该修复只有 fake clientset 单测（本报告 H1 证明真路径 RBAC 缺 update）；建议 R15 把这类"单测锚冒充真机锚"的声明逐条降级/补锚。
5. **exec relay 帧上限/丢帧语义与 gateway 共享（R08/R04）**：1MB 读上限/32KB 帧上限在 provider 与 assembly 两侧各自硬编码（`swarm/noderelay.go:35`、`k3s/exec.go:196`、`assembly/gateway_exec.go:92,183`），无单源常量；未来任一侧调整都会破坏兼容性。建议收进 capability 常量。

## 6. 测试与守卫质量评估

**强项**
- 内容等价（ADR-0064）与 restart_seq（ADR-0083）是"事故驱动 + 双 Provider 对偶"的高质量测试：账本短路/冷账本/replicas-only/内容变更/seq 变/skip/缺失/升级零滚全覆盖（`swarm/runtime_contentequiv_test.go`、`k3s/runtime_contentequiv_test.go`）；swarm 有真实 HTTP fake daemon（`fakedaemon_test.go`），不是纯 mock 断言调用数。
- k3s Watch 韧性测试（防静默/防 panic/退避 ctx 感知）与 netpol 收敛测试（fresh deploy 防自噬、跨域安全、grant 收敛、形状漂移）质量高，且注释带真机事故编号，可回溯。
- `go test -race` 双包通过（本次亲跑），锁使用（ledgerMu/issuedGenMu/nodeIDMu/savedMu）无竞态。

**缺口（与上面发现对应）**
- **RBAC 无"调用面 → 规则"守卫**：`TestDesiredClusterRoleTable` 只把表钉在自身（`k3s/rbac_test.go:139-157`）；fake clientset 不执法 RBAC，`TestNetpolShapeDriftConverges` 证明了 update 路径"测试绿、真机 403"的假阴性形态。e2e can-i 仅抽样 6 条（`e2e/dind-k3s.sh:331-336`），新增调用（configmaps/netpol update/pods deletecollection）零锚。这是本模块最需要机制层补齐的守卫。
- **swarm exec 面零单测**：`ExecWorkload`（hijack/TTY/stdin EOF/退出码轮询）、`relayConn`（帧多路复用/resize/shutdown）在 swarm 包无任何测试；只有 `nodeRelayScript` 字符串测试（`exec_script_test.go`）。同构的 k3s 侧有 `TestRelaySessionFrameFlow/TestRelaySessionErrorFrame`（`k3s/exec_test.go:130-236`）——一旦 swarm 副本漂移，只有 e2e 的 echo/退出码会兜。M5/M8 两个缺陷都在这个盲区里。
- **ConfigMount 双侧零测试**：grep 全仓 `*_test.go` 无 `ConfigFiles/ConfigMount` 断言 → M1/M7 无锚。
- **k3s 观测面语义无测试**：InspectWorkloads 只测 replicas=1（`runtime_ensure_test.go:227`），多副本聚合/事件槽无测试 → M2/M3 无锚。
- **e2e 缺口（读脚本）**：`e2e:dind-k3s` 覆盖 deploy/rollback/egress/netpol/grant/exec/backup-restore，但**不覆盖**：config materials、restart 动词、replicas>1（漂移/健康）、netpol 形状升级收敛、日志时间/节点归因。故 H1/M1/M2/M3 在 CI 永不红。
- 假阳性风险：k3s `putDeployment/putService` 的冲突重试用 fake clientset 覆盖，但 fake 不模拟 defaulting/immutable 字段（ClusterIP/Selector），真机面（如 headless Service 不可逆）无测试——本报告未将其列为发现（影响面低），但属同类盲区。

## 7. 未决与置信度

**最不确定的 5 点**
1. **H1 的真机 403 未复现**（本机无 k3s/Docker）。置信依据是 k8s RBAC verb 语义（configmaps 资源规则缺失、update/deletecollection 为独立 verb），标准且确定；缺的是"真机跑一次带 config 的 k3s 部署 + restart"的台账。置信度：高。
2. **M3 的 L1 假就绪是否在真实时序中发生**：机制（单槽 last-write-wins + reconcile 排序覆盖）已亲验，但"部署判 succeeded 时恰好 running 事件最后落槽"依赖 Deployment 创建顺序与 watch 时序；稳态 L2 掩蔽是确定性结论。待 e2e（k3s + replicas=2 + 一个坏副本）验证。置信度：中。
3. **M4 的游标补窗漏行**：时间戳=采集时刻是确定项；"重开流 From 游标会跳过生产时间早于游标的在途行"是机制推演（需要 VL 故障演练 + k8s 日志 SinceTime 语义复核）。置信度：中。
4. **M2 的实际触发频率**：fleetlyd 重启/`resolved` 状态重置才会重发假事件；若 ADR-0082 的 resolved 生命周期后续变化，影响会放大。事件/审计噪声的严重度取舍可能被 Lead 降为 L。置信度：中。
5. **L2 的 docker node 事件 attributes 形态**未核实（模块缓存无 swarmkit 源码）。若不带 labels，该分支恒 false，只剩"契约文本≠实现"；若带 labels，则存在窗口内节点假可用。置信度：低（需 daemon 源码/真机 `docker events --filter type=node` 取证）。

**整体置信度：中高**。所有 H/M 结论都有 `文件:行` + 代码摘录并已核对调用方（engine 消费面/assembly/材料来源）；未做真机复跑与 e2e 是主要限制。覆盖为两 Provider 全部 30 个生产文件 + 契约 + 关键消费方；测试文件与 e2e 为抽样（清单见 §1）。

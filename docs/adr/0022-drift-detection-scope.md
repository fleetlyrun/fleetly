# ADR-0022: 漂移检测口径——spec 对照 + 启动基线重放 + 稳态看门狗

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | ADR-0005（收敛 opt-in 维持）、ADR-0016（Generation 锚）、架构 §5、领域模型场景 7 |

## 背景

N0 验收发现漂移检测三处口径缺口：

1. **gen-only 对照对"人工改载体"失明。** 检测锚只有 Workload 标记里的
   Generation：`docker service update --image …` 改镜像、改副本数都不动
   fleetly 标签——Generation 相同，drift 永不触发（领域模型场景 7
   "人工改载体 → Drift 事件"不可达）。
2. **重启后归属缓存清零。** workloadApp/expected 是进程内缓存；引擎重启
   后无在途部署的 App 不再 Ensure，缓存空置——drift 检测对稳态 App 失明
   直到下一次部署。
3. **看门狗只在 L3 观察窗内咬合。** succeeded 之后载体崩溃/停止没有
   任何信号（L2 只在 observing 步内检查）。

## 决策

1. **drift 升级为 spec 对照。** 引擎在 Ensure 时缓存投影后的 Workload
   spec（镜像/副本/命令）；周期扫描经新可选子面 `RuntimeInspector`
   （`InspectWorkloads(ctx, ns)`）读取载体观测 spec，逐字段对照。失配 →
   `workload.drift_detected`（payload 增补失配明细），签名去抖沿用
   （签名含 spec 指纹）。Generation 偏离仍是 drift 信号（保留原路径）。
2. **启动按 succeeded 基线重放。** Start 时对每个"最近一次部署为
   succeeded"的 App 以行上 Generation 幂等重放 Ensure（载体未变即
   no-op）：重建归属/期望缓存，drift 立即在场，无需等下一次部署。
   不产生新的 Deployment 行（重放不是部署）。
3. **看门狗稳态常驻（观测面）。** 稳态 App（最近部署 succeeded）的
   当前 Generation 观测到 stopped/degraded → 发 `workload.stopped`
   （新事件，去抖）。**只观测不迁移**：自动回滚仍是部署期语义
   （ADR-0005：Ensure 是唯一写动词，收敛 opt-in 不变）；稳态处置由
   人/Agent 决定（重部署即收敛）。
4. **RuntimeInspector 是可选子面**（同 RuntimeLogs/RuntimeAdmin 形态）。
   未实现的 Runtime：spec 对照降级为 gen-only + 状态观测（诚实降级，
   日志明示），不阻断。

## 后果

- 场景 7 可达：人工改镜像/副本 → 下一扫描拍（DriftScanInterval，
  默认 30s）出 drift 事件（带明细）。
- swarm Provider 新增 InspectWorkloads（ServiceList + 标记还原 +
  spec 读取）；假底座同步实现（apitest/engine 测试）。
- 事件册只增：`workload.stopped` 入册（workload.drift_detected
  Summary 同步改为 spec 对照口径）。
- 代价：每扫描拍一次 per-App ServiceList（N0 小团队规模可忽略；
  扫描间隔可配）。

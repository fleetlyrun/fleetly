# ADR-0099: 收敛循环 panic 自愈——有界重启与终态健康

日期：2026-11-11 ｜ 状态：Accepted

## 背景

engine 的每条收敛循环是单写者：`Loop.Run` 捕获 panic 后关闭 `done`，旧 supervisor 只把环记为 dead 并写 health。进程不退出，systemd 不会重启，于是 deployment/task/data 等域在进程剩余生命期内永久停摆。R02-M7 的 health 面让死亡可见，但没有自愈动作。

内部测试期允许破坏性变更；本 ADR 把「环死亡 = 进程生命期终态」改为「有界自愈 + 终态健康」。

## 决策

1. **有界重启**：每条环（含 drift 扫描环）由 `runLoopWithRestart` 驱动。panic 后按 1s → 2s → 4s → 8s 退避（上限 30s）重启；最多 5 代（4 次重启）。第 5 次 panic 后记 `ring dead` 终态，不再重启。
2. **代际重置**：`Loop.Reset()` 重建 kick/done 通道并清除 panic 标记；`Done`/`Panicked` 只描述当前代，旧代 `Done` 保持已关闭。重启后立即 `Kick` 一次，按 Generation 幂等重放收敛。
3. **优雅停止不重启**：ctx 取消（Stop）返回且不记 dead、不计重启。
4. **健康语义**：`DeadRings`/`HealthComponents` 只报终态死亡（`ring=<name> dead`）；瞬态重启只进日志与 `Loop.Restarts()` 计数，不制造 DEGRADED 抖动。
5. **熔断基线**：`engineCoreFuseBaseline` 409 → 416（+7 生命周期机制函数）。这是机制原语/生命周期能力，不是新域动词，不违反 ADR-0088 生长律；基线修订随本 ADR 同批。
6. **loop registry**：lifecycle 受管挂载从 `r.loop.Run` 改为 `e.runLoopWithRestart`；登记表同批更新。

## 后果

- 瞬时 panic 自愈，控制面不再需要人工重启才能恢复某域。
- 持续性 panic 在 5 代后终止并进 health，避免无限重启风暴。
- 重启期间的收敛由 Generation 幂等重放兜底；单写者序不变。
- 重启计数目前只经日志/`Loop.Restarts()` 暴露；后续可并入 `GetStatus` 组件详情。

## 验收锚

- [x] `TestLoopResetStartsFreshGeneration`：Reset 换代、旧/新 Done 与 Panicked 语义。
- [x] `TestRingRestartPolicyBackoff`：退避序列与上限。
- [x] `TestRingPanicOnceRestartsAndConverges`：panic 一次后重启并继续收敛。
- [x] `TestGracefulStopDoesNotRestartRings`：Stop 不重启、不记 dead。
- [x] `TestRingPanicMarksRingDeadAndHealthVisible`：5 代后终态 dead 且 health 可见。
- [x] `TestKindRingPanicStaysInItsOwnRing`：故障域隔离保持。
- [x] `engineCoreFuseBaseline` 416 与 `engine-func-baseline.golden` 同批重锚。

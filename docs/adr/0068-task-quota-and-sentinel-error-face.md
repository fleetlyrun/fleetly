# ADR-0068: Task 配额定律单源与哨兵错误面收口

日期：2026-10-11；状态：已采纳（架构评审第三轮候选 4，同批落地）。

## 背景

配额规则三路径两条文案各自演化（架构评审第三轮候选 4，Worth exploring）：API 受理位 `taskQuota`（acceptance.go，直发 apperr 两条文案）、engine `taskSpawnQuota`（task.go，`ErrTaskQuota` 哨兵）、`ScaleTask` 内联 delta 检查（第三份判定）；`ErrTaskQuota` 的 API 转义在 `mapTaskVerbError`（scale 专属措辞）与 `mapScheduleVerbError`（schedule 复制体）两处两种说法。注释自承 spawn 检查曾经历 firstboot/schedule 逐字双拷贝的收编——同一失败模式在转义面复现。

## 决策

1. **配额定律单源**：engine 导出纯函数 `CheckTaskQuota(projectID string, active int, sum, delta int64, spawn bool) error`——双上限（非终态行数 / desired_concurrency 总量，闭区间上界），两模态显式入签名：spawn 腿计活跃行数（铸造占新行位），scale 模态 spawn=false 只计并发净增量（原地改 desired 不占新行位）。三处判定共用：`taskSpawnQuota`（StatsByProject + spawn）、`ScaleTask`（内联块删除，delta=新−现）、API `taskQuota`（StatsByProject + spawn）。stats 读取留各调用方事务内（SQLite 单写串行，无 TOCTOU 不变）。
2. **`ErrTaskQuota` 唯一转义**：`mapTaskQuotaError`（mapTaskVerbError 与 mapScheduleVerbError 共用）——动词中性文案 `this request would exceed the project task quota (%d tasks / %d desired concurrency)` + WithSuggestion 保留；两旧文案退役。哨兵详情（项目现值）在 cause 链。golden 面安全：schema golden 只钉 reason 枚举说明、quota_probe 断言码 + 松短语 "would exceed"。
3. **mapper 拓扑注记**：`mapStateError` 内嵌的 Task 三分支（ErrTaskTerminal/ErrTaskNotRenewable/ErrNotResident）实核不可达——现役 `mapStateError(err, "task")` 调用点全为 state 读（Get/List），无写路径哨兵可达；三分支归并为单 case 委托 `mapTaskVerbError` 并注记拓扑：**Task 域哨兵唯一 owner 是 mapTaskVerbError，新增哨兵落此不落通用出口**。不做无病灶搬迁。
4. **不动项**：firstboot 的 engine 域消费（`TestFirstBootQuotaBoundedRetry` 锚定的有界等待）是哨兵的第四消费者、不同模态（机制动作非用户动词），保留 engine 域语义；配额常量 `MaxTasksPerProject`/`MaxTaskConcurrencyPerProject` 已单源于 engine，不动。

## Consequences

- 配额新腿（如未来 per-Team 上限）只改 `CheckTaskQuota` 一处；create/scale/fire 错误面一种说法，客户端处置逻辑不再随动词分叉。
- `ErrTaskQuota` 哨兵消费面最终形：API 三 verb 同文案 + firstboot 有界等待（engine 域）+ 到期拍 skip（engine 域）——转义与消费各归其位。
- mapper 拓扑成文：通用出口不再吸纳域哨兵分支（现有三分支为防御 fallback，随 mapTaskVerbError 委托）。

## 验收锚

- [x] `TestCheckTaskQuotaModes`：spawn 腿两上限分别命中 + 闭区间边界；scale 腿行数上限豁免（超行数下仍放行非增量 scale）+ 并发腿执法。
- [x] 变异验证：spawn 腿删除 → 直测 + `TestFirstBootQuotaBoundedRetry` 双红，还原复绿。
- [x] quota_probe 全绿（E_QUOTA_EXCEEDED 码锚 + "would exceed" 松短语；行数腿断言随统一文案更新并注明依据）。
- [x] engine/api/apitest 全绿；golden 零漂移。

// Package engine 是 fleetly 发布引擎（task-breakdown T2.10 发布状态机与
// 对账核心 + T2.11 窗口与失败语义）：compose → 放置/env/镜像/secret 准备 →
// Swarm service 对账（新增/更新/删除）→ 健康门 → 观察窗 → 版本固化，以及
// 失败分流（归位/首发 scale=0/已切流告警）、L2 看门狗与 L3 观察窗、控制面
// 重启分类恢复、cancel 与 app 派生状态。
//
// 设计真源（逐条对齐，不改语义）：
//   - docs/design/2026-09-17-release-semantics.md（§2.1 四不变量、§2.2 窗口
//     模型、§2.3 状态机、§2.4 快照与归位、§2.5 场景矩阵 1-16、§2.6 stop-first
//     专表、§2.7 错误码/事件/审计、§2.8 治理参数）
//   - docs/design/2026-09-17-architecture.md §2.4/§2.5（受管字段固定
//     failure_action=pause、monitor=5s；省略=删除；env 三层合并随部署生效）
//   - docs/design/2026-09-17-state-model.md §2.10（app 派生状态优先级）
//
// 结构纪律（架构 §2.8）：核心不出现第三方类型——Swarm 服务/任务面经本包
// 定义的端口（Substrate）访问，moby 类型只存在于 internal/substrate 的实现
// 内；状态写走 internal/state（事务内事件+审计 fail-closed）。
//
// 观测纪律（Spike B 硬经验）：Engine 29.x docker events 无 task 事件——
// 状态机推进一律走 service/task API 轮询（poll tick），订阅流只作观测缓存
// 失效信号（internal/state.Observer，与本引擎无关）。归位重放禁用 --force
// （Spike B2：同内容重放任务零替换的零成本归位依赖它）。
package engine

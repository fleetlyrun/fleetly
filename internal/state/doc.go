// Package state 是 fleetly 控制面状态层（SQLite 权威态 + 底座观测缓存
// + 写前直读端口）。
//
// 分层判据与权威归属（state-model §2.1，本包实现的真源）：
//
//   - 权威态：只存无法从底座重算的知识——期望态（apps/revisions/env_vars/
//     domains/placements）、历史（deployments/events/audit_log）、凭证
//     （tokens）、备份台账（state_backups）、孤儿登记（orphans）。
//   - 派生缓存：nodes 表 = Docker/Swarm 观测快照，每行带 observed_at/stale，
//     可整表重建，禁止用于决策（state-model §2.2）。平台不承诺任何底座侧
//     存活时间戳（Swarm 不暴露该概念），平台观测时间字段为 last_seen_at。
//   - 实时直读：写操作先直读底座并以对象版本作乐观令牌，冲突返回
//     E_STATE_VERSION_CONFLICT（resolve.go 的 VersionResolver）。
//
// 审计 fail-closed（state-model §2.9）：破坏性/管理操作与业务写同事务，
// 审计写失败即操作失败（audit.go，事务内 WriteAudit）。事件 seq 单调
// （events.go，AUTOINCREMENT 永不复用），SSE 游标早于保留窗返回
// E_EVENT_CURSOR_EXPIRED（410）并附 oldest_seq。删除走 tombstone-first
// （apps.go：deleting → deleted 状态位，恢复不复活）。
//
// 节点身份（state-model §2.3）：领域身份 = 平台节点 ID（n_<ULID>），写入
// Swarm node label fleetly.node-id；Swarm node ID 仅存适配器映射
// runtime_node_refs(platform_id, swarm_node_id)。
//
// 底座访问经 DockerClient 小端口（docker.go）抽象，moby/client 实现见
// internal/substrate 适配器——为架构 §2.8 端口纪律打底，核心不出现
// 第三方类型。
//
// 本包不依赖任何框架类型（D20：框架只做装配与生命周期）；lynx.Service /
// lynx.Checker 的装配壳在 cmd/fleetlyd（CheckHealth 方法为结构性实现）。
package state

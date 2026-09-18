// Package compose 实现 fleetly 的 Compose 应用描述处理：解析、受控子集
// 校验、稳定归一化与归一化差异（plan/diff 的地基）。
//
// 契约真源（本包行为逐条对齐，不发明清单外语义）：
//   - docs/design/2026-09-17-architecture.md §2.4（受控子集支持/拒绝清单、
//     平台 label 约定、受管字段、变量插值关闭、plan/apply 语义）
//   - docs/design/2026-09-17-release-semantics.md §2.4（revisions 归一化
//     形态：env 按 key:sha256(value)+来源标注参与 spec_hash）与 §2.8
//     （受管字段政策：failure_action=pause、monitor≤5s、order 照用）
//   - docs/design/2026-09-17-stateful-placement.md §2.3（placement label
//     语法、constraints 命名空间、replicas×卷校验、label 一致性）
//   - docs/design/2026-09-17-state-model.md §2.4（fleetly.* 保留前缀 →
//     E_LABEL_RESERVED）
//
// 解析器选型（关键决策）：采用 github.com/compose-spec/compose-go/v2 官方
// loader 作为 **schema 载体**（compose 语法合法性、类型换算：时长/字节/
// 列表→映射 canonical 化），但 **受控子集校验完全自研**——受控子集是平台
// 契约而非 compose 全集，compose-go 的默认放行面远大于 §2.4 支持清单。
// loader 选项与理由：
//   - SkipInterpolation：`${VAR}`/`.env` 插值关闭（§2.4），归一化按字面；
//   - SkipInclude / SkipExtends：include/extends 在拒绝清单内，必须保留
//     原始键让本包校验显式拒绝（默认 loader 会先解析消解掉这两个键）；
//   - ResolvePaths=false：路径保持书写形态，保证 spec_hash 跨机稳定；
//   - SkipResolveEnvironment / SkipResolveLabels：env_file 与 label_file
//     不由 compose-go 合并——env 合并链（env_file < environment < 平台层）
//     需要保留 **来源标注**（release-semantics §2.4），本包自行解析
//     env_file（字面值、无插值，见 envfile.go）；label_file 不在支持清单，
//     由白名单拒绝。
//   - schema 校验保持开启：语法非法（未知键/类型错）→ 包装为
//     E_COMPOSE_UNSUPPORTED；其上再跑本包白名单/拒绝清单/受管字段校验。
//
// 归一化产物 Spec 是后续 revisions.compose_normalized 快照与 desired-hash
// 的地基：字段排序确定（服务/卷/域名/env 按字典序）、env 以 key+sha256
// 参与哈希（值永不落盘、永不输出）、spec_hash = sha256(canonical_json)。
//
// 本包不做的事（留待后续票，勿在此扩权）：
//   - apply 的真实执行（引擎层 T2.10）；省略=删除的卷数据例外语义
//     （T2.10）；平台 env_vars 合并层与 W_ENV_PLATFORM_OVERRIDE 告警
//     （env 票接入 DB 后）；危险字段的 admin 显式旁路 + 审计（本期只
//     「拒绝+错误」，旁路留 TODO）。
package compose

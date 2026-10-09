# ADR-0058: Managed Providers 排障面——组件实名露出与页名词裁决

日期：2026-10-09；状态：已采纳（随 IA v3 T6 实现同批落地）。

## 背景

IA v3（docs/design/2026-10-09-console-ia-v3.md）为受管组件增设平台域排障页。历史上 UI 刻意只用 "managed proxy" 泛称、不露组件实名，导致排障场景（日志断了、注册仓满了）全站没有入口与语义锚。2026-10-09 拍板：推翻泛称策略，实名露出（Traefik / zot / VictoriaLogs / VictoriaMetrics）。

同时发现初稿页名 "Components" 撞 CONTEXT.md 既有词条 **Managed Provider** 的 _Avoid_ 表（component 明确在禁）。按 ADR-0007 词汇纪律（更名即设计缺陷、词条不合走显式 ADR），本 ADR 一并裁决页名词。

## 决策

1. **实名露出**：Managed Providers 页及排障文案直接使用组件实名（Traefik、zot、VictoriaLogs、VictoriaMetrics）。"managed proxy" 泛称自本 ADR 起退役；泛称不是词条，无需 CONTEXT.md 删改。Routes 等产品语态页面的 "managed Traefik" 注记允许保留（注记位，非主体称谓）。
2. **页名采用既有词条 Managed Provider（页面复数：Managed Providers），不引入新词 "Components"**——既有词条语义精确覆盖（"由平台以普通 Workload 形式托管部署的 Provider 实例"），零冻结面改动；route 为 `/providers`，侧栏 FLEET 组入口同名词。初稿词 "Components" 废弃。
3. **CONTEXT.md 零新词条**：实名是 Provider 词条的具体落点（该词条本就以 "swarm、traefik、victorialogs…" 括注在册）；本批仅把 Registry/Logging/Metrics 词条的 Provider 括注补全实名（zot / VictoriaLogs / VictoriaMetrics），不新增、不删改 Avoid 表。
4. **一期诚实边界**：健康显示 unverified（GetStatus 恒 HEALTHY，provider Health() 接线属二期）；endpoint 不展示（config 文件唯源，无 API 通路）；VictoriaMetrics ingest 时效用 PromQL 时间戳探针（`time() - max(timestamp(...))`），VictoriaLogs 侧标 n/a（VL 查询代理属三期 ticket 面）。钉版数字是 provider 编译期事实（受管 Workload 镜像钉），标注 "pinned (provider)"。

## 不变量

- Capability/Provider/Managed Provider 词条语义不动；Avoid 表零改动。
- 词汇冻结执法面（禁词扫描）覆盖 Managed Providers 页全部文案。
- 通知渠道、平台备份等 fleet 级配置面不迁入本页（归 Settings）。

## 验收锚

- [x] `/providers` 路由与 FLEET 组入口名词为 Managed Providers（T1 翻闸接入）。
- [x] 四卡实名 + 角色 + pinned 版本 + unverified 徽标；VictoriaMetrics 卡含 ingest 时效探针（PromQL），VictoriaLogs 卡明示 n/a 与三期去向。
- [x] 全 console src 无 "managed proxy" 新增文案（禁词执法面既有测试覆盖）。
- [x] CONTEXT.md Registry/Logging/Metrics 词条括注补全实名，零新词条、Avoid 表零改动。

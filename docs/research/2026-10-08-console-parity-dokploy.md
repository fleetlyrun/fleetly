# Console 对齐 Dokploy 级 PaaS 的差距图谱与路线图（2026-10-08 调研）

## 0. 前提与地位声明

用户裁决（2026-10-08）：**Console 与 CLI 地位同等重要**。这是对 ADR-0044「minimal read-only console」的演进终点声明：全功能瘦客户端。两条不动摇的钉形：

- **零专属服务端面**（ADR-0044 决策）：Console 是公共 REST API 的纯静态消费面。能力补齐优先走「消费已有 API」，后端缺口才开 API 批——与 skills（ADR-0031「与 CLI/API 同能力面」）形成对称：**CLI / API / Console 三面同源同权**。
- **第一等地位的可执法定义**：CLI 每个动词组在 Console 有消费面（UI 无意义者显式豁免并记档），随批跑 `console:verify` 零漂移门禁。

对标物：Dokploy（主，README+docs 功能面）、Coolify（辅）。

## 1. 对标功能矩阵

| 能力 | Dokploy/Coolify | fleetly 后端/API | Console 现状 | 缺口归属 |
|---|---|---|---|---|
| 登录（用户名密码/2FA/会话） | ✅ 两者均有账号体系+2FA | ❌ token 单形态（凭证模型裁决）；SSO/2FA 在 Backlog | token 粘贴登录页（F3.1） | **L3 后端** |
| SSO（OIDC/GitHub） | Coolify Cloud 有 | ❌ Backlog | ❌ | L3 后端 |
| Projects 层级 | ✅（Dokploy 再分 environments） | ✅ projects + 蓝绿（environment 轴无显式面） | ✅ 项目/应用管理 | L3 可选（environment 轴裁决） |
| 应用部署多源 | git/dockerfile/image/compose | ✅ git / --from-dir / image / compose / spec_file / dokploy 导入 | ✅ 四源表单（image/compose/spec/upload） | **L1：git 源表单缺** |
| 部署日志实时流 | ✅ | ✅ builds logs（VL 回读+ring） | ✅（部署详情轴） | — |
| 回滚/取消 | ✅ | ✅ revisions + rollback + cancel | ✅ | diff 视图 L1 小缺 |
| Webhook 触发重部署 | ✅ | ✅ hooks（get/set/rotate） | ❌ | **L1** |
| 数据库生命周期+浏览 | ✅ | ✅ 五引擎 + browse（F3.6）+ 备份/恢复/校验 | 部分（创建/删除/browse/备份触发+列表） | **L1：verify/restore 面** |
| DB 备份到 S3 | ✅ | ✅ ObjectStore S3（ADR-0042，config 面） | 同左 | L2 可选（目的地管理 UI） |
| 监控图表（per-service CPU/内存） | ✅ 两者均有 | ✅ VM+cadvisor+`metrics query`（QuerySeries 单序列首形态，多序列已预留「Console 批扩展」） | ❌ | **L2（后端小批伴生）** |
| 告警+通知渠道 | ✅（Slack/Discord/TG/email） | ✅ alerts rules + channels（webhook 形态；test/不可达诚实） | ❌ | **L1**；部署成败→通道接线 L3 |
| 一键模板 | ✅（Dokploy 72+/Coolify 300+） | ✅ templates（builtin+外部目录） | ✅（F3.3） | 量随目录，非代码缺口 |
| cron 定时任务 | ✅ | ✅ schedules | ✅（Tasks 页 schedules tab） | — |
| 常驻服务池 | ✅（services） | ✅ runs（resident pool） | ✅（Tasks 页 runs 观察+stop） | scale/renew 入口待补（L1 小） |
| 多节点管理 | ✅（Swarm 多服务器） | ✅ nodes（list/enroll/relay 状态/drain/cordon） | ❌ | **L1** |
| 域名/TLS 证书状态 | ✅（LE 自动） | ✅ routes + tls auto（LE 经受管 traefik） | ✅ routes tab（create/delete） | L1 小：证书状态显示 |
| Docker 容器级管理 | ✅（Dokploy container 列表操作） | ❌ 载体哲学：平台中介（exec/logs/事件承载运维） | — | **不追随**（见 §4） |
| 用户/团队/角色管理 | ✅ | ✅ users/teams/roles（F0.5-F0.7 全量） | ❌（仅 tokens） | **L1** |
| API/CLI | ✅ | ✅ 双形态 golden 钉死 | —（自身即参照） | — |
| Git PR 预览部署 | ✅（Dokploy preview deployments） | ❌ Backlog「GitLab/Gitea 深度集成与 PR 预览」 | ❌ | L3（先后有序：先 git 源 UX，后 preview） |
| 私有 Registry 管理 | ✅ | ✅ 受管 zot per-Project（ADR-0036） | 平台托管零配置 | —（差异化：更强） |
| 蓝绿/Revision 模型 | ❌（Dokploy 无一等蓝绿） | ✅ 冻结 Revision + 蓝绿窗 | ✅（F3.1 卡片） | —（差异化：更强） |
| 双 Runtime（swarm/k3s） | ❌ | ✅（ADR-0052..0056） | nodes 面随 C 批 | —（差异化） |
| 平台自备份/灾备 | Coolify 有（自身） | ✅ Platform Backup + KEK 轮换 | Settings 触发 | **L1：列表/校验面** |

## 2. 三层缺口清单

**L1 — Console-only 缺口**（后端已在，纯消费面，均为小批）：

1. 身份管理页：users（create/invite/accept/list）、teams、roles——RBAC 的 UI 面。
2. Alerts 页：rules / channels（create/test/delete，test 不可达诚实呈现）。
3. Nodes 页：list（available 过滤、relay_online、runtime 形态）+ enroll 材料展示（RelayCommand 复制）。
4. Hooks UI：get/set/rotate（per-App webhook）。
5. 部署表单 git 源（后端 `deploy` git 源已在；表单补 URL+branch+subdir）。
6. Revisions diff 视图（CLI `revisions diff`；详情页列表已索引 revision）。
7. Databases verify / restore 面（`databases verify`；restore=recreate `--restore-from-backup` 流程 UI）。
8. Platform backups 列表+校验面（Settings 现只有触发）。
9. dokploy 导入面（CLI `create-from-dokploy --file` 已在——妙手：对标物用户迁移通道）。
10. Runs scale/renew 入口、Routes 证书状态列、builds 独立列表（或确认详情轴足够后豁免记档）。

**L2 — Console 批伴生的小后端批**：

1. `QuerySeries` 多序列（metrics 图表数据面；ADR-0041 验收注记已预留「Console 批扩展」）。
2. （视 C2 定）per-workload 资源序列的窗口聚合面。

**L3 — 真后端缺口**（独立批/Backlog 升格）：

1. **凭证第二形态**：密码会话（users 表已在，加 password hash + session 面 + Console 登录页双形态）或 OIDC SSO；2FA 随后。Backlog「SSO/2FA」升格。
2. **Git 深度集成**：repo webhook auto-deploy → PR 预览环境（Backlog 升格，依赖序在 git 源 UX 之后）。
3. **部署成败→通知通道接线**（channels 现承载 alerts；deploy 生命周期事件接入是产品裁决）。
4. （可选）environment 轴：projects 内 dev/staging/prod 分层——或裁决用现有多 project + 蓝绿表达，不追 Dokploy 形态。

## 3. 分批路线图（建议序）

| 批 | 内容 | 性质 |
|---|---|---|
| **C1 可观测与告警** | Metrics 图表页（L2 多序列伴生）+ Alerts 页 + Events/L-Logs 既有面收口 | 纯消费+小后端；用户价值最直观 |
| **C2 身份与治理** | 身份管理页（users/teams/roles）+ Hooks UI + platform backups 完整面 | 纯消费 |
| **C3 部署 UX 深化** | git 源表单 + revisions diff + dokploy 导入 + runs 入口补齐 + Routes 证书状态 | 纯消费 |
| **C4 节点面** | Nodes 页 + enroll 材料展示 | 纯消费 |
| **C5 数据面深化** | DB verify/restore + S3 备份目的地展示 | 消费+config 面 UI 化裁决 |
| **C6 凭证第二形态** | 密码会话或 SSO（ADR 裁决）+ 登录页双形态 + 2FA 预留 | 后端批+Console |

每批随批纪律：新页面 golden/组件测试、`console:gen && console:build` 同 commit、CLI-Console 动词对照表更新（第一等地位执法面）、staging 浏览器级走查补档。

## 4. 不追随项（诚实边界）

- **Docker 容器级 CRUD 管理**：与载体哲学冲突（平台中介、spec 驱动），exec/日志/事件已覆盖运维面；Dokploy 的 container 列表操作不搬。
- **i18n**：用户可见文本英文是仓级约定（AGENTS.md），Console 文案维持英文单语。
- **多租户 Cloud 形态**（Coolify Cloud）：fleetly 是自宿单租户窗口，多租户隔离另有前置面（ADR-0036 挂账）。

## 5. 待裁决点

1. **C6 认证形态**：密码会话（最小自洽）vs OIDC SSO（对接现有 IdP）vs 两者先后——建议先密码会话后 SSO，或若已有 IdP 直接 SSO。
2. **C1 起步确认**：路线图序以「用户价值直观度」排（监控/告警先）；若以「dokploy 迁移用户首触面」排则 C3 先（git 源+dokploy 导入）。
3. **environment 轴**做不做（L3-4）。

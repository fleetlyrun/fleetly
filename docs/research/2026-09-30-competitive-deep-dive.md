# 竞品深调报告（DX 透镜）——2026-09-30

背景：约束面重置（ADR-0010）后，Dokploy 地板废止，硬约束只剩三条（轻量 PaaS + 人类/Agent 一等、torchwood 一期 dogfooding、**DX 第一权重**）。本报告以 DX 为第一透镜重估一切，替代原"Dokploy 地板"作为功能取舍的输入。

方法：五路并行调研——dokploy / coolify / caprover+zane-ops / tsuru+porter 本地代码深潜（产品功能面，非架构面）+ 全景口碑网络调研（HN/GitHub/定价页，2025-04~2026-09 为主窗口）。结论均带代码或来源佐证；本地仓证据以路径标注。

---

## 1. 一页结论

1. **2026 年自托管轻量 PaaS 的 table stakes 已抬高**：自动 TLS、git 自动部署、一键数据库+S3 备份、面板+日志流、模板库、**零停机部署**（已从差异化滑入基本盘）、基础指标+告警。低于 table stakes 是 bug 不是"少个功能"。
2. **全行业公认的两个大空白恰好是 fleetly 的机会**：① **晋升（promote）**——dokploy/coolify/zane-ops 三家都有 Environment 实体、**没有一家有一等 promote 原语**（staging→prod 全靠手工复制配置）；② **预览环境**——自托管侧只有 dokploy 有（还出过泄密漏洞），托管侧是付费层标配。
3. **"回滚=不可变快照重放"被四家独立验证**：dokploy rollback fullContext JSONB、zane-ops service_snapshot+任意历史 redeploy、tsuru AppVersion、porter Installation→Run 台账。fleetly 的 Revision Replay 设计方向正确，且没有任何一家把它做成跨环境晋升的载体——这就是差异化切入口。
4. **CLI+Skills 路线拿到强外部证据**：Railway 2026-05 归档独立 MCP server 改为 `railway mcp` 并入 CLI（CLI 是交付本体、MCP 退化为传输选项）；Dokploy 官方 MCP **508 个工具**被迫做 preset 收敛；coolify MCP 44 工具仅 3 个写工具（实质是精心设计的只读面）；HN 共识"gh CLI 比 MCP 省 token"。Fly.io 内部结论："**agent 在事物显式时工作得最好**，人类 DX 反而靠隐藏复杂性"——直接支撑 fleetly 的显式 CLI 契约哲学。
5. **信任是下一个产品面**：Coolify 2025-11 自动更新注入构建参数弄坏全社区层缓存、Dokploy 预览环境泄密拖 6 个月 + Swarm 硬编码数据库密码。"升级永不弄坏你的东西、备份必带恢复演练"可以是 fleetly 的 headline 功能。
6. **没有直接对位竞品**：最接近的 Nixopus（"fully agentic"）走聊天式运维 + LLM 依赖 + Docker Compose 锁定，恰是"契约化可操作"的反面。Coolify v5 押注可扩展性（官方承认现状不足），多节点是公认的下一代竞争轴。

## 2. 市场与口碑要点

- **Heroku 2026-02 进入维持性工程模式**（不签新企业客户、史上最大裁员），社区共识=慢动作关停；Railway 2026-02/05 两连故障（面板谎报 Online、无通知）；Fly.io 2026-07 换帅融资转向 agent VM（Sprites）。托管侧的动荡持续把用户推向自托管。
- **自托管侧**：Coolify（62.4k★，品类之王，v5 重构押注可扩展性）；Dokploy（37.6k★，高速增长但安全/支持/许可证三连争议）；Dokku（32.2k★，git push 纯粹主义老钱，CLI-only 无面板）；CapRover（15.2k★，闭源化后心智明显衰退）；Easypanel（闭源按服务器收费，稳但功能少）。
- **高频抱怨 = 机会清单**：升级引入回归（Coolify 缓存事故）、备份救不了命、多节点不成熟（"严肃工作负载不能跑 Swarm"）、代理排障黑箱、零停机后进者被罚、安全翻车、资源占用（Dokploy 高到有人专门做了 1GB 服务器部署器 Graft）。
- 长尾新玩家（Mist/Cygnus/Deeploy/Senate/Graft…）验证需求存在、也验证没人跑出来。

## 3. 六参考物 DX 记分卡

| 项目 | 最强面 | 最弱面 | 对 fleetly 三句话 |
|---|---|---|---|
| **dokploy** | 上手路径（1 命令装机 + sslip.io quickstart 2 分钟首部署）；PR 预览部署接近 Vercel；通用部署 webhook token；Patch 仓内文件覆盖层 | 无晋升无 diff；核心 DX 权限锁企业版；回滚绑 registry；无幂等 API | 偷内存队列（per-server 分区+同服务 FIFO+可配并发）、回滚快照、Patch 思想、watchPaths+tag 触发 |
| **coolify** | 部署流水线防御性工程（admission control/去重/queue_full/cancel）；预览部署全生命周期（fork 安全门/commit status 回写）；默认值链（装机→sslip.io→LE→密码自动生成）；386 模板+CDN | 程序化面 UI>API>MCP 三级衰减（API 只能建 5 种形态）；Livewire 漏斗+2s 轮询；迁移功能写完不敢上生产；回滚=本机镜像列表 | 偷队列 admission 语义、"为什么没滚动更新"写进部署日志、模板 schema（SERVICE_* 语义变量）；避免 proxy 枚举式多后端 |
| **caprover** | captain-definition 4 行即部署 + Dockerfile 回落；one-click 市场+CDN+私有源；逃生舱密度（serviceUpdateOverride/customNginxConfig）；nginx 校验失败自动回滚 | 非 GET 全局互斥 429；webhook fire-and-forget；观测近零；无服务端回滚 API；无泛域名 TLS | 偷"零声明文件部署"的入门体验与逃生舱哲学（但逃生舱进 Spec 而非绕过 Spec）；反面教材：全局锁与不可观测触发 |
| **zane-ops** | 草稿→应用全链路（字段级 diff/单条撤销/快照冻结）；蓝绿 slot+per-deployment URL+**pause_at_step 任意步骤暂停**；HttpLog 请求级日志；Token 三维授权+凭据卫生模范 | Temporal 全家桶 11 单元起步；默认域名带随机后缀；单机假设写死；Service/ComposeStack 双轨心智 | 偷草稿-diff-撤销、pause_at_step、detected-ports 自动发现、zn_tok_ token 卫生、create-from-dokploy 式迁移钩子 |
| **tsuru** | 长操作"流式+事件 ID 当场返回+事件内嵌日志回放"；构建现场教育（进程来源归属/deprecated 字段警告）；事件封禁（change freeze）；env managed-by 声明式卫生 | Mongo 硬依赖+5 组件足迹；API 层积幽灵功能；日志默认内存不持久 | 偷 X-Eventid 模式、streamfmt 构建日志排版、事件封禁做 agent 刹车、did-you-mean CLI 框架件 |
| **porter** | Installation→Run→Result→Output 台账（ULID/参数摘要/日志全档）；schema 自描述合成（mixin GetSchema + `#/mixin.NAME/` 改写）；check-strategy 四档；`porter explain` | 非 PaaS（CNAB 打包工具），产品面不适用 | 偷 Run 台账做部署历史、`fleetly explain/schema` 服务 agent、三层护栏（schemaVersion+状态机守卫+封禁） |

## 4. 七主题跨产品结论

**A. 上手与首部署**：赢家通吃的公式 = 一行安装 + 零 DNS 默认域名（sslip.io）+ quickstart 一键样例 + 自动 TLS + 密码自动生成（dokploy/coolify/zane 三家趋同）。反面：coolify 全 UI 无 CLI 初始化路径（agent 不友好）；caprover 证书签发靠 sleep(7s)。
**B. 部署与变更模型**：高阶形态共识 = 结构化变更记录（zane DeploymentChange：改不发布/diff/单条撤销）+ 不可变快照 + 一等回滚动词。全行业缺口 = 部署间 config diff（dokploy/coolify 都没有）。fleetly 的 Revision 天然自带 diff 面（Spec 对 Spec），是免费优势。
**C. Agent/API 面**：全部六家无幂等键、无统一事件流。tsuru 的事件 ID+可取消是孤例佳品。coolify 的 MCP 实质是只读观察面+3 个写工具，且 env/日志永不返回（省 token 取舍牺牲 debug）。程序化能力普遍呈 UI>API>MCP 衰减——**"三面同等能力"本身就是差异化**。
**D. 环境与晋升**：dokploy/coolify/zane 都有 Project→Environment；变量分层（project→env→app）是成熟实践；**promote 全行业缺失**（dokploy 的 duplicate 不复制服务；coolify 只有逐资源 move/clone）。预览环境三家形态：dokploy per-PR 域名+PR 评论、coolify 全生命周期+fork 安全门、zane PreviewEnvTemplate（TTL+自动销毁+克隆策略）。
**E. 观测**：zane HttpLog（字段级请求日志+facet 查询）是独有卖点但靠 fluentd+loki+PG 11 容器堆出来；coolify Sentinel+流量分析+6 通道告警但无聚合检索（要外排 logdrain）；tsuru 日志默认内存。**"CapRover 的体量 + Zane 的观测深度"正是 fleetly 插件化观测瞄准的位置**。
**F. 信任与安全**：token 卫生最佳实践 = zane（前缀便于 secret scanning、sha256 存储、过期、last_used_at 节流、constant-time、未标 scope 一律拒绝）；coolify 的 member 持强 token 403 防御。平台自身可升级性（Coolify 事故）与备份可恢复性（"备份不是能救命的能力"）是社区最痛的两点。
**G. 多节点**：coolify 承认不足（v5 押注）、dokploy Swarm 口碑一般（迁移 transfer.ts 601 行且体验糙）、zane 单机写死。**多节点成熟度 = 下一代竞争轴**，fleetly 的插件化运行时叙事应讲成"多节点不翻车"，不是架构洁癖。

## 5. MCP 证据专节（对"CLI+Skills、不做 MCP"的检验）

支持性证据（占压倒多数）：
- **Railway 2026-05-23 归档独立 MCP server**，改为 `railway mcp` 并入 CLI——CLI 成交付本体，MCP 退化为传输选项。
- **工具爆炸有硬数据**：Dokploy 官方 MCP 508 工具/49 类，README 自认"超大工具列表让客户端更慢更不可靠"被迫做 preset；社区 coolify-mcp 42 工具光列表吃 ~8300 token，还得配 evals 防模型选错。
- **Token 成本是 HN 第一抱怨**："gh CLI 比 MCP 省得多"；"不如临时文件+管道"；模型变强后"MCP 价值递减，agent 越来越会用 API/CLI"。
- 真实迁移样本：jira MCP → jira skill → 直接 acli。Skills 承接方（Anthropic Skills）被普遍当 MCP 轻量替代。

反方（保留 awareness）：非技术终端用户场景成立；企业治理/爆炸半径控制优于裸 CLI。**fleetly 的对应物**：scope token + tsuru 式事件封禁 + porter 式"读默认开放、写显式授权"闸门，用契约面获得治理能力而不背 MCP 包袱。

## 6. 替代"Dokploy 地板"的功能取舍原则（提案，待用户裁定后入 ADR）

1. **地板=2026 table stakes 全齐**（§1.1 清单）：低于基本盘按 bug 处理，进入 N 批次的必选项。
2. **天花板=无硬天花板，逐项过 DX 权重**：新功能三问——服务三条硬约束吗？DX 增益多大（对人/对 agent 各答一次）？实现重量多大（单二进制+自宿轻组件可承载吗）？
3. **差异化押注五项**（竞品流血面 × DX 杠杆最大）：① **promote/晋升**（全行业空白，Revision 跨环境重放）；② **预览环境**（自托管最大空白）；③ **Agent 契约**（幂等+事件流+explain/schema+Skills，三面同等能力的独占位）；④ **信任即功能**（平台自身原子升级可回滚、备份带恢复演练、零硬编码共享密钥）；⑤ **多节点成熟**（插件化运行时的产品化叙事）。
4. **轻量重述**：红线不再是固定数字预算，而是"控制面单二进制 + 受管能力组件单核自宿 + 实测校准"；超出即触发重估。

## 7. 对现有设计的重估清单

| 设计项 | 原状态 | 重估结论（DX 透镜） |
|---|---|---|
| **Environment 实体** | 我曾建议砍掉（deletion test 论证） | 调研中一度反转为"保留并升格"（三家竞品都有该实体、变量分层是成熟 DX、预览环境空白、promote 缺口）。**2026-09-30 用户终裁（定位=小微团队）：否决保留，砍掉实体**——成本由全体用户支付而收益归少数；环境即项目 + 两级变量，promote 后置为跨项目原语，Spec 目标无关对冲（ADR-0011）。本行竞品证据留作重开触发时的参考 |
| 回滚=Revision Replay | 参考默认 | **强确认**（四家独立验证同构设计），并升格：Revision 是 diff 面（竞品全缺）与 promote 的载体 |
| 状态语义四件一拍/单写者 | 参考默认 | **确认**，补强：写操作当场返回事件 ID（tsuru X-Eventid 模式）、部署日志归档进事件可回放、队列 admission 语义（去重/queue_full/可取消） |
| webhook+拉源单轨 | 参考默认（ADR-0009 已标注待重估） | **维持单轨但补 Agent 轨**：通用部署 token URL（dokploy refreshToken 形态）+ 镜像/上传三轨不变；git push 收包面仍不做（六家中仅 dokku/caprover CLI 走上传，主流是 webhook） |
| Compose 受控子集 | 参考默认 | **维持**，补 DX：拒绝时给精确原因与建议（不是静默失败）、zane detected-ports 式端口自动发现、归一化 diff 预览 |
| Agent 面=CLI+Skills | 硬约束 | **强验证**（§5），补充三件套：`fleetly explain`/`fleetly schema`（porter 模式，能力自描述服务 agent）、事件封禁/change freeze（agent 刹车）、token 卫生全套（zane 模式） |
| HA 口径 | 参考默认 | **维持**（无新反证；coolify v5 才开始做多节点反而印证节奏） |
| 部署状态机 | 设计稿 | 补：pause_at_step 调试暂停（zane）、蓝绿 per-deployment URL 先验后切、pause/回滚一等 CLI 动词（caprover 反面教训） |
| 上手体验 | 未成文 | **新增硬要求**：一行安装 + sslip.io 零 DNS + `fleetly quickstart`（2 分钟首部署）+ 默认值链（TLS/密码自动生成）+ **CLI 可完成全部初始化**（对 agent 友好，coolify 反面） |
| 模板生态 | 未设计 | 新增：模板=compose+语义变量（coolify SERVICE_* 模式）+ CDN 热更新 + 竞品迁移钩子（zane create-from-dokploy 模式） |

## 8. 定位建议（三句）

1. **"契约化可操作，而非聊天式运维"**——与 Nixopus 一句话差异化；CLI/Skills/幂等/事件流是产品本体。
2. **"升级永不弄坏你的东西"**——信任即功能，打 Coolify/Dokploy 流血面。
3. **"Hetzner + fleetly，人和 agent 都能用"**——接过 "Hetzner + Coolify" 的迷因槽位；dogfood torchwood/messageloop 公开跑。

## 9. 来源

本地深潜证据见各节内嵌路径（dokploy/coolify/caprover/zane-ops/tsuru/porter 仓内）。网络调研主要来源：
- HN：Coolify 线程（43555996/45480506）、Railway 全局故障（46976466）、Heroku 维持模式（46913903）、Fly.io 转向（49051369）、Ask HN: Who is using MCP in production（49548600）、Dokploy sweet spot（44884077）、预览环境漏洞（44548952）、许可证变更（46715953）、Mist（46669045）、Flightcontrol（45488441）、开源 Railway 需求（46976989）、Migrating off Coolify（47547705）、Kamal 吐槽（43639026）
- GitHub：coollabsio/coolify（v5 公告 #5685、缓存事故 #7040）、Dokploy/dokploy + Dokploy/mcp（508 工具）、dokku/dokku、caprover/caprover、basecamp/kamal、nixopus/nixopus、StuMason/coolify-mcp（~8300 token 工具列表）、railwayapp/railway-mcp-server（已归档并入 CLI）、CyPack/dokploy-mcp-skills（Skills 包装实践）
- 厂商：Railway/Fly/Render/Northflank/Porter/Easypanel/Dokploy 定价页；Cloud 66《The PaaS Graveyard》

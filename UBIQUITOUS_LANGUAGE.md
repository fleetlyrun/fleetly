# fleetly 词汇表（Ubiquitous Language）

> 2026-09-19 命名审查产物：核心概念的语族一致性审计 + 领域词汇钉死。
> 语族缩写：**拉**=拉丁/法语系 · **日**=日耳曼系 · **希**=希腊系 · **混**=混合词源。

## 发布生命周期

| 词条 | 定义 | 语族 | 别名（避免） |
| --- | --- | --- | --- |
| **deploy** | 一次部署：compose 入队到终态的全链 | 拉 | apply（CLI 面已废）、release（仅指切流阶段） |
| **release** | 发布阶段：任务替换到健康门的切流窗口 | 拉 | rollout |
| **rollback** | 平台按版本快照的单层重放回退 | 混（roll 拉 + back 日） | revert、downgrade |
| **replay** | 同一快照内容的再次部署（幂等，通常零任务变动） | 混（re 拉 + play 日） | redo |
| **restore** | 灾难恢复：从备份快照重建控制面状态 | 拉 | recover |
| **revision** | 已验证的版本快照（归一化 compose + 覆盖层，保留 5 版） | 拉 | version、snapshot（快照泛称，勿代指 revision） |
| **verdict** | 部署终态的稳定性判定（healthy/unstable/degraded…） | 拉 | outcome、result |
| **drift** | 运行域与期望态的偏离（检测默认开） | 日 | divergence（保留 drift——GitOps 事实标准词） |
| **converge** | 漂移的显式收敛动作（per-app opt-in） | 拉 | sync、heal |
| **reconcile** | stack 对账：底座实况与期望态的增删改核对 | 拉 | 对账的英文正式词；sync |

## 时间性安全层（L1/L2/L3）

| 词条 | 定义 | 语族 | 别名（避免） |
| --- | --- | --- | --- |
| **health gate** | L1：任务健康才许切流（Traefik 不查健康，控制面补位） | 日 | readiness（指探针，勿混用） |
| **watchdog** | L2：发布看门狗（releaseTimeout，含 PENDING/停滞） | 日 | timeout 泛称 |
| **observe window** | L3：切流后观察窗（默认 60s 只告警） | 拉 | cooling、soak |

## 触发与数据流

| 词条 | 定义 | 语族 | 别名（避免） |
| --- | --- | --- | --- |
| **trigger** | 部署入口：git push(SSH) 或 webhook 两条 | 日（借拉丁 trigga） | — |
| **delivery** | 一次 webhook 投递（ID 是防重放键） | 拉 | — |
| **fetch** | 从远端仓库拉对象到平台 bare 仓库 | 日 | pull（pull 专属镜像拉取） |
| **source**（git） | 应用的代码来源配置（URL/分支/认证） | 拉 | remote（指 git 语义的远端） |
| **source**（logs） | 日志行的产生端：`container` \| `build` | 拉 | producer、origin |
| **source**（env） | 环境变量的层来源：`env_file` \| `environment` \| `platform` | 拉 | layer 单独指层序，source 指来源标记 |
| **source_git**（deployments） | 部署产地的 git 溯源列（sha/ref） | 拉 | — |
| **follow** | 日志实时跟随流 | 日 | tail |
| **watch** | 事件流订阅（seq 游标） | 日 | subscribe |
| **history** | 日志/事件的落盘历史检索 | 希→拉 | archive |

## 状态与信任

| 词条 | 定义 | 语族 | 别名（避免） |
| --- | --- | --- | --- |
| **ledger** | 台账：append-only 的登记表（domains/backups/certs） | 拉 | 表名直译、record |
| **audit** | 审计：与业务写同事务的不可抵赖记录 | 拉 | log（泛指日志，勿混） |
| **event** | 事件流的单条广播（seq 单调，游标续读） | 拉 | — |
| **cursor** | 流式读取的位置令牌（410 过期） | 拉 | offset |
| **prune** | 按保留期的删除（janitor/backups） | 拉 | purge |
| **sweep** | 周期扫描轮（**仅** ingress 收敛/续期扫描语义） | 日 | 见 flagged ①：缓存过期删除不得称 sweep |
| **backup** | 控制面状态快照（VACUUM INTO + 回读校验入台账） | 日 | dump |
| **envelope** | age 信封加密形态 | 拉 | — |
| **token** | Bearer 凭据（scope 三级） | 日 | key（key 专指 age 主密钥） |
| **principal** | 通过鉴权的调用方身份 | 拉 | — |

## 入口与放置

| 词条 | 定义 | 语族 | 别名（避免） |
| --- | --- | --- | --- |
| **ingress** | 平台自管的 Traefik 入口层 | 拉 | gateway（专指 grpc-gateway） |
| **route** | 下发给 Traefik 的单条路由 | 拉 | rule |
| **entrypoint** | Traefik 监听面（web/websecure） | 拉 | listener |
| **challenge** | ACME 验证挑战（HTTP-01 经反代） | 拉 | — |
| **placement** | 放置域：服务落哪个节点的决策 | 拉 | scheduling（K8s 语，勿借） |
| **pin** | 有卷应用钉住到本机节点（不迁移） | 日 | — |
| **binding** | 钉住的登记关系（placement binding） | 日 | constraint（专指调度约束编译产物） |
| **anchor** | 节点身份锚写（平台 ID → Swarm label） | 拉 | stamp |

## 审查结论（2026-09-19）

**input/sink 型「同一概念对混族」反例：不存在。** 全库 `sink`/`upstream`/`downstream`/`producer`/`consumer` 零命中；`input` 仅 9 处且全部是 `placement.Input` 参数结构（与 `Decision` 构成代码层 in/out 对，自洽）；`output` 53 处全部是 CLI help 文案与局部变量，非领域概念。

**Flagged ambiguities（按重要性）：**

1. **sweep 双义**〔已改（2026-09-19）〕：`gitserver.deliveryCache.sweep` 已更名 **expire**（TTL 惰性过期删除）；sweep 专指周期扫描轮（ingress 收敛/续期）。
2. **source 四义**〔部分已改（2026-09-19）〕：git 来源（source_url）/日志来源（container|build）/env 层来源/部署溯源（source_git_*）四义仍并存——各自有 enum 与字段前缀限界，属可容忍多义；`gitserver.Source` 聚合门面已更名 **GitTriggers**（构造器 `NewGitTriggers`，装配 provider 同名），名实相符。
3. **drift/converge 混族对**〔有意让位〕：反义对语族不对称（日/拉），对称替代 `diverge/converge`（双拉）被否——drift detection 是 GitOps 全行业词汇，行业词优先于词源对称。
4. **rollback 混合词源**〔接受〕：roll（拉）+back（日）。行业标准，不动。
5. 双语对照钉死：台账=ledger（21 处已一致）、对账=reconcile、切流=switch flow（switch traffic）、锚写=anchor、钉住=pin。

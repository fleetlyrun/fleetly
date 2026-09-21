# E3 对象存储（S3 外部端点 + RustFS opt-in）专项设计

| 状态 | 日期 | 说明 |
|---|---|---|
| **裁决轮完成（2026-09-21，用户三裁）**：D-S3-9 = 内网 + **可选公网子域 s3.\<base\>**（W3 实现开关）；D-CR-1 = robfig/cron/v3；D-S3-2 = 库内运行期设置 | 2026-09-21 | W3 = E3（本文）+ E5 Cron（细则已冻结于 [architecture §4.3](2026-09-17-architecture.md)，本文 §8 只补契约面与落点，不重开细则）；裁决输入：[v0.2 规划 §2 W3 行](../plan/2026-09-20-v0.2-plan.md)、V2-2（RustFS opt-in 用户直裁）、D4（S3 外部端点，奥卡姆复议后形态）、FZ-7（conformance 记 N/A v0.1，随本票落地）；zane-ops 对照不涉对象存储 |

## 1. 现状与问题

1. **状态备份只有本地单份**：`statebackup` 产物落 `<backup.dir>/<ULID>/`，主密钥分离、sha256 manifest、回读校验齐备——但目录在控制面主机本地（[drill-l1 §6 如实声明](../runbooks/drill-l1-2026-09-18.md)：「异机上传随 v0.2 S3 目标」）。主机盘损/误删 `rm -rf` 即全损。
2. **minio-go 已裁未用**：依赖复核轮落定 minio-go 为 S3 客户端，但 v0.1 代码库零使用、go.mod 未引入（FZ-7 因此记 N/A）。
3. **restic 只存在于文档**：rebind 迁移 runbook（multi-node §2.8）让用户自选 restic 镜像手工搬卷数据，平台未捆绑、未钉版。
4. **应用无对象存储故事**：目标画像「web + worker + cron，依赖对象存储」（architecture §1.2 行业共识行）——v0.1 用户要么自带外部 S3 凭证手填 env，要么没有。
5. **RustFS 1.0 已 GA**（2026，Apache 2.0，`rustfs/rustfs` 多架构镜像）：V2-2 用户直裁引入为 opt-in 管理组件，给「不想注册云账号」的用户一个开箱即用的 S3 兼容端点。

## 2. 目标设计

### 2.1 provider 抽象（internal/objectstore）

新包 `internal/objectstore`：S3 兼容端点的平台唯一客户端面（minio-go 封装，其余代码不直接 import minio-go）。（2026-09-21 实现修正：包名原稿 `objstore`，AGENTS.md 命名规则裁定缩写不可接受，落为 `objectstore`。）

- `Endpoint` 描述符：`URL`（scheme+host+port，如 `https://s3.amazonaws.com` 或 `http://rustfs:9000`）、`Region`、`Bucket`、`AccessKey`/`SecretKey`（明文只在内存存活，持久层走 envelope，见 §2.2）、`PathStyle`（bool；RustFS/MinIO 类自建端点必须 true，AWS 虚拟主机式 false）。
- 操作面 = 平台实际用到的封闭集：`EnsureBucket`（缺省建桶，幂等）、`Put`/`Get`/`Stat`/`Delete`/`List`（前缀列举）。不做 presign/multipart/生命周期等大面——**操作面即 conformance 面**（§2.7）。
- `TestConnection`：**put→get→delete 一枚探针对象**（`fleetly-probe-<ULID>`，比对写入字节），不是 HeadBucket——诚实契约：测试通过 = 「能认证、能写、能读回」，不是「TCP 通」。返回结构化结果（endpoint 回显脱敏 + 各步耗时 + 失败步与底层错误码）。

### 2.2 配置面：库内运行期设置（新 platform_settings KV）

S3 端点配置**不落 config.yaml**，落 SQLite 运行期设置（新表 `platform_settings(key TEXT PRIMARY KEY, value TEXT, updated_at)`，迁移随票）：

- **理由**：目标用户在 Console Settings 页配端点→点「测试连接」→保存即生效（含 RustFS 开关触发部署），不应要求 SSH 改 yaml + 重启 daemon（V2-1 易用性直裁）；与 domains/env/tokens 同为「用户运行期资源」类，zot/traefik 镜像类静态项才留 config.yaml。
- **设置键**（词表只增）：`s3.mode`（`unset`｜`external`｜`rustfs`；缺省 unset）、`s3.endpoint_url`、`s3.region`、`s3.bucket`、`s3.access_key_id`、`s3.secret_access_key`（envelope 加密存储——沿用密钥库封套模式，明文不进库）、`s3.path_style`、`s3.public_exposed`（bool，仅 rustfs mode 可开，见 §2.6）。
- **互斥校验（fail-fast）**：`mode=external` 时 endpoint_url/bucket/两钥必填；`mode=rustfs` 时这四项必须为空（配了即 `E_S3_CONFIG_CONFLICT`，拒绝保存）——托管与自管是两种互斥事实源，静默覆盖会制造「凭据到底哪来的」悬案。
- 变更路径：API 写入 → 审计 `s3.updated` → 备份上传轨与新部署立即消费新值（读取点在每次备份/每次部署装配时取当前值，不缓存长驻）。

### 2.3 状态备份上传轨（restic）

本地备份核**一字不动**（VACUUM INTO → 回读校验 → manifest → 台账，T2.22 信任闭环），上传是 verify 之后的**追加步**：

- **执行形态（D-S3-3）**：restic 以**钉版容器一次性执行**——fleetlyd 经 Docker API `create→start→wait→rm`，镜像 `restic/restic:<版本>` 钉 digest（Go 常量 + image-prepull 台账双锚，zot 同款纪律）。restic 无库形态、fleetlyd 不嵌二进制、供应链口径与平台其余镜像一致。
- **上传语义**：`restic backup <本地备份根目录>` → repo `s3:<endpoint>/<bucket>/statebackups`（两种 mode 同一路径形态，外部端点与 RustFS 无分叉）。repo 口令平台生成（32B 随机）存密钥库条目 `s3.restic_password`——**与 fleetly.key 分离**（口令泄露不暴露数据、主密钥泄露不暴露远端；恢复取回路径进 runbook）。
- **回读校验（诚实契约延伸）**：上传后 `restic snapshots --json` 必须列出该次快照 ID 才记 ok——「绿色成功但实际没上传」的路径结构性不存在（与本地 verify 同型纪律）。
- **台账**：`state_backups` 行增列 `upload_status`（`none`｜`ok`｜`failed`）、`uploaded_at`、`upload_error`（迁移随票）。上传失败：行记 failed + 事件 `backup.upload_failed` + system status backup 组件不健康（红）——本地份仍有效，DR 缺口如实可见。
- **保留**：本地 keep 不变；远端 `restic forget --keep-last <keep>` 与本地份数对齐（同一备份周期尾部执行，失败只告警不回滚本地）。
- **触发面**：全部四类触发（daily/post_deploy/manual/pre_upgrade）都上传——restic 去重使 post_deploy 高频上传成本可忽略。
- **恢复**：runbook backup-restore.md 增 §「从 S3 恢复」：`restic restore latest --target <临时目录>` → 得到备份目录 → 进现有 §4 流程（本地份优先，远端是主机盘损时的路径）。

### 2.4 应用凭证注入（fleetly.s3=true + source=system env）

compose 服务 label `fleetly.s3=true` → 发布引擎为**该服务**注入一组 system env（词表见 §5.4）。这是 env 三层合并链预留 `source=system` 的**第一个生产者**（FZ-1 数据库连接串在 W4 走同一机制）：

- 注入键：`S3_ENDPOINT`（含 scheme；rustfs mode = `http://rustfs:9000`）、`S3_REGION`、`S3_BUCKET`、`S3_ACCESS_KEY_ID`、`S3_SECRET_ACCESS_KEY`、`S3_PATH_STYLE`。
- **网络牵线（rustfs mode 独有）**：引擎给带该 label 的服务 spec 附加 `fleetly-rustfs-net` overlay 网络（应用→RustFS 单向可达 = 同网络互通）。这是 V2-5「平台牵线共享网络」的 W3 窄面先例（只为 S3 可达性，不开放通用共享网络）。
- **前置校验**：label 出现而 `s3.mode=unset` → plan 阶段 `E_S3_NOT_CONFIGURED`（诚实拒绝，不注入空 env 让应用谜之失败）。同键与用户 env 冲突 → 复用 `W_ENV_PLATFORM_OVERRIDE` 警告（system > platform 既定序）。
- external mode 无网络牵线（应用自行出网到外部端点）。

### 2.5 RustFS opt-in 管理组件（V2-2）

`s3.mode=rustfs` 保存 → 部署 duty（zot 部署器同款：幂等比对、spec 漂移即收敛）：

- **服务**：swarm service `fleetly-rustfs`，单副本；镜像 `rustfs/rustfs:1.0.x` 钉 digest（Go 常量 `DefaultRustFSImage` + image-prepull 台账双锚）；卷 `fleetly-rustfs-data` **钉住 manager**（数据重力：控制面状态备份的便捷目标在控制面同机；节点选择器与 zot 同口径）；内部 overlay 网络 `fleetly-rustfs-net`（不发布任何 host 端口）。
- **凭据**：root access/secret key 平台生成，存密钥库（envelope），经 Swarm secret 注入服务；`TestConnection` 走同一路径（endpoint=`http://rustfs:9000`，path-style=true，内部经 fleetlyd 所在网络可达性自测——fleetlyd 自身 attach `fleetly-rustfs-net`？否：**探测容器**一次性 attach 该网执行 §2.1 探针，与上传轨 restic 容器同形态）。
- **桶**：启用即 `EnsureBucket(fleetly)`（statebackups 前缀之外，应用注入面共用单桶——应用级桶/凭证是 v0.2.x+ 议题，见 §7）。
- **禁用**：`s3.mode` 改回 unset → duty 移除服务，**卷保留**（volume.detached 同型数据安全语义）+ Console 指引「数据仍在卷 `fleetly-rustfs-data`，确认放弃再 docker volume rm」。再启用：复用卷，凭据重生成（RustFS 凭据是运行时配置不烙进数据）。
- **资源**：内存限额 256MB（对齐 zot 口径）；**启用时计入 600MB 预算复测**（V2-2 裁决原文；实测 idle 进 runbook §7 复测记录）。
- **诚实标注（V2-2 裁决核心）**：Console Settings S3 卡与备份页常驻文案——「本机 RustFS = 便捷层（防误删/防单文件损坏），**非灾备**；主机整体损毁时该备份随主机一同丢失。灾备请配置外部端点（或等待跨节点互备，§6 挂账）」。CLI `fleetly s3 status` 同口径输出。

### 2.6 网络暴露形态（D-S3-9 已裁：内网 + 可选公网子域）

RustFS **默认仅内网**（overlay 网络，无公网面）——W3 全部在 scope 内的内网消费方（应用注入、restic 上传轨、TestConnection 探测容器、卷迁移 runbook 的节点上 restic）都走内网。**可选公网子域**（2026-09-21 用户裁决）：

- 设置键 `s3.public_exposed`（bool，缺省 false；仅 `mode=rustfs` 时可开，external 模式无此开关——外部端点本就在公网）。
- 开启动作链：ingress 视图增 `s3.<base>` 路由（TLS websecure → `fleetly-rustfs:9000`）+ 平台证书 duty 的 `_fleetly-platform` 多 SAN 集**条件增第四 SAN `s3.<base>`**（SAN 集变化 → 重签发，acme duty 既有收敛语义覆盖）+ RustFS 侧无变化（凭据鉴权已在）。关闭反向收敛（路由摘除 + SAN 集回缩 → 重签发）。
- 诚实口径：开关旁明示「公网可达面 +1，鉴权 = RustFS 凭证」；8423 的教训不重演——这是**用户显式选择**的暴露，不是结构性默认。
- 依赖：`base_domain` 已配置（单节点无平台域名形态下该开关不可用，`E_S3_PUBLIC_REQUIRES_BASE_DOMAIN`）。

### 2.7 FZ-7 conformance（CI，RustFS 代演）

`internal/objectstore` 的封闭操作面（§2.1）对 RustFS 实例跑通 = conformance：CI dind job 起 `rustfs/rustfs:<钉版>` → 全操作面断言（ensure/put/get/stat/delete/list + 探针协议 + path-style 行为）。**语义**：平台对「S3 兼容端点」的全部依赖面被 RustFS 证真——外部端点（AWS/MinIO/B2…）由用户侧 `TestConnection` 自测覆盖，CI 不mock外部云。FZ-7 就此闭环（v0.1 记 N/A 的条件消除：minio-go 有了真实消费者）。

## 3. 关键裁决（D-S3-*）

| # | 裁决 | 理由与代价 |
|---|---|---|
| D-S3-1 | S3 客户端面收敛单包 `internal/objectstore`（minio-go 不外漏） | 依赖复核轮已裁 minio-go；单包边界使 conformance 面=操作面可枚举 |
| D-S3-2 | 端点配置落**库内运行期设置**（platform_settings KV + envelope），不落 config.yaml | V2-1 易用性：Console 配置→测试→生效无需重启；domains/env 同类先例 |
| D-S3-3 | restic 执行形态 = **钉版容器一次性执行**（Docker API create/start/wait/rm） | restic 无库形态；不嵌二进制；供应链钉版纪律统一；镜像入 image-prepull 台账 |
| D-S3-4 | 上传是本地 verify 后的**追加步**，本地核不动；台账增 upload_status 三态 | 信任闭环（T2.22）不重写；「没上传」结构性可见（红），不静默 |
| D-S3-5 | restic repo 口令独立生成存密钥库（与 fleetly.key 分离） | 双因子分离：口令泄露不暴露数据、主密钥泄露不暴露远端 |
| D-S3-6 | 凭证注入 = label `fleetly.s3=true` → system env（FZ-1 同构先行） | env 链预留 system 层的第一生产者；W4 数据库连接串复用整套机制 |
| D-S3-7 | RustFS = zot 同款 duty（幂等比对/卷钉 manager/内部网/凭据经 Swarm secret）；禁用保留卷 | 平台组件部署纪律复用；数据安全语义与 volume.detached 一致 |
| D-S3-8 | 同节点 RustFS 备份 = Console/CLI/文档三面**诚实标注「便捷层非灾备」** | V2-2 裁决原文；灾备指向外部端点（跨节点互备 §6 挂账） |
| **D-S3-9** | **已裁（2026-09-21）：内网 + 可选公网子域 s3.\<base\>**（`s3.public_exposed` 开关，默认关；详见 §2.6） | 用户裁决：保留外部工具可达性入口，默认姿态仍是零公网面 |
| D-S3-10 | RustFS 版本钉 `1.0.x` GA（digest 双锚），应用与平台共用单桶单凭据 | V2-8 已裁 W3 时点钉最新 1.0.x；per-app 凭证/桶留 v0.2.x+ |

## 4. 票据分解与验收（E3 切面）

| 票 | 内容 | 验证方式 | 回滚 |
|---|---|---|---|
| E3-1 | `internal/objectstore` + minio-go 引入 + TestConnection 探针 + 单元测试（httptest 假 S3） | go test；探针负路径（错凭据/断端点） | 还原点提交 |
| E3-2 | platform_settings KV 表 + s3.* 设置 CRUD + envelope + API/CLI（`fleetly s3 set/show/test`）+ proto 加法 | API 集成测试 + 审计断言 | 迁移前滚回（goose down） |
| E3-3 | state_backups 上传列 + restic 容器执行器 + 上传/回读/forget + backup.upload_failed 事件 + 红健康 | 单元（fake 执行器）+ dind 端到端（备份→断本地→远端仍在） | 上传轨开关 = s3.mode unset 即停 |
| E3-4 | label 注入（system env + 网络牵线 + E_S3_NOT_CONFIGURED） | 引擎单测 + dind：注入后容器内 env/连通断言 | label 移除即回 |
| E3-5 | RustFS duty（部署/收敛/禁用留卷/凭据/EnsureBucket）+ Console 诚实标注 + `fleetly s3 status` | dind：启用→部署→注入→读写闭环 + 禁用留卷断言 | s3.mode unset |
| E3-6 | 公网子域开关（`s3.public_exposed`）：ingress 路由 + 平台证书 SAN 集条件增减（重签发收敛）+ 开关门禁（需 base_domain）+ Console 开关与口径文案 | dind：开关往返路由/SAN 断言 + 单节点形态门禁拒绝 | 开关关闭即收敛摘除 |
| E3-7 | FZ-7 conformance CI job（RustFS 代演）+ image-prepull 台账增行（rustfs、restic） | CI 绿；check-image-pins 过 | — |
| E3-8 | runbook：backup-restore 增 S3 恢复节 + image-prepull 增行 + Console Settings S3 卡 | 文档走查 | — |

**E3 验收（锚定规划 §5）**：备份→S3→回读校验通过 + 恢复演练（restic restore 进 §4 流程）+ opt-in 启用路径端到端（启用→部署→凭证注入→应用可读写）+ 诚实标注三面可见 + 预算复测（RustFS 启用态全栈 idle <600MB）。

## 5. 契约面

### 5.1 proto（buf breaking FILE 内，只加）

`server.v1` 增 S3 设置面：`GetS3Settings`/`UpdateS3Settings`/`TestS3Connection`（请求/响应含脱敏回显——secret 只写不读，读面给 fingerprint）。CLI `fleetly s3 show/set/test/status`。

### 5.2 错误码（注册表只增）

`E_S3_CONFIG_CONFLICT`（mode 与显式字段互斥冲突）、`E_S3_NOT_CONFIGURED`（label 注入无配置）、`E_S3_TEST_FAILED`（探针失败，detail 带失败步）、`E_S3_RESTIC_FAILED`（上传/回读失败码，日志面）、`E_S3_PUBLIC_REQUIRES_BASE_DOMAIN`（公网子域开关在无平台域名形态下拒绝）。

### 5.3 事件（注册表只增）

`s3.updated`（设置变更，payload 带模式不带走秘密）、`s3.rustfs_deployed`/`s3.rustfs_removed`（duty 收敛差分，node.* 同型）、`backup.upload_failed`（上传轨红）、`backup.upload_recovered`（failed→ok 回绿）。

### 5.4 env 词表（system 层）

`S3_ENDPOINT`/`S3_REGION`/`S3_BUCKET`/`S3_ACCESS_KEY_ID`/`S3_SECRET_ACCESS_KEY`/`S3_PATH_STYLE`——键名进 env 三层合并链文档词表（只增）。

### 5.5 Console 锚点（data-testid 清单只增）

`s3-settings-card`、`s3-mode-select`、`s3-test-button`、`s3-test-result`、`s3-public-toggle`、`s3-honesty-note`（便捷层非灾备常驻）、备份页 `backup-upload-status`。

## 6. 与既有文档一致性声明（逐条）

- 架构 §2.3 备份基线行「备份密钥与元数据独立于备份数据保存」：restic 口令独立密钥库条目，一致。
- 架构 §4.2「任何备份失败红色告警」：upload_failed 红 + 组件不健康，一致。
- state-model 快照/台账纪律：upload 列是 state_backups 行内追加，不改快照结构，一致。
- multi-node §2.8 restic runbook：「S3 after E3」兑现——平台配置的端点可直接作用户 restic repo 目标（凭证=注入同款），一致。
- v0.2 规划 §6 挂账「跨节点互备」：本文不实现，指向不变。
- drill-l1 §6 不覆盖项「异机上传随 v0.2 S3 目标」：E3-7 恢复演练清偿（真机，非 dind）。

## 7. 明确不做（W3 切面外）

- per-app S3 凭证/桶（RustFS IAM 面重，v0.2.x+ 议题；W3 单桶单凭据共享）。
- 应用数据（卷）的 restic 托管备份——W3 只做**控制面状态**备份上云；应用卷备份留给跨节点互备票（§6 挂账）评估同载。
- S3 生命周期/配额/监控面；presign；multipart。
- RustFS 分布式多盘/纠删码形态（单副本单卷，诚实口径=便捷层）。

## 8. E5 Cron 契约面补记（细则不重开，架构 §4.3 为准）

- **调度核修正（D-CR-1，已裁 2026-09-21：robfig/cron/v3）**：架构 §4.3「基于 lynx contrib/schedule」——lynx v1.11.0（当前最新）**无此包**（contrib 仅 zap）。修正为 `robfig/cron/v3`（Go 生态事实标准，MIT）+ `cron.ParseStandard`（恰好就是平台契约的五段标准式，无需「六段含秒钉零」转换）；tick 循环骨架沿用 v0.1 备份 ticker 模式（stop/done channel + 重载信号）。架构 §4.3 复用行随票加注（实现修正标注，multi-node §2.4 同型先例）。
- **调度核抽取**：statebackup 的 daily 循环不动（它只有一条固定周期线）；新包 `internal/cron` 承载：表达式解析（五段 + tz）、下次触发计算、重叠 skip（每 schedule 串行）、错过点不补跑（控制面重启后 next-fire 按当前时间重算，过去点丢弃 + `cron.skipped(reason=missed_during_downtime)`）。
- **cron_runs 表**（迁移随票）：`id`（ULID）、`app_id`、`service`、`expression`、`scheduled_at`、`started_at`、`finished_at`、`status`（`succeeded`｜`failed`｜`timeout`｜`skipped`）、`skip_reason`、`job_service`、`error`；保留每 schedule 最近 20 条（janitor 增项）。
- **事件集（注册表只增，FZ-4 兑现）**：`cron.triggered`、`cron.succeeded`、`cron.failed`、`cron.timed_out`（FZ-4 钉名）、`cron.skipped`（已在表）；手动触发走同一路径 + 审计 `cron.manual_triggered`。
- **job 形态**：一次性 Swarm job（replicated-job、restart-condition=none）、完成删服务；触发前绑定节点前哨（不 ready → skipped(node_unavailable)，不建 job）；看门狗默认 10m（label `fleetly.cron.timeout` 覆盖）超时删服务 + timed_out。
- **手动入口**：API `POST /v1/apps/{app}/services/{svc}/trigger` + CLI `fleetly cron trigger` + Console 按钮（写审计）。

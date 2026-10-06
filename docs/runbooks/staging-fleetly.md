# staging 重装 runbook（新 fleetly 接管，2026-09-30 实录）

| 状态 | 日期 | 关联 |
|---|---|---|
| 现役：新 fleetly（N0 批尾 HEAD 起）双节点 | 2026-09-30 | 功能清单 F0.18/F0.19 真机验收、F1.15 前哨；归档仓 runbook `fleetly-archived/docs/runbooks/vps-dogfooding.md`（历史教训） |

## 拓扑与连接

- **manager** = `ssh root@fleetly-dev.deeploop.net`（146.190.58.0；VPC eth1=10.124.0.3；Debian 13 / 2C / 4G）。新 fleetlyd = systemd `fleetlyd.service`（数据根 /var/lib/fleetly；unit 另注入 `FLEETLY_PROXY_CONFIG_ENDPOINT=http://10.124.0.3:9082/proxy/config` + `FLEETLY_PROXY_ACME_EMAIL`）。
- **worker** = `ssh root@143.198.234.68`（本机可直连；跳板形态 `ssh -J root@fleetly-dev.deeploop.net root@fleetly-node2.deeploop.net` 亦可）。VPC eth1=10.124.0.5。**零平台安装物**：只跑 docker daemon + swarm worker。
- DNS：DNSPod 通配 CNAME `*.dev.fleetly.run → fleetly-dev.deeploop.net`（n0.dev 实证解析）。
- 两台 dockerd 均带 drop-in `--insecure-registry 10.124.0.3:5000`（zot 走 HTTP；VPC 内网形态）。
- CLI 凭据在 manager `/root/.config/fleetly/credentials`（`FLEETLY_ADDR=127.0.0.1:9080` + 自动读凭据）。

## 现役资产

- swarm 双节点（manager advertise **10.124.0.3**——eth0 的 10.48.0.x 是跨 VPC 假象地址，advertise/raft 绝不可用）。
- 受管 proxy：traefik（80/443，LE **staging** CA；ACME 卷 `fleetly-proxy-acme`）。
- **受管仓库：zot**（F1.11，ADR-0019 附录 B）：受管 Workload 形态（fleetly/system/registry 域，发布 5000 routing mesh），镜像 `ghcr.io/project-zot/zot-minimal:v2.1.21` 钉版，数据卷 `fleetly-registry-zot`。**per-Project 凭证域隔离已上线（2026-10-04，ADR-0036 N2 兑现节 2）**：平台凭证（adminPolicy 全域）在 `/var/lib/fleetly/keys/registry.json`；per-Project 凭证在 `/var/lib/fleetly/keys/registry-projects/<projectID>.json`（用户名=projectID，密码+bcrypt 行同文件）；zot config 带 accessControl——`<projectID>/**` 仓门禁该用户 read/create/update、`*`（扁平存量仓）defaultPolicy read 全用户可读、其余拒绝。构建推送目标=`10.124.0.3:5000/<projectID>/<app>:r<seq>`（双段小写），from_build 下发=`10.124.0.3:5000/<repo>@sha256:<digest>`（repo 取 builds.repo 列；存量行空 = 扁平回退 `<app>`）。项目创建/删除经 API Kick 即时滚动 zot（htpasswd 摘/增行）；凭证轮换=删对应 json+重启 fleetlyd。unit 注入 `FLEETLY_REGISTRY_ADDR=10.124.0.3:5000`。旧手工 `n0-zot` 已停（容器保留作回滚资料；F0.18 场景 13 证据在上表）。
- 验收应用：project `n0reg`（私有镜像双节点部署）、`n0probe`（exec 探针）、quickstart `n0demo`（route n0.dev.fleetly.run, tls auto）。

### 受管 zot 接管实录（F1.11 部署时操作序）

1. 让位端口：`docker stop n0-zot`（受管 zot 发布 5000 routing mesh 会绑全部节点 5000，与 host network 的 n0-zot 冲突）。
2. unit 注入 env：`systemctl edit fleetlyd` 加 `Environment=FLEETLY_REGISTRY_ADDR=10.124.0.3:5000` → `systemctl restart fleetlyd`（两台 dockerd 的 `--insecure-registry 10.124.0.3:5000` 已在位，无需动）。
3. 验证：`fleetly doctor`（registry Provider healthy）；`docker service ls` 出 `fleetly-registry-zot`；curl `http://10.124.0.3:5000/v2/` 带 Basic 认证返回 `{}`（密码在 keys/registry.json）。
4. 全链实证：`fleetly deploy`（git 或 --from-dir 源）→ 构建日志出 push 步骤 → node2 `docker images` 出 `<digest>` 拉取产物。

## 2026-09-30 重装实录（顺序敏感处加粗）

1. 归档版退役：备份 `/root/fleetly-archive-20260930.tar.gz`（数据根）+ `-config.yaml` + `-services.txt`（18 服务台账）→ **停容器形态控制面必须 `docker stop fleetlyd`（--restart unless-stopped 会 12s 重拉 pkill 后的进程）** → `docker service rm` 全部（卷保留）→ secrets/configs 清理 → buildkit 漏网容器补清。
2. 数据根让位：`mv /var/lib/fleetly /var/lib/fleetly.archived-20260930` → install.sh（FLEETLY_BIN_DIR + **FLEETLY_ADVERTISE_ADDR=10.124.0.3**）→ unit 注入 Proxy env → `fleetly init`（bootstrap 即吊销）→ doctor 零 fail。
3. node2：daemon 已活则直接 join；**join 材料地址必须核验是 10.124.0.3:2377**——若出现 10.48.0.6（eth0）说明 raft 播报错网，修法 = `docker swarm leave --force && docker swarm init --advertise-addr 10.124.0.3`（manager）+ node2 leave/join。
4. **dockerd 旗标坑**：`--advertise-addr` 不是 dockerd 旗标（swarm 子命令专属）；写错触发 systemd "Start request repeated too quickly" 限速——`systemctl reset-failed docker` 后再起。

## 真机验收记录（N0 批尾）

| 验收 | 结果 | 关键证据 |
|---|---|---|
| F0.18 场景 13 私有镜像双节点拉取 | ✅ | Secret `registry:10.124.0.3:5000` → replicas=2 双节点 Running；node2 `docker images` 出现私有镜像；载体 spec 零凭证（inspect grep 密码零命中）。overlay 数据面双副本互通 = **UDP 4789/7946 已放行**（W3-F2 解除） |
| F0.19 RuntimeAdmin | ✅ | drain（task 真迁移：node2 Shutdown/manager 接管）→ cordon(Pause) → uncordon(Active)；审计 node.drain/cordon/uncordon（source=cli）；陈旧平台 ID 被 E_NOT_FOUND 拒（ID 永不复用执法） |
| 探针 | ✅（exec）/ 记档（http/tcp） | exec：compose healthcheck → L1 门真机通过。http/tcp 探针 CLI/API 无声明面（compose 只出 exec）——翻译正确性由单元钉死，声明面随 API 批 |
| F0.15 ACME | ✅ | `issuer=Let's Encrypt CN=(STAGING) Ersatz Emmer YR2`、`CN=n0.dev.fleetly.run`（LE staging + HTTP-01 经受管 traefik；生产 CA 轮换另批） |
| F0.3 quickstart --tls auto | ✅ | TLS 握手 + router + 证书全通；2026-09-30 首验时后端 502（受管 proxy 挂项目网当时是空桩）——B1 实装 `activeProjectNetworks` 后 **HTTPS 200 全通**（2026-10-01 双视角复验，见下节） |

## 本批真机逼出的修复（均在仓，随批推送）

1. **受管域 Generation 逐 tick +1 → traefik 繁殖 115 实例打穿 4GB**（managed.go：指纹未变 gen 不推进）——内存雪崩杀死 sshd/网关，主机硬重启恢复。
2. **docker API hang 卡死单写者 managedLoop**（daemon 重启窗口静默卡死直至进程重启；ManagedStepTimeout=30s 带界）。
3. **node label 服务端过滤对点号键恒不命中**（`label=k=v`/`node.labels.k=v` 都不行；admin.go 改全量 NodeList + 内存精确匹配）。
4. quickstart 镜像直投无端口声明 → route 后端永解析不到（改 compose 形态带 ports）。

## 2026-10-01 记录（N0 修复批尾 + N0.1 小修批）

- **quickstart HTTPS 200（双视角）**：manager 与 node2 路径各验一次，均 200——受管 traefik 挂项目 overlay（B1 `activeProjectNetworks` 实装）后端连通收口，F0.3 的"后端 502"边界项关闭。现役 quickstart：app demo（whoami:v1.10，网络挂靠）+ 路由 n0.dev.fleetly.run（tls auto，LE staging）。
- **C2 删除语义真机验证**（验证用 App n0demo，已删）：`fleetly apps delete` 对活跃部署 E_CONFLICT；终态后删除 → swarm service 真拆除（`docker service ls` 无残留）→ 引用路由撤销 → tombstone（同 Project 同名可重建、旧 ID 404）。
- 本机出站波动期间的外部验证照旧走 node2（教训节不变）。

## 2026-10-02 记录（F1.15 dogfooding 上线）

**栈现状**：torchwood（项目 torchwood，六常驻 + firstBootJobs 两件，pgvector 受管库 torchwood-pg）与 messageloop（项目 messaging，三服务 + 跨 Project peer 挂靠 torchwood 网）双栈从零上线。Route：`tw.dev.fleetly.run`（9080 http，readiness 200）、`tw-grpc`（9060 h2c，gRPC 应答 415=服务端在答）、`ml` / `ml-grpc`（9090 h2c）/ `ml-api`（9091 h2c）。

**真机八件清单实证**：

| # | 验收 | 结果 | 关键证据 |
|---|---|---|---|
| ① | swarm 跨服务 alias DNS RR | ✅ | resident 池 task-\<id\> 名 6 次查询轮转 3 IP（run 服务共享池别名）；栈内服务名互访（redis/minio/dispatcher/server）全靠 ADR-0034 别名 |
| ② | per-Run=service 池 | ✅ | concurrency 3/4 补足即 3/4 个独立 swarm service；renew 推 deadline；scale 3→1 排空 platform_drained（**缩容排空缺口=本批修 7**）；stop→draining→drained；停止原因活体观测 completed/failed/stopped_by_user/lease_expired/platform_drained |
| ③ | lynx 流式豁免 | ✅ | 网关 9081 `GET /v1/events?follow=1&replay=1` SSE 30KB 帧流无缓冲 |
| ④ | 512MiB 级上传 | ✅ | 511MiB（535823872B）14s 收；512MiB+512B 精确拒（E_UPLOAD_TOO_LARGE 带限额值） |
| ⑤ | 受管 zot 起服+构建推送+双节点 digest | ✅ | buildprobe 构建→push zot→node2 以 `10.124.0.3:5000/...@sha256:3ba27221...` 拉起 Running（**zot 四连修=本批修 8-11**） |
| ⑥ | 受管数据库 | ✅ | pgvector torchwood-pg running；App/migrate job 经项目网连 db-\<id\>（node2→manager 跨节点验证）；卷钉住 manager（placement constraint 可见）；redis 模板起服/删除全链 |
| ⑦ | 部署期迁移 job 端到端 | ✅ | compose `x-fleetly-first-boot-jobs`：migrate（版本化 psql，codeload 源）→ roles-sig（torchwood CLI 落 tw_secrets）串行执行，失败即回滚，最终部署 succeeded（ADR-0030/0033 全链） |
| ⑧ | railpack/static 构建链 | ✅ | railpack：宿主二进制 0.39.0（FLEETLY_RAILPACK_BIN 注 unit drop-in）+ gateway.v0 前端镜像，Node 源构建部署 succeeded；static：Caddyfile heredoc COPY 真机可用（容器内 200；**外网 Route 404=upload 面端口声明缺口，ADR-0032 已挂账**） |

**本批真机逼出的平台修复（11 件，全在 main）**：①swarm secret 引用 id+名双发（malformed secret reference）②UID/GID 显式 "0"（Atoi("") 启动崩）③identical spec update 自激环（no-op 跳过+canonical 归一化+网络名→ID 先解析）④DB 探针引擎原生化（pg_isready/redis-cli；裸 argv 无 CMD 前缀=无探针）⑤compose CMD-SHELL 探针引号保真 ⑥ErrCrossProjectRefNotApproved 哨兵漏映射 ⑦resident 池缩容排空缺口 ⑧endpointSpec Mode vip ⑨zot htpasswd 进程内一次铸（bcrypt 随机盐=指纹恒变=无限滚替根因）⑩zot 入口绝对路径 ⑪zot config htpasswd 块去 user 键。附：torchwood compose 调试批（DSN sslmode/JWT 全服务/addr 键/migrate 版本化）在 torchwood 仓。

**运维事实（记入操作序）**：
- **unit drop-in 会丢**：FLEETLY_REGISTRY_ADDR 曾消失（build 链精确报错自愈提示）；现以 `/etc/systemd/system/fleetlyd.service.d/registry.conf` + `railpack.conf` 双 drop-in 固化——fleetlyd 换二进制后 `systemctl restart` 前必查 drop-in 存活。
- **n0-zot 幽灵**：被 docker daemon 重启复活（--restart unless-stopped），占宿主 5000 收流→manager 上一切 401 全来自它。已 `docker update --restart=no` + stop。任何"端口被谁收流"排查先 `docker ps -a` 全表。
- **受管 zot 无钉住**（B.5②边界真咬人）：任何 spec 变更的滚动替换都可能把 task 漂到无卷节点 preparing 打转。临时操作序：`docker node update --availability=drain fleetly-node2` → 待 task 落 manager Running → `active` 恢复。**平台侧钉住=挂账，落期 F2.3**。
- **build 行按 (revision, builder) 内容寻址复用**：改源（哪怕加注释）才铸新 build；排查"修复不生效"先想到旧 build 行重放。
- **swarm secret 载体已 1300+**（zot 风暴遗产）——**已闭合**：N1 收尾批 5fd610a RuntimeHygiene 孤儿清扫面，2026-10-03 真机实效 1300+→36 稳定（见当日记录节）；run 服务残留同批根修+兜底扫窗。
- torchwood 镜像 sha-78ea1a4 的 `configs/config.yaml` 不会被 ENTRYPOINT 自动加载——addr 类键必须显式 env（v0.1 形态同款教训再现）。

**挂账（F1.15 收口时的诚实边界）**：torchwood dispatcher 客户端仍是 v0.1 vendored 契约（对新 Tasks API 的移植=torchwood 侧独立批；池语义已按 ADR-0012 以平台 API 面真机回归）；mlbridge→torchwood 的 E2E 凭据接线（torchwood 首管引导+scoped key）未走完（ml-tw-projects 现为占位值）；GHCR 私有镜像直投未实证（机制=zot 私拉 F0.18 同款 registry: 凭证，无私有 GHCR 镜像可测）。

## 2026-10-03 记录（N2 前收尾批：真机锚闭环，全部门禁绿随批）

四枚 ADR 真机验收锚当日闭环（fleetlyd `eec4236-n1-final` 在位，测试物随批清理）：

| 锚 | 结果 | 关键证据 |
|---|---|---|
| ADR-0025 两级 DNS（池级 RR + per-Run 稳定名） | ✅ | n0probe 自建 dstcheck 池（concurrency=2，双节点分布）：`task-<id>` 4 次查询 10.0.5.4/10.0.5.2 轮转；`run-<id>` 各自单一稳定 IP 恰对号（.2/.4）；run 容器命名 `fleetly-run-<id>`。dind 实证以 staging 双节点真机覆盖 |
| ADR-0026 lynx 长流 vs 优雅关停 | ✅ | 活跃 StreamEvents 流（71s 长流）遇 `systemctl restart`：30s drain 窗内持续投递（关停信号后仍送达 2 事件，引擎观测环同窗照常收口 run 终态）；超窗后 HTTP/2 GOAWAY `NO_ERROR` + `graceful_stop` 显式收流，客户端立即干净退出可凭游标重同步——无误杀无静默截断 |
| ADR-0018 Schedule 跨 daemon 重启窗 | ✅ | 拍点间窗口重启：重启后下一拍恰一次（10:52/10:54 各一 task）、next_fire_at 重算正确（10:56:00Z）、无漏拍无双发；用户池 Workload 全程 running 零扰动（ADR-0015 迷你证据） |
| E29 孤儿载体清扫实效 | ✅ | `docker secret ls | grep -c fleetly-sec-`：1300+（zot 风暴遗产）→ 当日 136 → **36 稳定**（=现役载体集；100/拍预算限流如期清空积压） |

**DST 观察钟（ADR-0018 剩余锚）——已闭锚 PASS（2026-10-03 16:42Z 核验，钟已删）**：n0probe schedule `dst-boundary-observe`（*/20 Australia/Sydney，busybox echo）跨悉尼夏令时边界（2026-10-04 02:00→03:00 春令 = 2026-10-03 16:00Z 跳变）**逐拍恰一次、无双发无漏拍**：边界两侧事件流 seq 592-611 完整记录 15:40Z（01:40 AEST 最后一拍）→ 16:00Z（03:00 AEDT——02:00 AEST 墙钟槽不存在，归一到同一物理时刻触发，真实间隔恰 20 分钟）→ 16:20Z → 16:40Z（03:40 AEDT）稳定推进；每拍 task.created + schedule.fired + task.completed 三事件齐、next_fire_at 边界后重算 17:00:00Z（04:00 AEDT）正确。创建时换算（next fire 11:00Z=21:00 AEST）+ 当日 fleetlyd 两次停机窗（10:52 重启窗实证、13:36-13:38 换装窗）均不扰动拍点。判定：Spring-forward 缺口拍处理与 Vixie cron 语义一致且全程真实间隔稳定。核验后钟已删（后续自动化 f1e951fd/4bdcbf35 复读此处：锚已闭、钟已删，无需动作）。

**教训（Windows 本机远程操作）**：cmd → ssh → sh 三层引号嵌套必炸（`\$VAR` 转义层丢失）；复杂远程操作一律写脚本 scp 过去 `sh`，简单命令内联且零变量零嵌套引号。

## 2026-10-03 记录·二（bb5259d 全链路重验：升级滚动替换事故 + 回归矩阵）

**换装**：`eec4236-n1-final` → **`bb5259d-archreview`**（架构评审两轮收尾）。Windows 本机 `GOOS=linux` 交叉构建（版本注入 `bb5259d-archreview`）→ scp /tmp/n2bin → **停机 tar 快照**（/root/fleetly-data-prearch2-20261003.tar.gz，512M）→ install → drop-in 双文件核验（registry.conf + railpack.conf）→ doctor 10 ok / 0 fail。

### 事故（P0）：升级一次性滚动替换对有状态负载不安全 → torchwood-pg WAL 损坏

- **前提**：换装首启全量滚动替换本身是候选 7 记录在案的一次性代价（bb5259d 提交说明：label 消失+方言归一使存量 service spec diff 变化，"daemon 升级时预期全量滚一次"，staging 现付最低——设计已接受）。**本轮真机暴露的是该滚动对有状态负载的数据安全缺口**。
- **机制链**：bb5259d 改 Workload IR 形状 → 换装首启 `managedFingerprint` 全域漂移 → `EnsureGeneration` gen 推进（db `fleetly.generation` 2→3，traefik 同窗口 13:14:32 被更新+任务搬迁 manager→node2）→ swarm 滚动替换 → **pgvector 在 start-first + 单卷钉住 + 10s StopGrace 编排下被硬杀** → WAL `invalid checkpoint record`（13:14:34 非正常关停）→ 崩溃循环 → torchwood server 连锁崩溃（`db-<id>` 无 endpoint）→ tw.dev 502。
- **对照实证**：同二进制重启零 churn（本次 13:37 重启与当日 10:52 重启均未替换任何任务；traefik 任务上一次变更在换装前 21 小时）——指纹同版本内稳定，churn 只发生在**二进制变更首启**，与候选 7 的预告一致。
- **救援实录**（13:36 全程 ~7 分钟）：`systemctl stop fleetlyd`（traefik 持留末次配置）→ `docker service update --replicas 0` 冻结崩溃循环 → **卷 tar 留底**（/root/torchwood-pg-vol-pre-resetwal-20261003.tar.gz，11M）→ `pg_resetwal -f`（必须 `--user 999:999`，root 被拒）→ `--replicas 1` 起库（ready to accept connections）→ torchwood server 自愈 → tw.dev 200。同二进制重启 daemon 验证零 churn 后收口。
- **修复方向（修复批挂账，本批未动代码）**：① 数据库域（及一切挂卷受管面）UpdateConfig 改 stop-first + StopGrace 提到 60s 级——滚动替换不得硬杀数据面 ② 升级操作纪律（见下）③ 若一次滚动代价想免：Ensure 侧语义等价比较豁免 generation 标签（与候选 7 已接受裁决冲突，仅作备选重议）。
- **操作纪律（升级口径）**：**任何 fleetlyd 换二进制 = 预期全部工作负载滚动重启**；换装前先快照受管库卷（`docker run --rm -v <vol>:/d -v /root:/o busybox tar czf /o/<db>-pre-upgrade.tar.gz -C /d .` 一类）。
- 附带教训：`fleetlyd` 无 version 子命令——**未知参数会以默认配置直接引导 daemon**（本次误触在 /root/data 生成流浪库，靠真 daemon 占 :9080 才没起来；已清理）。版本核对用 `journalctl service.version` 或 `fleetly version`（CLI）。

### 全链路回归矩阵（bb5259d-archreview 真机，scratch 项目 archprobe 已清）

| 验收 | 结果 | 关键证据 |
|---|---|---|
| from-dir 构建链 | ✅ | Dockerfile→build→push zot→digest 下发 `10.124.0.3:5000/<app>@sha256:ff99e6c8...`（digest 直存绕 tag 坑）；building→releasing→observing→succeeded 全状态机 |
| 双节点 digest 拉取 | ✅ | compose 钉 digest replicas=2 → 双节点 Running（node2+manager）；受管仓凭证平台直注（materials.go 域内注入），载体 spec 零凭证 |
| 探针翻译（候选 7 IR） | ✅ | compose exec healthcheck（CMD 数组）逐字落 swarm Healthcheck：`["CMD","wget","-q","-O","/dev/null","http://localhost/"]` + Interval/Timeout/Retries |
| 路由 + 证书 | ✅ | arch.dev.fleetly.run：路由创建→LE staging 签发→200 且 body 内容校验过；traefik 挂靠收敛（见下 F-C） |
| identical 重部署 | ✅（设计语义） | 重部署=新 revision=新 Generation=合法 rollout（gen 2→3 双节点滚动）——修 3 的 no-op 指 tick 级自激；tick 级实证：20 分钟无 churn + 同二进制重启零 churn。**勿再把「identical 重部署不滚动」当预期** |
| 受管库生命周期 | ✅ | redis 模板 create→running→delete（载体拆、卷+凭证保留语义在 CLI 文案可见） |
| schedule 引拍 + one-shot 铸造 | ✅ | */2 UTC 恰时引拍（13:48:00Z 恰一次）、`last_task_id` 落位、task `completed`、next_fire 重算 13:50；命令 argv 元素保真（echo arch-fire） |
| 事件 owner 词汇（typed owner） | ✅ | `deployment.succeeded` 载荷带 `app_id`；run/task 事件带 `dns_name`（run-\<id\>/task-\<id\>）；schedule.fired 带 timezone/next_fire_at/last_task_id/source=cron |
| resident Run 池 | ✅ | concurrency=2 → 2 run（per-Run=service `fleetly-run-<runid>`）；stop --force → stopped_by_user → drained → delete |
| lynx SSE | ✅ | `events follow --replay` 555 行有界回放 |
| uploads（objectstore 入口） | ✅ | 2MiB put → digest sha256:7673c6e2 入库 + list |
| C2 删除语义 | ✅ | apps delete → 载体+引用路由全拆 → 404；project delete 干净 |

**F-C（P2，已收口）**：compose 引用 `networks: [default]` 但 networks 表无行时**静默半物化**——swarm 侧 overlay 建了、表行没有，traefik 挂靠（真源=`activeProjectNetworks` 读 networks 表）永不收敛 → 路由 502。**已由出生网络收口（31bcd86，2026-10-03）**：项目创建同事务建 default 网络行，新项目不再需要显式 `networks create default`（显式重复建必撞 E_ALREADY_EXISTS）；存量零网项目（出生面之前创建，如 n0reg/n0probe）仍走显式建。

**重启后路由冷窗（行为可接受）**：daemon 重启后若后端暂不可达，首次 publish 对应路由诚实跳过（journal `route publish: backend unresolved, skipping route`），下一拍后端回来即重发布——实测 13:37:46 跳过 → 13:38:47 全量恢复（~60s 有界）。static.dev 的跳过是 ADR-0032 既有缺口（upload 面端口声明），非本轮回归。

**P3 日志噪音**：`engine drive: unexpected driving state succeeded`（deployment 01M40ZXP2，终态行撞 driving 路径的单次 error，非循环）——留修复批顺手收。

**DST 闭锚**：automation-4bdcbf35 已挂（本地 10-04 00:45 = 16:45Z 一次性），闭锚结果另记小节。换装窗口 13:36-13:38 停机两拍内 DST 钟无漏拍争议（next_fire 重算正确，13:40 拍照常引燃）。

### 修复批落地（同日 b4cfea0-upgrade-safety，四件全绿随批推送）

staging 真机验收（14:53-14:56）：

| 修复 | 真机证据 |
|---|---|
| 数据面滚动安全（3b3dd85：挂卷负载 stop-first + db/zot StopGrace 60s） | 换装首启滚动如期发生（db updated 14:55:05），**postgres 干净关停**（14:54:54 shut down，无 PANIC/无 invalid checkpoint）→ 1 秒后新任务 ready；spec 实证 `order=stop-first grace=60s`。本次仅数据面滚一次（app 负载指纹未变不受扰，与"最后一次滚动"预判一致）；torchwood server 全程 Running，tw.dev 200 |
| default 网络随项目出生（31bcd86，F-C 根修） | `projects create birthprobe` → networks list 即见 default 行（同秒）；traefik 挂靠真源与 compose 引用面同源，半物化窗口关闭 |
| fleetlyd 首参守卫（841ca20） | `fleetlyd version` → exit 2 + 明确报错，不再引导流浪 daemon（/root/data 零产物） |
| drive 终态安静停驱（b4cfea0） | 换装窗口 journal 无 "unexpected driving state" 噪音 |

升级纪律执行实录：换装前快照 `fleetly-vol-torchwood-pg`（7.6M）+ `fleetly-vol-fleetly-registry-zot`（126M）至 /root/upgrade-b4cfea0/ + 数据根 tar；zot 卷名实测为 `fleetly-vol-fleetly-registry-zot`（runbook F1.11 节的 `fleetly-registry-zot` 是服务名非卷名，已勘正使用）。存量零网项目（n0reg/n0probe）不受出生面影响——如需 default 网络仍走显式 `networks create`。

### 升级矩阵 CI（P1，2026-10-03 挂）

本节的真机验收已有 CI 常态回归锚：`e2e/dind-upgrade.sh`（CI job `e2e-upgrade`，`mise run e2e:upgrade` 本地可跑）——HEAD~1 旧版装到 HEAD 新版，三件负载（web+Route / 第二 App / 受管 postgres）断言升级全程**路由零失败、Workload 零重启、数据库零滚动**（ADR-0015 验收的 CI 化）。staging 手工升级前可先本地跑一轮同款脚本预热；脚本首跑若在 CI 红，按 job 日志取证（升级路径 vs 环境面分离诊断）。

## 2026-10-04 记录（c8c0c56 换装实录：两 P0 事故与恢复 + F2.2/F2.3 链真机验收）

**换装**：b4cfea0-upgrade-safety → c8c0c56-f23（升级序五步、旧版无 platform 组走手工快照回退轨道），随后同日两次热修换装至 **22b2f1a-staging2（现役）**。restic 0.19.1 按 install.sh 4d 节补装（旧装机无此节；bunzip2 缺件 apt 补）。goose v18→v19（00019_backups）干净前滚；用户域任务行零扰动（15h Running 不变）；**受管钉住首滚未发生**——zot/traefik 换装前后约束同值（node.labels.fleetly.node.id 钉 manager）不滚，仅 db 因 DataTarget 变更滚一次。

### P0 ①（数据丢失级）：镜像 VOLUME 匿名卷遮蔽命名卷 → db 任务替换即空库服现

- **机制**：刷新版 postgres:17-bookworm（18+ 布局入口）镜像 VOLUME 声明 `/var/lib/postgresql/data`——docker 对"嵌套在命名卷内部的镜像 VOLUME 路径"铸**匿名卷**并遮蔽命名卷同名子目录。F2.2 父目录挂法（DataTarget=/var/lib/postgresql）+ 入口默认 PGDATA 正中遮蔽：initdb 与服务全落匿名卷（per-task 新铸）。
- **咬出链**：10-03 14:55 b4cfea0 首滚即中招——torchwood-pg 空库服现 14h（pg_isready healthcheck + 前端 200 全程掩盖）；10-04 04:32 c8c0c56 换装再次触发。取证三件：container Mounts 双卷铁证（命名卷@/var/lib/postgresql + 匿名卷@/var/lib/postgresql/data）、system identifier 分歧、base/16384 高 OID 孤儿文件（catalog 空而文件在——resetwal 后遗形态）。**真簇（21 表）自 /root/upgrade-b4cfea0 卷 tar 起回**——belt-and-suspenders 纪律救命实录。
- **修复**：7cd19b8——postgres/pgvector 模板显式 `PGDATA=/var/lib/postgresql/pgdata`（父挂内、镜像 VOLUME 路径之外；创建/存量双态成立）。锚=TestVolumeShadowContract（per-engine DataTarget×镜像 VOLUME 契约）+ e2e dind-backup 任务替换存活锚（`--force` 滚任务后种子行在场——该回归此前对 CI 不可见：种子后源库任务从不替换）。
- **staging 恢复**：停服窗内 `<卷>/data → <卷>/pgdata` 改名归位（b4 tar 真簇），7cd19b8 起服 reconcile 滚一次 → 21 表 + tw_secrets 全回、server outbox 错误清零、tw.dev 200。

### P0 ②：Platform Backup systemd 面缓存目录缺席

- restic 子进程 env 无 HOME/XDG_CACHE_HOME（systemd unit 形态；dind/e2e 恒有 HOME 故 CI 不红）→ "unable to locate cache directory" 拒跑。修复 22b2f1a（resticEnvFrom 双缺补 `XDG_CACHE_HOME=platform-backups/cache`，域内缓存随备份排除）+ e11398c（ListPlatformSnapshots 旁门直拼 os.Environ 漏补丁——备份链绿但回读 exit 1 同因）。
- **Platform Backup 真机首验绿**：快照 fa94c59f；变更后备份 4cd3a003。

### F2.2 备份链 staging 真机验收（scratch 项目形态）

- **存量边界咬实**：torchwood/messaging/n0reg/n0probe 四个 pre-F2.2 项目网无 attachable 位——Database Backup 的 utility 附着被拒（ADR-0039 §47 预告的精确错误路径）。`docker network update --attachable` 不存在；平台无 networks delete/recreate 动词——**网络重建动词缺口挂账**（错误文案承诺的 runbook 指引即本节：等动词落地，勿手工拆网——service 级 network-rm/add 会被平台 reconcile 回滚）。
  - **闭合注记（2026-10-05，N2 评审批 P1-4 根修）**：`RebuildNetwork` 动词落地（ADR-0046，bce7f01；CLI `fleetly networks rebuild --project <id> <name>`）。平台中介的受监督重建：附着载体逐个 detach → 删网（带界排水 + 等 swarm overlay 异步退役落地）→ ensureNetworks 同源复建（attachable）→ 载体 re-attach；与部署 Ensure/受管 reconciler 全程串行化（引擎维护锁），外来附着诚实拒绝（E_CONFLICT 列出）。staging 恢复操作序 = 换装新版后对 torchwood/messaging/n0reg/n0probe 四项目网逐个 `fleetly networks rebuild --project <项目ID> default`（重建窗分钟级：库服务各滚两轮，低峰窗执行；dind 实测单网 ~4s CLI + 滚动收口 ~30s），torchwood-pg 定时备份自愈——随换装批执行。**操作注记**：re-attach 滚动收口窗内（秒级~半分钟）立即触发的备份可能撞 db-\<id\> 的 overlay DNS 未注册窗（报 could not translate host name，dind 实录）——重试即愈；判断收口 = 库容器内 `getent hosts db-<id>` 可解析。
- **scratch 项目（出生即 attachable）全链绿**：pgvector 建库→种子→backup succeeded→verify ok=true→delete→**卷真删**（等容器 GC，rm 循环到成功）→同名 recreate `--restore-from-backup`→anchor 表回归。首轮"恢复绿"实为同名卷残留数据（volume rm 被 GC 窗挡下且被 2>/dev/null 吞）——**恢复证明必须空卷起家**；恢复是异步任务，断言要等。

### 工程事实（本批积累）

- **`docker service logs` 在本 daemon（29.8.1）可无限挂起**（对已删/不存在服务尤甚——10-02 起三只僵尸即此，连带 nettest3/4、mgrtest 残留）；诊断一律容器级 `timeout 20 docker logs <cid>`，service 级禁用。
- pkill -f 自匹配：ssh 远端命令行含 pattern 即自杀（会话无输出退出）——按 PID kill。
- db 任务每次替换泄漏一枚匿名卷（镜像 VOLUME 遗产）：本机已积 1680 枚——RuntimeHygiene 扫匿名孤儿卷**挂账**。
  - **闭合注记（2026-10-05，N2 评审批 P2-4 根修）**：匿名孤儿卷清扫落地（88bc1c6；`RuntimeHygiene` 子面扩 `SweepOrphanVolumes`，retention janitor 100 枚/拍 × 10 分钟节拍）。判据五重全满足才删（宁可漏扫不可误删）：① 名字 64 位小写 hex（docker 匿名卷命名规律，命名卷/fleetly-vol-* 天然出局）；② 悬空（daemon dangling 过滤，无任何容器引用）；③ 出生超 7d 年龄窗；④ 无任何 label（用户/编排器标记卷绝不碰）；⑤ 仅控制面节点本机 daemon（worker 节点泄漏不在射程）。法律依据 = 架构 §8 孤儿词条后半句"平台自建残留由看门狗收口"。staging 实效预期：存量 1680 枚按 100/拍约 17 拍（~3 小时）清空；稳态每次 db 替换泄漏一枚、7d 窗后由清扫回收。**诚实边界**：匿名卷无标记面，操作者想保住某枚就得在 7d 窗内命名化或导出（如 `docker run --rm -v <64hex名>:/from -v "$PWD:/to" busybox cp -a /from/. /to/` 导出，或 `docker volume create` 具名副本后迁数据）——窗外无保留手段：匿名卷不载任何"谁在用它"的信息，超窗即按平台副产物收口。
- 升级断言面：**路由 200 ≠ 数据在场**——数据核对步已进升级操作序第 5 步。

## 2026-10-04 记录·二（per-Project registry 凭证域隔离上线：迁移实录 + 竞速修复）

**换装**：22b2f1a-staging2 → 4d4a88d-perproj → **c7de917-perproj2（现役）**。goose v19→v20（00020_builds_repo）干净前滚；Platform Backup 前后各一（04b6bff0/692ba6ef）+ 手工卷 tar（zot 133MB + torchwood-pg 662MB 至 upgrade-4d4a88d-perproj/vols/——本批 zot 只滚材料不触卷，纪律仍全量执行）。

**迁移前置信（前置真机验证，防 crash-loop 于未然）**：升级前在 manager 起临时 zot 容器（127.0.0.1:5050，挂同形状 config+htpasswd 夹具）打满 12 项权限矩阵——自有前纲读 404-allowed/写 400-allowed、跨 Project 读写 403、扁平仓全用户可读+写 403、admin 全域、匿名 401——全对后才动平台。**zot v2.1.21 accessControl 语义实证**：未匹配任何 pattern 的仓对非 admin 拒绝；glob 是 doublestar（`*` 不跨 `/`、`**` 跨段）；多 pattern 命中取最长——**不可加 `"**"` catch-all**（会压过 `*` 反噬扁平仓可读）。config 键 camelCase（viper/mapstructure 大小写不敏感）；`accessControl` 在 `http` 块下。

**迁移实录（升级序五步走完）**：zot 恰滚一次（config 增 accessControl + htpasswd 增 5 项目用户行）；traefik/db 零滚动；tw 200 + ml-api 415 + torchwood-pg 21 表 + tw_secrets 在场（数据核对步）；用户域任务零扰动。

**验收锚（真机）**：
- **存量兼容**：扁平仓 buildprobe r2（`10.124.0.3:5000/<appid>@digest`）对 per-Project 用户 GET 200、写 403——存量 digest 引用全用户可读不断流（暴露不扩大不收缩）；builds 表存量行 repo 空 → 投影扁平回退（DB 实查双态在场）。
- **新内容前纲**：scratch 项目 regprobe dockerfile 构建全链 succeeded——推送目标双段小写前纲、builds.repo 列定型、swarm 任务 image = `<addr>/<projectID>/<appID>@sha256:...`、catalog 扁平+前纲共存。
- **域隔离**：同 URL 异凭证对照——自有项目 404-allowed vs messaging 凭证 403-denied（读与写双面）。
- **撤销面**：项目删除 → 45s 内其 zot 用户 401（htpasswd 摘行即时滚动）；仓库数据留存但门禁已撤（keys/ 残留 json 无害——ULID 不复用）。

**竞速事故与修复（本批真机咬出）**：项目创建后**同秒**部署 → 构建推送 HEAD blob 401 一次失败（zot 用户随活跃集再生的受管滚动晚于首构建到达：节拍 ~60s + 滚动时长；且失败 Build 行按 Revision 一次性语义使同内容重试直接继承失败——需换内容铸新 Revision 才会真重推）。修复 c7de917：①Project 创建/删除 API 写路径 KickManagedLoop（滚动窗缩到即时）；②builder 推送 401 有界退避（10×10s，判定只认 unauthorized 文本——digest hex 含 "401" 不得误判）。**回归绿**：raceprobe 项目同秒部署首试即 succeeded。

### 工程事实（本批积累·二）

- **`docker secret inspect` 读不回载荷**（.Spec.Data 恒 null——write-only）；zot-minimal 容器无 cat/sh；`docker cp` 从 tmpfs 挂载（/run/secrets）取文件恒 0 字节——材料核验走行为面（真实凭证打权限矩阵），字节面无门。
- `fleetlyd --version` 不是版本旗标——会**当场起一个旁路 daemon**（cwd 相对数据根 ./data 会被凭空铸出 + bootstrap token；绑定失败 30s 排水退出）。已铸杂散数据根要即时清（rm -rf /root/data 实录）；版本看 journalctl 的 service.version。
- scratch 项目源目录（--from-dir）内容寻址：同内容重部署复用同 Revision——终态失败 Build 行会被继承，重试需改内容。
- curl 探 staging 路由要 -k（LE staging CA 不入系统信任根）；manager 直跑 `https://` 探针 exit 60 属预期。

## 2026-10-04 记录·三（F2.4 VictoriaLogs 上线：九锚全绿 + 真机咬出四修）

**换装**：c7de917-perproj2 → **09c9ad7-f24c（现役；= 09c9ad7 + 真机修复四件同批）**。goose v20→v21（00021_log_cursors）干净前滚；Platform Backup 前后各一（前置 + c51ecd26 收尾）+ 手工卷 tar（zot/torchwood-pg/redis 系全量至 upgrade-09c9ad7-f24/vols/）。配置面新增：unit drop-in `10-logging.conf` 物化 `FLEETLY_LOGGING_ADDR=10.124.0.3:9428`（lynx env 桥 → config logging.addr 同键；install.sh 已同源内置给新装）。

**受管面**：VL 服务名按命名公式 = `fleetly-fleetly-system-logging-victorialogs`（不是 workload ID 直用——诊断过滤器勿拿 workload ID 当服务名）。首启 traefik/zot/VL 各滚一次：**受管 Generation 是进程内计数（每进程重置为 1），而 fleetly.generation 标签持久在 spec 里**——前一进程存活期内材料变更把 gen 推到 2（zot accessControl 批）+ 强制重放节拍把 traefik 也写成 2，新进程 gen=1 → 全域各滚一次归一。**二连重启零滚**（gen 已归 1）= 判别法；系统性回归被此法 + CI e2e-upgrade 排除。既有语义非本批引入（良性：60s stop-grace 数据面无扰），挂账观察。

**真机咬出四修（全部同批回仓 + 单测以真机 ground truth 为夹具）**：
1. **VL 查询响应 `_stream` 是字符串不是 JSON 对象**（LogsQL 文本形态 `{fleetly_app=...}`）——严格 Unmarshal 整行失败 → 行行被静默跳过（200 + 0 帧）。修：平铺字段解码 + `_stream`/`_stream_id` 按未知字段忽略。教训：对外 API 形态没有真机 ground truth 夹具前，"防御性双形态解析"是自嗨。
2. **LogsQL 文本过滤的合法形态是过滤器位 `_msg:~"regex"`**——管道形态 `| ~ "re"` 被拒（400 "missing ':' in front of '~'"；官方 docs 通篇无此形态，凭记忆写的语法）。修：`{...} _msg:~"escaped"`。
3. **LogsQL `{}` 花括号过滤器只作用于流字段**（`_stream_fields` 成员）——非流字段（fleetly_team 是行字段）进 `{}` 恒空集**且不报错**（200 + 0 行的静默空，比报错更险）。修：查询构造只收流字段（project/app/workload/task/kind/build）；行级隔离锚 = project+app（team 是派生轴冗余）。
4. **低流量域永不入库**——批汇器只有 256 帧阈值 flush，没有时间面：受管 pg 域 checkpoint 每 5 分钟一行，批永远不满。staging 实证：VL 全库只有高流量 probe 域的行。修：loggingStep 节拍驱动的空闲冲刷（批非空且闲置 ≥1s 即冲；不违 loop.go 的 ticker 单源纪律——时间面由环节拍承载）。**ADR-0040 写的 "≤1s 或 256 帧" 在初版实现里只做了一半，真机把另一半咬出来了。**

**验收锚（九项全绿）**：
- VL 1/1（钉版 victoria-logs:v1.52.0，入口 /victoria-logs-prod 绝对路径）；mesh 端点无凭证 401 / 有凭证过（basic auth 执法）；
- **发现 A 修复真机锚**：双副本跨节点 app（probe 副本分落 fleetly-dev/fleetly-node2），实时日志流 node 归因双节点齐全（swarm ServiceLogs 集群聚合 + details 归因）；
- `--text` 检索双形态（人读/JSON）命中双节点行；`--text --since` 时间窗；
- 低流量域入库（system 580+ 行 / torchwood 域持续流入）；
- build 日志链：RUN 步帧进 ring + VL（kind=build + fleetly_build 过滤锚）；
- **P11 真机锚**：构建推送凭证（keys/registry-projects/<pid>.json 的 password）在 build log 全量零出现；
- **重启回读**：daemon 重启 → ring 蒸发 → `builds logs` 经 VL 保留窗回读历史（step markers 完整返回）；
- **采集续流**：重启后游标重放，probe 行连续（跨三次 daemon 重启无缺口）；
- 零扰动：用户域任务 20h 零重启、tw/ml 路由 200/415 正常、torchwood-pg 未触。

**操作教训（runbook 级）**：
- **shell 解析 `--json` 输出取 id 是雷**：`projects list` 非"最新优先"，`sed | head -1` 取到的是首行项目——本批 probe 误部署到 N0 期 app（01M3SGPMMWT，镜像 n0/private:1），经 `rollback --to <原始 revision>` 显式恢复（默认回滚目标是"最后成功基线"= 误部署本身，必须带 --to）。**id 一律从 create 输出取，不从 list 头行取**。
- 全缓存无步骤的构建（纯 FROM + CMD）VertexLog 零帧是正常形态（推送帧也少）——builds logs 空先查构建内容再怀疑管线。
- 诊断新受管域时：服务名按命名公式拼（fleetly-<team>-<project>-<app>-<process>）；VL 行为面直查（/select/logsql/query + hits stats + /metrics 的 vl_http_requests_total/vl_rows_ingested_total 计数器）比读库表直接。
- manager 无 sqlite3/python3——控制面库表直查不可用，走行为面（同材料核验文化）。
- 换装二进制要带 `-ldflags "-X main.version=<commit>-<tag>"`（裸 go build 的 service.version 空，journal 排障少一个锚）。


## 2026-10-04 记录·四（F2.5 VictoriaMetrics/cAdvisor 上线：告警全链真机绿 + 三修）

**换装**：fc38727-f24final → **cfbb59c-f25d（现役）**。goose v21→v22（00022_alerting）干净前滚；Platform Backup 前置（004e03c8）。配置面新增：unit drop-in `11-metrics.conf` 物化 `FLEETLY_METRICS_ADDR=10.124.0.3:8428`（install.sh 已同源内置给新装）。

**受管面**：VM 单节点（`fleetly-fleetly-system-metrics-victoriametrics` 1/1，8428 mesh）+ cadvisor 全局（`fleetly-fleetly-system-metrics-cadvisor` **2/2 双节点 task**——每节点一个 host:8080 端点）。首启滚动卡死一次（旧 task 占 host 8080 + start-first 双 task 争位——`docker service rm` 后 reconciler 重建即愈；host 端口类全局服务的滚动序语义挂账观察）。

**闭合注记（2026-10-05，N2 评审批 P2-5 根修，随批 commit）**：host 端口滚动序收口——swarm 翻译层 `rolloutOrder`（translate.go）对一切 host 模式发布（`PublishModeHost`）统一强制 stop-first：旧 task 先退让宿主端口、新 task 再起，争位卡死不再可能；cadvisor 未来 tag bump / spec 变更不再需要手工 `docker service rm`。判定刻意不看全局形态（replicated 单副本在单节点集群滚动同样同节点共存）——第二个 host 端口服务自动同款，无防呆缺口。StopGrace 取舍：cadvisor 留零 = 编排器缺省（10s 硬杀兜底）——无状态采集端 SIGTERM 即退，宽限窗只在进程滞留时才消耗（挂卷负载的 60s 是数据面排水窗，此处无数据面）。诚实边界：stop-first 每节点滚动窗内 8080 有秒级无监听空窗（采集环节拍退避容忍，换取不再卡死）；存量在役 cadvisor（spec 仍是 start-first）换装后下一拍 Ensure 见 spec diff 按新序各节点滚一次。

**真机咬出三修（2d01193）**：
1. **域材料默认挂全域 Workload 咬死 cadvisor**：VM 密码材料（swarm secret → /run/secrets/）注入同域的 cadvisor，其镜像无该目录且 overlayfs 只读 → mountpoint 创建失败 crash-loop。修 = IR 新增 `Workload.SkipMaterials`（显式退出面；无状态采集端不收存储凭证——本就是安全正确取向）。
2. **swarm Command 是全量 argv**：只给旗标会把入口二进制丢掉（`exec "--housekeeping_interval=15s" not found`）。zot 绝对路径先例再证——受管域 Command 恒写全量。
3. **cgroup v2 systemd 形态 + 标签值小写**：容器 id 是 `/system.slice/docker-<id>.scope`（非 `/docker/<id>`）——放弃前缀硬编码，app 标签非空即平台过滤器；`fleetly.ns.app` 标签值经 sanitizeNamePart 已小写，规则行的大写 ULID 在评估面归一比对。

**验收锚（真机绿）**：VM 1/1 + cadvisor 2/2（全局双节点）；8428 无凭证 401；`metrics query` 返回 cadvisor 序列（job=fleetly-cadvisor 归因标签入库）；**阈值规则全链**（tw app memory > 1B → 15s 内 firing → alerts list 可见）；通道 create/test(不可达端点 delivered=false 诚实)/delete；doctor 告警行（无通道 warn 带可行动建议）；用户域零扰动（26 服务基线不变）。QuerySeries 单序列首形态（多序列取首个——Console 批扩展）记档。

**端口表 +1**：8428 VM（basic auth 平台凭证）；**8080 cadvisor（每节点 host 直绑，无认证——VPC-only 边界，多租户前挂账：前置认证代理或节点防火墙收窄）**。

### F2.5 尾批：受管 Generation per-域化（7232da4，2026-10-04）

CI 升级零扰动锚咬出**全域滚动缺陷**（b6f59f3 根治）：受管 Generation 原为全局单计数 + 进程内重置回 1，而 `fleetly.generation` 标签持久在 spec——新 daemon 首拍给 Proxy 发 gen=1 ≠ 载体现行标签 = traefik stop-first 滚动 = 路由中断；且任一受管 Provider 指纹变化（新增 Provider/zot 材料随 Project 集变化）都放大成全部受管域滚一遍。**F2.4 记录的"受管 gen 重置滚动"挂账就此闭合。**

修复双件：①per-受管域独立计数（指纹=本域 Workload+材料，跨域不再传染）；②`InspectWorkloads` 观测播种续接（重启从载体现行 gen 续接，首见指纹沿用不假 +1）。附带：cadvisor 全局形态声明 `Replicas=1`（InspectWorkloads 对全局服务报缺省 1——声明侧 0 = 每拍假 drift 风暴，升级矩阵零漂移锚咬出）。

**staging 真机锚（7232da4-f25final）**：daemon 重启后**五件受管域全部零滚**（traefik/zot/victorialogs/victoriametrics/cadvisor task id 不变）+ 零 drift 事件 + 服务全绿——与 F2.4 时代"每次重启各滚一次"对照，升级零扰动语义首次全量成立。

## 2026-10-04 记录·五（F2.8 ObjectStore S3 Provider 换装 + 真机全链验证）

ADR-0042 落地（64f07f0..8b7f51d 七 commit，CI run 37218474928 六 job 全绿含 e2e S3 离机腿）。**换装**：7232da4-f25final → **8b7f51d-f28final（现役）**，按平台升级操作序：前置快照 3e4e1fb9 + 五卷 tar `/root/upgrade-8b7f51d/vols/`；**升级重启零滚锚过**（五受管域 task 龄 7h/5h 原样 + torchwood-pg 21 表锚不变 + 新 daemon 日志干净）；无新迁移。

**F2.8 真机验证（全绿后已回退复原）**：临时 silo 容器（manager loopback 9100，复用仓内已在的 pgsty/silo 镜像）+ systemd drop-in `12-objectstore-s3.conf` 五元组 → 重启切 s3（capability faces 锚）→ **验证链七锚**：①daemon 日志 objectstore/s3；②新库（f28probe）备份成功且 `object_key/size` 与桶内 mcli stat 精确一致（1330B）；③verify ok（S3 Get 重算 digest）；④本地 backups/ 计数不变（真离机）；⑤恢复到新库数据断言 F28_STAGING_OK；⑥platform backup 对 restic s3 仓 roundtrip（517MiB 入桶，config/data/keys/snapshots 齐）；⑦内置告警 platform-offsite-backup → ok。回退：drop-in 删除重启（provider=local 复原）+ 验证项目/库/卷/silo 清扫；受管域风暴后稳定无新滚。

**真机咬出四实录（产品挂账两件）**：
1. **验证类存储负载必须 bind 卷**：silo 容器 /data 落 overlayfs（无 bind 卷）时 517MiB 平台备份引发 manager I/O 停滞（silo 自报 "unable to write+read for 32.6s"）→ docker API 超时 → 平台备份一次失败 + daemon reconcile 全面 deadline。改 host bind 卷后全绿。
2. **观测失败被当 drift → 受管域假滚动（产品挂账）**：I/O 风暴期（docker API 停滞）受管 reconciler 的 InspectWorkloads/list 失败被当作 spec 失配处理——五受管域连滚三次；删除操作期 traefik 又假滚一次（zot 同拍滚动是项目材料语义、预期）。与 7232da4 的重启零滚语义冲突：**观测错误不得触发 spec 对照判 drift**（Ensure 前置观测失败的保守化），待专属批根修。**已闭合（2026-10-05）**：观测失败保守化落地（a30944f，N2 评审批 P1-5 根修）——观测面返回错误 = 跳过该受管域本拍 + warn 告警（沿触发不刷屏），不判 drift、不冷启 gen、不折成"无网络"挖 Proxy 挂网引用集；双分支带回归锚（播种 Inspect 失败 / 挂网 list 失败，观测恢复下一拍正常收敛）。staging 换装后受管域重启零滚语义（7232da4）不再受观测风暴窗口破坏——换装窗 docker API 停滞不再有受管域假滚叠加面（与 digest 滚动的归因也不再需要按 task 龄 + fleetly.generation 标签差区分）。
3. **项目删除不级联库（既有行为实录）**：`projects delete` 后库行仍 running、服务/卷原样；须逐库 `databases delete`（载体拆 + 卷/凭证保留）再手工 `docker volume rm`。**已闭合（2026-10-05）**：项目删除级联落地（ed04775，N2 评审批台账 #4 根修；裁决记档 ADR-0029 追记）——`projects delete` 受理先拒活跃 App（App 面维持"先手工清"现状，不级联——App 有部署状态机与路由撤除面），再对项目下全部活跃库逐个执行单库删除同款收口序（载体拆 → tombstone + database.deleted + 审计），全部成功后项目才 tombstone；卷与凭证 Secret 仍保留（数据卷回收照旧手工 `docker volume rm`）；任一库失败整体诚实失败（错误带库 ID），重试幂等收敛。**staging 存量孤儿库处理**：换装新版后对已删项目重跑 `fleetly projects delete --project <id>` 即收敛（归属授权与 SoftDelete 幂等对已删行成立，级联枚举面是活跃库行——apitest TestDeleteProjectRetryConvergesOrphanDatabases 锚），或照旧逐库 `databases delete`。
4. **legacy 项目网不 attachable 使 torchwood-pg 定时备份持续失败**（F2.2 已知挂账，错误文本自带 runbook 指引；s3 链路无辜——失败链经 s3objectstore Put 包装报出，链路语义正确）。**已闭合（2026-10-05）**：网络重建动词 `RebuildNetwork` 落地（ADR-0046，N2 评审批 P1-4 根修；换装后对四项目网逐个 `fleetly networks rebuild` 即自愈——见 F2.2 节闭合注记）。

**新能力面（物化指引）**：`platform_backup.s3` 五元组在场 → ObjectStore 装配切 s3 Provider（数据库备份对象直写远端桶，键 `backups/<projectID>/<databaseID>/<ts>-<id>`）+ restic 外置仓同批启用（同桶 `<prefix>/`——**prefix 勿取 `backups`**，双命名空间）；缺席 → local 现状零差。staging 物化：unit drop-in 五件 `Environment=FLEETLY_PLATFORM_BACKUP_S3_*`（endpoint `http://` 前缀=明文；**桶须预建**——Provider 探测不代建）。**边界（ADR-0042）**：切前台账行对象留本地 `backups/`（restic 备份集捎带离机），对新端点 verify/restore 诚实报 object not found；凭证 config 明文（0600，专用低权 key 建议；信封化挂账）；RustFS 自宿挂账（自宿推荐 silo——MinIO 社区版 2026-02 EOL 的社区续命版）。消警：五元组在场 = 内置规则归位（staging 现无真实离机端点，告警已重新武装——诚实姿态）。

## 2026-10-05 记录·六（ADR-0047 Edge→Proxy 词条更名 flag-day：env/端点路径/配置键换代）

一次性全库更名（词汇冻结显式 ADR；无兼容读层——外部消费者核证为零）。staging 换装面**只有 systemd unit 的三行环境**，受管 traefik 的 `X-Fleetly-Proxy-Token` 头与 `/proxy/config` 端点由新版 fleetlyd 全权渲染，无手工面：

1. unit drop-in 更名：`FLEETLY_EDGE_CONFIG_ENDPOINT` → `FLEETLY_PROXY_CONFIG_ENDPOINT`（**值内路径同步换代**：`http://10.124.0.3:9082/edge/config` → `http://10.124.0.3:9082/proxy/config`）；`FLEETLY_EDGE_ACME_EMAIL` → `FLEETLY_PROXY_ACME_EMAIL`。旧 `FLEETLY_EDGE_*` 行删除（不设兼容读）。
2. 配置文件如有 `edge_config` 键 → `proxy_config`（staging 现为 env 注入形态，无配置键面）。
3. 按平台升级操作序换装（前置 Platform Backup → 替换二进制 → 起新版）；新版 `fleetly doctor` 的 `proxy config exposure` / `bind surface` 行复核（文案与端点已随 ADR-0047 更名）。

## 2026-10-05 记录·七（备份重试风暴事故 + 网络重建四刀实录：torchwood-pg 备份自愈）

### 事故：116,938 枚匿名卷（诊断链完整，复盘锚）

- **机制链（三因叠加）**：① torchwood-pg 定时备份因 legacy 非 attachable 网附着被拒（START 期 PermissionDenied——**容器 create 已成功**，pgvector 镜像 VOLUME 的匿名卷已在 create 期分配）；② 备份失败不推进 `last_backup_at` 也无退避 → 1s tick 每秒重铸行重试；③ utility 收尾 `ContainerRemove(Force)` 不带 `RemoveVolumes` → 每次尝试漏一枚匿名卷。F2.2 上线（10-04 ~04:30）→ 止血（10-05 13:26）≈ 33h × ~1/s ≈ 11.4 万 + 基线 1.7k ≈ 11.7 万，实测 116,938——算术闭环。
- **同规模连带**：~11.4 万行 failed 备份台账行 + 同数 `database.backup_failed` 事件（outbox 7d 保留窗自清；台账行由本批 48h 失败行清扫出清）。
- **修复三补丁（本批复入 main）**：utility 清理带 `RemoveVolumes: true`；备份失败 5min 退避（f6040c4 platform 轨同款）；失败行 48h 清扫。e2e dind-backup 增双窗匿名卷零增量锚（失败面 + 成功面）。
- **清量**：`docker volume prune -f`（默认只碰悬空匿名卷；db task 在用卷不受影响）116,938 → **27** 枚（回收 101.3kB——全是空卷元数据垃圾，无数据损失面）。

### 网络重建四刀实录（ADR-0046 动词的第一次真机洗礼）

#1/#2/#3 均在 RemoveNetwork 排水 deadline（300s 全程 "active endpoints"）失败，根因三层逐刀咬出：

1. **crash-loop 服务卡死排水**：torchwood app 进程 crash-loop（应用侧：自带 :9000 探针 exit(1)）→ task 高频替换 → 网络端点永不清零；且 detach 触发的滚动更新被失败替换卡住、**健康副本不被替换**（swarm 滚动暂停语义）。操作解：`docker rm -f <健康 task 容器>`——swarm 按"已 detach 的现行 spec"补无网 task（与目标态合作，非对抗）。
2. **半死节点卡死 overlay 退役**：node2 dockerd agent 半死（30 分钟 804 条 journal 错误、node.left×13 抖动、manager→node2 ssh 断）→ swarm overlay 删除要全集群节点放行。操作解：**直连 node2（143.198.234.68）重启 dockerd**——worker 自动重入集群，容器照常。
3. **app 域无周期 ensure 重放面**：失败尝试的半 detach 态（app 服务被 network-rm 后 spec 与载体不一致）要等下次部署才愈——受管/数据库域每拍自愈，app 域不会。**本批补失败回滚（rollbackDetach，detach 后任何步失败即尽力挂回）**；旧版遗留的半 detach 态需 app 重部署收敛（torchwood app 待用户侧重部署恢复网挂）。

#4 成功：`rebuilt network default (detached 2, reattached 2)`、attachable=true → **torchwood-pg 备份 1 秒成功、`last_backup_at` 自 F2.2 以来首次推进**（13:59:45Z，对象 55KB+digest 落地）——P1-4 现场闭环。

### 挂账与待办

- ~~**torchwood app 进程 crash-loop 未处置**~~ **已自愈**（记·八：网重建+回滚 re-attach 后六服务 Running 20min+、tw.dev 200——crash 是环境性 db 失联，无需动 torchwood 仓；probe app ×3 半 detach 遗留可弃）。
- **node2 抖动根因未深查**（dockerd journal 804 错误未逐条分诊；重启后恢复，观察窗——记·八尾窗 90s 零 fatal task error）。
- ~~其余三 legacy 网（messaging/n0reg/n0probe）待新版部署后逐个 rebuild~~ **已收口**（记·八：messaging scale-0 收敛法完成；n0reg default 实测已 attachable；n0probe 无 default 网表行无库无动作；另 quickstart 网亦已 rebuild）。
- rebuild 遇 "attached endpoints did not drain" 的分诊序：`docker service ps`（失败列=crash-loop？）→ `docker node ls` + 节点 dockerd journal（半死？）→ **app 是否 boot 期 fatal（exit 1 即退——probe-fail-later 可等滚动、boot-fatal 永不收敛；解=snapshot scale-0 清端点 → rebuild → 平台 replay 回挂，记·八）**→ 处置后重试（动词幂等收敛）。

## 2026-10-05 记录·八（8b7f51d→a57de18 换装实录 + N2 修复批真机锚闭环）

**换装一**（8b7f51d-f28final → a57de18-n2final，评审修复栈 18 commits + Edge→Proxy 更名）：前置 Platform Backup `560d998e` + 七卷 tar（/root/upgrade-a57de18-n2final/vols/）+ unit 主文件两行 EDGE→PROXY env 改名（记·六序）+ **acme 卷预置**（`fleetly-vol-fleetly-edge-acme` → `fleetly-vol-fleetly-proxy-acme` 卷内 cp -a——ACME 卷名随更名换代，不预置则证书全重签；预置后 staging 证书无重发、路由即恢复）。

- **勘误（ADR-0047"无手工面"断言的缺口）**：受管域 App 轴 `edge`→`proxy` 使 traefik 服务名换代（`fleetly-fleetly-system-edge-traefik` → `...-proxy-traefik`），而 reconciler 只 ensure 现役域、**不拆除旧域**（孤儿永不自动删原则）——旧服务占 mesh 80/443，新 traefik 创建即端口冲突。处置：停机窗内 `docker service rm` 旧 edge-traefik（紧贴起服，中断最小）。后续同型全库更名若再动受管域命名，flag-day 序必须含旧域拆除步。
- **零扰动断言全绿**：用户域全部服务 task 行数与基线逐位一致；zot/VL/VM/cadvisor 零滚；零 drift 事件；goose v22→v23 干净前滚；traefik 新名一次性重建（1/1）。
- **torchwood-pg digest 零滚分支实录（F2.7/ADR-0045 决策 4）**：镜像引用更新为 `pgvector/pgvector:0.8.6-pg17-bookworm@sha256:cf134a…`，但运行中容器镜像 ID 恰等于钉定 digest（上游未漂移）→ swarm 视为同镜像、spec 原地更新、**零任务替换**（delta=0、21 表/tw_secrets 数据锚不变、pg_isready 活体）——"tag→digest 同内容"分支的真机形态；"digest 有差→恰一次受监督滚动"分支由 F2.7 本地 dind 配对实证。
- **事故处置接记·七**：风暴根修批（24ee477/356f208/e67a23f/b2eda80）push 后 CI run 37325995883 七 job 全绿（含 e2e-backup 匿名卷零增量双窗锚与 P1-4 rebuild 腿）。

**messaging 网 rebuild 收敛（记·七分诊序的第四层根因与操作解）**：detach 后 messageloop 应用 **boot 期 fatal（exit 1，跨服务 DNS 解析失败即退）**→ start-first 滚动序下新 task 永不 ready → 老 task（持有端点）永不退役 → rm 排水恒超时（与 torchwood 的 probe-fail-later 形态不同：boot-fatal 根本走不到滚动完成）。**操作解（scale-0 收敛法）**：`docker service scale <三服务>=0` 清端点 → `fleetly networks rebuild`（秒级成功，detach 4/attach 4、attachable=true）→ `fleetly rollback --app <ml> --wait`（replay 重放=re-Ensure，新 task 落新网）→ ml 三路由活体（404/415/405 均应用层应答）。torchwood app 六服务在网重建+回滚 re-attach 后自愈（Running 20min+、tw.dev 200，crash 是环境性 db 失联——无需动 torchwood 仓）。

**N2 修复批真机锚（一次 scratch 流全闭，事件尾链 seq 117701-117724 完整在册）**：

| 锚 | 证据 |
|---|---|
| F2.9 合成真机 | `revisions diff --from 1 --to 2` = `processes[0].env.FOO: bar1`（Project 层共享变量进冻结 Spec，与 `--env OVR=1` 共存）；put 响应 affected_apps 提示；值明文回显；同输入重部署内容寻址复用同 Revision |
| F3 级联删除真机 | redis 建库 running → apps delete → projects delete → 载体拆除 + 事件链 `app.deleted → database.deleted → project.deleted`（117722-117724）+ `fleetly-vol-r1` 卷保留义（ADR-0023 语义） |
| ADR-0041 锚 6 告警真发 | staging 本机 webhookrecv（e2e/webhookrecv 同款二进制）：`channels test` → `alert_test` 载荷落盘 + delivered；阈值规则（memory>1B）15s 内 firing → `alert.fired` 真载荷（真 rule/app ID + 真采样 3.17MB）落接收器；顺带清掉 10-04 验收遗留的两条恒 firing 规则 |
| Console 真机（F2.6） | HTTP 五项（index 200+title/asset immutable/index no-cache/SPA fallback/无凭证 401）+ 三页消费面逐端点对拍（per-App deployments 轴/logs NDJSON 帧含 base64 line/events 票据 SSE 具名帧 117721 实投）+ 走查 token 创建-吊销-401 复核；**浏览器 UI 走查已补**（2026-10-05/06 真机走查 PASS-with-notes，docs/reviews/2026-10-05-console-browser-walkthrough.md：判据全绿，SSE 活体双证；四发现 F1-F4 同日修复批闭——F2 服务端 300ec2d / F1+F3+F4 Console f9d42f5，staging 换装后生效） |
| CLI 旗标纪律再证 | Go flag 位置参数停析——`--value`/`--channel`/`--role` 等必须前置位置参数（runbook"旗标前置"条的三次新实录） |

**事件面**：风暴 ~11.5 万 `database.backup_failed` 事件在 outbox（7d 保留窗自然老化；台账行由 48h 清扫出清——**观察窗 10-06 04:30Z 起**，验证：`fleetly --json databases backups 01M3Y8WZ94YZ0R2D9PSK8MR13Z | grep -c '"status": *"failed"'` 应骤降、成功行不受影响、journal 无 prune 报错）。

**遗留（挂账）**：三个 probe app（buildprobe/staticprobe/railpackprobe）服务半 detach 遗留（无网运行、F1.15 验证残留，可弃——清理走 apps delete）；`fleetly-vol-archredis`/`fleetly-vol-probe-redis` 历史残留卷；node2 根因观察窗维持（本批尾 90s 零 fatal task error、node.left×25 系事故窗事件非现行）。

## 2026-10-06 记录·九（走查修复批换装 826fd93-conswalk + F2 服务端锚真机复验）

**换装**（b2eda80-n2storm → 826fd93-conswalk，走查修复批 aecf813/300ec2d/f9d42f5/826fd93）：无 flag-day、无新迁移（00023 仍是末位）；前置 Platform Backup + 七卷 tar（/root/upgrade-826fd93-conswalk/vols/）。**零扰动断言全绿**：全部服务（用户域 + 五受管域）task 行数与基线逐位一致、tw.dev 200、torchwood-pg 21 表锚不变、journal 干净（旧版 context canceled 为关停噪音）。

- **新 dist 上架实证**：`GET /` 引用 `index-BvdIo0nD.js`（f9d42f5 的新指纹）——F1（Stop 反向重提交）/F3（空流结束态）/F4（空项目禁查）前端修复已对现役流量生效；Console HTTP 五项复验绿（index/asset/SPA fallback/401）。
- **F2 服务端锚真机复验（走查复验第一锚）**：`/v1/logs?text=error&follow=1` **首帧 75ms**（修复前 = 零字节无限假挂死；积压段先行出帧生效）；非 follow 的 no-match 走 200 + 干净关流（F3 服务端契约）。
- **残留边（观察项，回传修复归属方）**：`text+follow` 且**积压零匹配**时仍 3s+ 零字节——follow 靠积压帧触发 grpc-gateway 写头，空积压无首帧则头仍悬着（F2 同类边的空集分支；UI 恒带 since 时可能不可达，curl 面可观测）。处置建议：gateway 层或 writer 链补"开流即写头"的空帧/哨兵——需设计裁决，不随记档顺手改。
- **F1 Stop 点击级行为**（走查复验第二锚）属 UI 面：新 dist 已上架 + f9d42f5 单测承载，浏览器点击级复核留走查环境（可选）。
- 两个 anchor token（anchor-check/anchor-check2）已吊销；匿名卷 27 枚稳定。
- **失败行出清实测（记·八观察项落地，2026-10-06 04:33Z）**：全表翻页（200/页 × 585 页）共 **116,802 行**，最老行 finished_at = `10-04T04:33:53Z`，测量时刻 now-48h = `10-04T04:33:52Z`——**清扫资格前沿与 48h 线贴合到 1 秒**（线外行零残留，机制满速在役）。**完成时点修正：交接估的"数分钟内清完"不成立**——资格线按墙钟推进（出清速率 ≈ 原风暴发射速率 ≈1 行/秒），全清预计 **10-07 ~13:59Z**（风暴止点+48h）；期间 `databases backups` 翻页量逐小时下降即为健康形态。附 API 面小坑记档：`ListBackups` 请求 limit>200 时静默落回默认 50（钳到默认而非钳到上限，repo.go ListByDatabase）——翻页统计用 `--limit 200`。

## 2026-10-06 记录·十（F3.1 Console 写面批换装 161b62b-f31console → 161b62b-f31walk + 蓝绿叙事真机锚）

**换装**（826fd93-conswalk → 161b62b-f31console，四 commit 48b963e/6f2bf60/ab70c9e/161b62b）：前置 Platform Backup `aadd7634` + 卷 tar（fleetly-db-三卷 + zot 卷 + torchwood-pg 旧名卷）；**00024 迁移随批前滚**（from_generation 列——826fd93 仍是 00023 末位，ADR-0048 引擎批的迁移首次上真机）。同日二次换装 `161b62b-f31walk`（走查发现 W1 修复，仅 spec/normalize.go 变更）。

- **零扰动断言带注（新形态）**：task 行数 22 服务 19 逐位一致；**3 个用户域服务一次性滚动**——ADR-0048 引擎批（e2e20e6..8e8a925）首次上真机的载体 spec 收敛（P16 双别名渲染 vs 旧代码物化存量；messaging/mlbridge 为停服窗内应用自崩 exit 1 自愈 ×3）。收敛后复测 STABLE-NO-FURTHER-ROLLS；tw.dev 200（注意：**manager 本机 curl 公网域名 000**——出站自环形态，探针走 `--resolve <公网IP>` 或 localhost+Host，本机直连 LE staging CA 需 `-k`）；torchwood-pg 21 表锚不变。
- **蓝绿叙事真机锚（F3.1 旗舰）**：walk-f31/bgwalk 项目 compose blue-green（whoami）——第二笔部署 REST 面带 `"from_generation":"1"`；observing 时点**两代 swarm 服务并存各 1/1**；收口后唯一在役服务容器 Aliases = `web`/`web.bgwalk`/`web.g5`（三别名形态真机在役）；R1..R3 revisions 全带 `"process_strategies":[{...DEPLOY_STRATEGY_BLUE_GREEN}]`；卡 L1 部署 cancel 写面 200。**坑复证：swarm 服务名/标签全小写（sanitizeNamePart），grep 大写 ULID 恒空**——交接单坑①在 service ls 过滤上同样成立。
- **走查发现 W1（同日修复）**：compose 声明 `ports` 不挂项目 default 网（image/upload 形态有 portDeclNetworks、compose 路径漏）→ Route 502（404 半边成立、可达性半边缺失）。修复 `internal/spec/normalize.go`：ports 在场且 networks 未列 default 补挂（spec_file 用户亲笔不改写）；`TestNormalizeComposePortsAttachDefaultNetwork` 三锚；修复后复验 walk.dev 200 端到端（whoami 主机名 = 收口代服务）。
- **观察项**：①`GET /v1/databases` 缺 project_id 回 404 E_NOT_FOUND（ListApps 同场景是 400）——读面口径不一致，留 API 面小裁决；②e2e 升级矩阵覆盖不到"旧代码物化存量载体"的收敛滚动（夹具无存量）——后续引擎批上真机应预期同类一次性收敛并预记基线差。
- walk-f31 走查痕迹全清（app→db→project 级联删除；`fleetly-vol-walkredis` 卷手工清——**级联删库不清卷**，与残留清理先例同形态）；console-walk-f31 token 已吊销；浏览器级 UI 走查本环境无浏览器后端未做（HTTP/消费契约级全绿，docs/reviews/2026-10-06-console-walkthrough.md——渲染层 tsc+vitest 承载，浏览器补档待有后端环境）。

## 2026-10-06 记录·十一（F3.2 exec 子面批换装 05df932-f32exec2 + 反向中继真机全链）

**换装**（161b62b-f31walk → 05df932-f32exec2，五 commit 3829263/0e50fb8/bd29438/console 批/05df932）：前置 Platform Backup `c8a74e33` + 卷 tar（受管四卷——含 probe 遗留 archredis；torchwood-pg 已非独立卷名）；**00025 迁移随批前滚**（audit detail 列）。零扰动：34 running task 行稳定；tw.dev 200。

- **回环代理（manager）零动作在线**：daemon 起服即进程内回环代理连自身 gateway（10.124.0.3:9081，明文 VPC 形态）——`nodes list` manager 行 `relay_online=true`、无容器（与 worker 容器形态分立）。
- **worker 代理落地 = AgentCommand 原样执行**：`fleetly --json nodes enroll` 的 `agent_command` sed 抽取（纯单引号形态 JSON 转义恒等——契约实测成立）→ node2 `sh` 执行：busybox:1.37 载体 + 41MB binary 经 `/v1/platform/binary` 下载 + docker cp 注入 + docker.sock 挂载 `fleetlyd relay` 起服（Up 即连，`relay_online=true`）。
- **exec 真机全链**：worker 侧（probe/web，node2 经反向中继）one-shot `echo` + **TTY 交互 shell**（管道灌命令实测：pty 回显/执行/`exit 0` 退出码透传）双绿；manager 侧（n0reg/web 回环 + torchwood/server 真负载 `/bin/hostname` 返回容器名）；错误进程名 → `E_NOT_FOUND: no running instance ... <workload-id>` 诚实信封。
- **WS/binary 契约探针**：`/v1/exec/stream` 无票 401、票据换流 **101 升级**（HTTP/1.1 upgrade 经真 gateway）、同票复用 401（单用途）、`/v1/relay` 无凭证 401、`/v1/platform/binary` 坏 token 401；票据铸造 REST（POST /v1/exec/sessions）protojson snake_case 全形。
- **审计/事件**：`audit --action exec.` 行 actor/source（cli/api 分立）+ Detail 命令面；`exec.session_opened` 事件在 outbox（注意 events list 是 after_seq 游标语义——从窗头起查尾部事件要 `--after-seq`，walkthrough 坑①）。
- **走查咬出 W1（同日修复）**：Provider 哨兵（无在跑实例）未进受理位信封映射 → E_INTERNAL 吞错因（3ms 快败无诊断面）。修复 = capability 跨层哨兵 `ErrExecNoRunning` 单源 + engine 归一 E_NOT_FOUND + 信封带 workload-id。**错因面**：目标 app 的 process 名错用（probe app 的进程是 web——`docker service inspect` 标签核对是排障第一步）。
- **TTY stdin EOF 语义实测**（e2e 咬出 + staging 复证）：非交互 `shell </dev/null` = 连接半关闭 → daemon 收口 TTY exec（退出 137 形态，非挂死）——交互面不受影响（真终端 stdin 常开）。
- **代理运维面**：AgentCommand 幂等（重跑 = rm -f 旧容器重建——e2e 实证单容器收口）；**rotate 双 token 后旧代理失联待重跑**（C3 泄漏处置语义）；**平台升级后代理二进制滞后**——帧协议只增容忍、`nodes list` 的 `relay_agent_version` 回显滞后，升级序补一步"worker 重跑 AgentCommand"（本批 node2 已重跑至 f32exec2 同版）。
- 残留清理（上批挂账）：torchwood 项目 buildprobe/staticprobe/railpackprobe 三 app 删除（级联拆载体）+ `fleetly-vol-archredis`/`fleetly-vol-probe-redis` 孤儿卷删除。

## 2026-10-06 记录·十二（F3.3 模板库批换装 9df27c9-f33tpl + 模板面真机全链）

**换装**（05df932-f32exec2 → 9df27c9-f33tpl，七 commit 9cb011c/…/9df27c9）：前置 Platform Backup `013d0362` + 卷 tar 五份（`/root/upgrade-f33/`）；**00026 迁移随批前滚**（template_catalog 单行快照表）。零扰动：受管 db task 行零新增（Running 8h 不变）；tw.dev 200 / n0.dev 200 / ml-api 415（预期形态）；worker AgentCommand 重跑（升级序既有步）→ 双节点 relay_online=true。走查判定 PASS，详见 `docs/reviews/2026-10-06-template-walkthrough.md`。

- **版本戳纪律**：本地构建换装必须带 mise build 同款 ldflags（`-X main.version=<shorthash>-<slug>`）——裸构建 `fleetly status` 显示 `server=unknown`，走查第一发即咬出（17:32 重装收口）。
- **模板面真机锚**：CLI `templates list`（source builtin）+ REST `/v1/templates[/name]`（gateway :9081，digest/变量声明规范形）；`templates refresh` 未配置 `server.templates_catalog_url` → E_INVALID_ARGUMENT 精确拒绝（内嵌目录即全部——staging 常态形态，刷新腿在 e2e dind 覆盖）。
- **实例化全链**：nginx（string+domain 链）66s 全绿（真镜像拉取）→ Route 80 口 200 "Welcome to nginx!"——**模板 routes 声明的 tls 缺省 none**（quickstart 同款；e2e 首发踩 CreateRoute 服务端缺省 auto → dind 无 ACME 404，模板面显式收口）；grafana（secret 链）→ `template:dash:admin_password` 指纹面在场、值零回显、GF_*__FILE 文件注入 302 登录重定向（应用活体）。幂等重跑 reused 报告面真机一致。
- **台账**：`template.instantiated` ×3（aggregate=app，payload template@version+deployment 三元组）+ audit `template.instantiate` ×3（AfterFP=模板名@版本）。
- **走查脚注**：事件尾被 `database.backup_failed` 洪水覆盖（既有面，每拍轰炸，处置另行）——台账取证走 outbox 直查（manager 有 python3 无 sqlite3 CLI，`sqlite3.connect(file:…?mode=ro)` 可用）；`events list` 大窗翻页脚本要小心 limit 页帽（after_seq 游标语义不变）。
- 残留清理：走查项目 staging-tpl 整体删除（routes→apps→project 序；apps delete 拆载体连带路由）；`template:dash:*` secret 行随项目材料保留（database: 凭证同口径——平台级命名空间，无级联清理面）。

## 平台升级操作序（F2.3 工具化，2026-10-04）

ADR-0015 升级序的完整落地形态：**Platform Backup 前置 → SIGTERM 排水 → 二进制替换 → 起新版（goose 前滚 + Managed Provider 逐个 reconcile + 解除只读，全自动）**。前置动词自 75a3d31 起可用（旧版无 platform 组时按 b4cfea0 节的手工快照纪律执行）。

1. **Platform Backup 前置（硬停分支——备份没成=不得动二进制）**：
   ```sh
   fleetly platform backup        # 同步执行；失败即中止升级（E_PLATFORM_BACKUP_FAILED）
   fleetly platform backups       # 快照在场核对
   ```
   前置条件：restic 0.19.1 在场（install.sh 4d 节；旧装机手动补）；systemd 面缓存目录已修（22b2f1a 起——旧版报 "unable to locate cache directory" 即此）。
2. **手工卷快照（belt-and-suspenders，沿用 b4cfea0 纪律）**：受管库卷与 zot 卷 tar 至 /root/upgrade-<版本>/（卷名公式 `fleetly-vol-<名>`；受管域已钉控制面节点——F2.3b，不存在漂移到 node2 的卷）。**10-04 实录：这一步是空库事故里真数据唯一的完整来源——不可省**。
3. **排水替换**：`systemctl stop fleetlyd`（30s 排水窗）→ `install -m 0755 <新版>/fleetlyd /usr/local/bin/fleetlyd`（fleetly CLI 同批）→ `systemctl start fleetlyd`。
4. **健康核对**：`fleetly status` / `fleetly doctor`；journal 无 goose 报错。首启路由冷窗 ~60s（后端未解析诚实跳过→下一拍重发布）属已知有界行为。
5. **升级零扰动断言**（CI 锚的手工同款 + 10-04 补强）：
   - `docker service ps` 三件负载 task 行零新增（受管域 spec 变更除外——预期滚一次）；
   - Route 探针 200；
   - **数据面核对**：受管库 exec 查已知表（表数/行数）——**路由 200 ≠ 数据在场**（前端+进程级 healthcheck 曾掩盖空库 14h，见 10-04 记录 P0 ①）。

### 失败回滚路径（goose 前滚失败 = daemon 拒引导）

- **特征**：新版起服即退（`state: goose up` 报错；启动序 = 先迁移后服务——迁移不过不引导，不存在半迁移服务态）。
- **回滚序**（数据根恢复 = Platform Backup 重放，ADR-0015"失败回滚 = 恢复备份重放"）：
  1. `systemctl stop fleetlyd`。
  2. 数据根旁路重建（旧数据根整体移走，不原地覆写）：
     ```sh
     mv /var/lib/fleetly /var/lib/fleetly.failed-<日期>
     mkdir -p /var/lib/fleetly
     cd /var/lib/fleetly
     RESTIC_PASSWORD=$(cat /var/lib/fleetly.failed-<日期>/keys/platform-backup.key) \
       restic -r /var/lib/fleetly.failed-<日期>/platform-backups/restic \
       restore latest --target /var/lib/fleetly
     # 仓密在备份集外（离机保管）——若数据根整体移走后 keys/ 缺失，从离机副本取回 platform-backup.key
     ```
  3. **旧版二进制回装**（goose 只前滚：新版迁移已写入的库，旧版二进制读不懂 schema——这正是要恢复备份而非直接回装的原因）。
  4. `systemctl start fleetlyd` + `fleetly status`。
  5. 失败数据根保留取证（`/var/lib/fleetly.failed-*`），确认稳定后清理。
- **边界**：Platform Restore 期间控制面只读语义 = 恢复窗口内不引导服务；受管库卷/zot 卷不在 Platform Backup 集内（平台只快照数据根）——数据面回滚依赖第 2 步的手工卷快照。

### staging pg 旧布局卷迁移（F2.2 挂账；2026-10-04 实录改写——原六步序作废）

**勘误**：原「backup→verify→delete→volume rm→recreate --restore」六步序有绿门之下的数据丢失洞——升到 F2.2+ 的首启 db 滚动会把旧布局卷变成**空库服现**（镜像 VOLUME 遮蔽，见 10-04 记录 P0 ①），随后的 backup 捕获空库、verify 也照绿；且同名 recreate 在卷未真删时复用残留数据=假恢复（10-04 实录两连踩）。真机实际的布局迁移形态=**停服窗内卷内目录改名**（2026-10-04 torchwood-pg 实操成功）：

1. `fleetly platform backup`（前置）。
2. 手工卷 tar（belt-and-suspenders）。
3. 停控制面（`systemctl stop fleetlyd`，防 reconcile 干扰）+ `docker service update --replicas 0 <db service>` 冻结。
4. 卷内改名（helper 容器）：原始旧布局（数据在卷根）= 卷根内容全部 `mv` 进 `<卷>/pgdata/`；F2.2 过渡形态（数据在 `<卷>/data`，遮蔽修复前的空库期）= `mv <卷>/data <卷>/pgdata`。
5. `--replicas 1` 起库 → **数据核对**（已知表行数）→ `systemctl start fleetlyd`。
6. 变更后 `fleetly platform backup`。

重建+恢复（delete → 真删卷 → recreate `--restore-from-backup`）保留为**灾备路径**（CI e2e 四引擎演练常态回归）。真机操作要点（10-04 scratch 实录）：删库后容器 GC 有窗，`docker volume rm` 需循环到成功（残留即假恢复）；恢复是异步任务，库状态先 running、数据后到（等 `database.restore_*` 收口或隔 30s 断言）。**pre-F2.2 存量项目网不 attachable 会挡 Database Backup/Restore 的 utility 附着**（见 10-04 记录——已由 `fleetly networks rebuild` 收口，ADR-0046/F2.2 节闭合注记）。

## KEK 轮换操作序（`fleetlyd admin rewrap`，2026-10-03 工具化）

数据根 `keys/master.agekey` 是平台 Secret（含受管库凭证）与 hook webhook secret 的 age 信封 KEK（ADR-0014）。泄露应对与例行轮换走本序（工具化前为手工 SQL 重写，废弃）。文件名约定即协议：`master.agekey` = 现役（唯一加密钥）；`master-*.agekey` = 退役（仅解封，rewrap 与 daemon 一并装载）；其他文件名（如 `master.agekey.bak`）不进装载面。

1. **停 fleetlyd**（`systemctl stop fleetlyd`）——维护面是停机窗口操作（SQLite 单写者 + KEK 轮换窗口），daemon 运行时禁止执行。
2. **轮换文件序**（顺序敏感：旧 key 先退役改名，新 key 才能就位现役名；文件内容必须只含 identity 单行，age-keygen 的 `#` 注释头会导致解析失败）：
   ```sh
   cd /var/lib/fleetly/keys
   mv master.agekey master-retired-<日期>.agekey      # 旧 key 退役（保留解封）
   age-keygen | grep -v '^#' > master.agekey          # 新 key 就位现役名（chmod 600）
   ```
3. **dry-run**（解封全量验证 + 报告将重封条数，不落库）：`fleetlyd admin rewrap --data-root /var/lib/fleetly`。输出 secrets/hooks 的 total / to rewrap / already current / failed 四列；`failed > 0`（报 `cannot be opened`）= 有行连退役 key 都解不开（key 全损或外来行）——先停下核对，执行模式整体拒跑、不写任何行。
4. **执行**：`fleetlyd admin rewrap --data-root /var/lib/fleetly --execute`。单事务全量重封（secrets 全表含 tombstone + app_hooks 的 webhook secret 信封；指纹不变，任一行失败整体回滚）。幂等：已封到现役 key 的行跳过，重跑零重写。`--json` 形态供自动化核账。
5. **重启 fleetlyd**——daemon 装载面与 rewrap 同源：现役加密 + 退役兜底解封，轮换窗口内重启不破坏旧密文注入路径。
6. **退役旧 key**：报告全 0 重封且平台 Secret 注入正常后，删除 `master-retired-*.agekey`（Platform Backup 含密封密钥，恢复走解封闭环；KEK 本体单独保管）。

**边界**：KEK 全损 = 全部信封密文不可恢复（备份恢复同理）；轮换窗口内"新 key 就位但未 rewrap"期间，新写入的 Secret 已用新 key 封装，属正常中间态。

## 教训与边界

- **本机（Windows 工作机）出站对该 VPS 全端口受限**（80/443/8420 全 000；node2 路径全通）——外部验证走 node2 或 check-host 类服务，勿信本机 curl。
- 归档版残留：`/var/lib/fleetly.archived-20260930`、`/root/fleetly-archive-*`、`/opt/fleetly`（旧二进制+config）——回滚资料，退役不迁移；确认不再回滚后可清。
- manager 内存 4GB 是硬约束：受管面每实例 ~86MB 级，任何"逐拍滚动替换"类回归都会很快显形。
- nodes 表会保留历史行（swarm 重建前后平台 ID 不同、旧行 available=false）——观测缓存非权威、ID 永不复用，CLI 侧按 available=true 取现行。
- **受管 zot 边界（ADR-0019 附录 B.5）**：~~数据卷节点本地无钉住~~（F2.3b 429b521 收口：带卷受管 Workload 钉控制面节点——zot/proxy 均已钉住；spec 变更不再漂移）；镜像无 GC（只增）；新 worker 加入时 dockerd 必须带同款 `--insecure-registry 10.124.0.3:5000`。
- **ssh 命令里的 `$()`/管道在 Windows 侧会被转义吃掉**——远程复杂操作一律写脚本→scp→sh（本 runbook 2026-10-02 的全部诊断脚本在 manager `/root/dogfooding/`）。
- **dbtemplate digest 钉不防 registry 侧清退**（上游删 digest 后重拉失败；ADR-0045 决策 5 诚实边界）：换装/新装前预拉镜像或评估镜像入受管 zot（超射程记档）；e2e 预拉只暖层不暖 index，digest 解析需 registry 可达一次。
- **dbtemplate digest bump 批必跑 VOLUME 契约 live 核对**（2026-10-05 N2 评审 P2-1 执法补强；ADR-0045 执法补强节）：改 adapter 钉定对常量的批次，本地实跑
  ```sh
  FLEETLY_DBTEMPLATE_LIVE=1 go test ./internal/engine/dbtemplate/ -run TestVolumeShadowContractLive
  ```
  （经 Docker Hub 匿名 API 读钉定 index 的 VOLUME 集与唯一预期表对账；需出网可达 registry-1.docker.io 一次，缺省 SKIP 不红 CI。实测锚：mongo 另声明 /data/configdb——P2-4 匿名卷泄漏的镜像遗产面。）红 = 上游 VOLUME 面变化：按 ADR-0045 决策 4 评估射程（patch 内不变量被破坏 = major 级变更走独立 ADR），不许静默改表。
- **docker 日志驱动默认 json-file 无轮转**（ADR-0040 卫生挂账）：磁盘占用无界 + VL 断流补窗深度以日志文件在场为界；运维建议 daemon.json 配 log-opts max-size/max-file（改后新容器生效）。

### 端口暴露矩阵（操作者责任 + 平台自证，ADR-0036）

控制面与受管数据面的端口**全部只允许 VPC/内网可达**（云防火墙/安全组封公网入口；下表是本 runbook 拓扑的核对清单，任何新端口入网前先在此登记）：

| 端口 | 面 | 认证形态 | 暴露要求 |
|---|---|---|---|
| 9080 | fleetlyd gRPC（控制面 API） | Token（authn 拦截链） | 仅 VPC/内网；CLI 经 manager 本机回环或跳板访问 |
| 9081 | REST gateway（SSE/幂等等同源面） | Token | 仅 VPC/内网 |
| 9082 | Proxy config 拉取端点（traefik HTTP provider） | 共享令牌头可选（`server.proxy_config.auth_token`，缺省关=升级零扰动；traefik providers.http.headers 原生通道——ADR-0036 N2 兑现节 1） | 仅 VPC/内网，**公网可达 = 任意人可改写全量路由**（置 auth_token 后未带头的拉取 401，仍建议钉内网绑址） |
| 5000 | 受管 zot（镜像仓库） | HTTP 明文 + 单一平台凭证（htpasswd） | 仅 VPC/内网；两台 dockerd 的 `--insecure-registry` 同依赖此形态 |
| 9428 | 受管 VictoriaLogs（日志存储，F2.4） | basic auth（keys/victorialogs.json 随机密码；VL 单租户——域隔离由平台查询构造执法） | 仅 VPC/内网；mesh 端点无凭证 401 已实证 |
| 8428 | 受管 VictoriaMetrics（指标存储，F2.5） | basic auth（keys/victoriametrics.json 随机密码；单租户同 VL 口径） | 仅 VPC/内网 |
| 8080 | 受管 cAdvisor（每节点指标采集端，F2.5） | **无认证**（cadvisor 无认证面；host 直绑每节点） | 仅 VPC/内网；**多租户前挂账**（前置认证代理或节点防火墙收窄） |

**绑面配置化（ADR-0036）**：四面绑址/引用地址全部可配置（缺省 = 上表现状，升级不静默改绑）。本拓扑的收窄配置示例（fleetlyd 配置文件，钉 VPC eth1 地址）：

```yaml
server:
  grpc:
    addr: "10.124.0.3:9080"   # 注意：CLI 的 FLEETLY_ADDR=127.0.0.1:9080 需同步改指（或经跳板）
  http:
    addr: "10.124.0.3:9081"
  proxy_config:
    addr: "10.124.0.3:9082"
registry:
  addr: "10.124.0.3:5000"     # 与旧通道 env FLEETLY_REGISTRY_ADDR 同键，config 值优先
logging:
  addr: "10.124.0.3:9428"     # F2.4 受管日志存储（VL）；env FLEETLY_LOGGING_ADDR 同键；空 = 面停用
```

**自证**（防火墙失配从人工核对降为一条命令）：manager 侧带同组 env 跑 doctor——

```sh
FLEETLY_PROXY_CONFIG_ENDPOINT=http://10.124.0.3:9082/proxy/config \
FLEETLY_REGISTRY_ADDR=10.124.0.3:5000 \
fleetly doctor
```

`proxy config exposure` / `registry exposure` 两检查对配置地址做公网可达性探测：公网可达即 fail/warn，公网地址不可达 = `self-certified` ok；`bind surface` 行汇报 gRPC/gateway/proxy config 三面生效绑址，通配绑定（`0.0.0.0`/`::`/`:port`）显式 warn——钉绑后把同值经 `--bind-grpc` / `--bind-http` / `--bind-proxy-config` 传给 doctor 消警示。

已知边界（记档不遮掩）：

1. **zot 平台凭证全域可读**：任何租户可拉他人镜像——单租户窗口下接受；多租户前必须按租户隔离或经 Proxy 前置认证（per-Project 凭证/前置认证已裁决推迟 N2，ADR-0036 决策 3，不静默升级）。
2. **9082 前置认证已具备、缺省关**：`server.proxy_config.auth_token` 置值后拉取端点要求 `X-Fleetly-Proxy-Token` 头常量时间比对（traefik 受管实例随 token 增同名头，providers.http.headers 原生通道）；Unix socket 形态已否决（traefik http provider 无 unix scheme 支持，ADR-0036 N2 兑现节 1）。多租户启用前置=置值。

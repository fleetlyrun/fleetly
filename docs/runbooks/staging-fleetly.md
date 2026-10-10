# staging 重装 runbook（新 fleetly 接管，2026-09-30 实录）

| 状态 | 日期 | 关联 |
|---|---|---|
| 现役：新 fleetly 单机 `fleetly-dev`（2026-10-08 用户口径：staging 收缩为一台，泛域名 `*.dev.fleetly.run`；node2 k3s 实证环境记录见 `k3s-runtime.md`） | 2026-09-30 | 功能清单 F0.18/F0.19 真机验收、F1.15 前哨；归档仓 runbook `fleetly-archived/docs/runbooks/vps-dogfooding.md`（历史教训） |

## 拓扑与连接

- **manager** = `ssh root@fleetly-dev.deeploop.net`（146.190.58.0；VPC eth1=10.124.0.3；Debian 13 / 2C / 4G）。新 fleetlyd = systemd `fleetlyd.service`（数据根 /var/lib/fleetly；unit 另注入 `FLEETLY_PROXY_CONFIG_ENDPOINT=http://10.124.0.3:9082/proxy/config` + `FLEETLY_PROXY_ACME_EMAIL` + drop-in `browse.conf`：`FLEETLY_BROWSE_HOST_SUFFIX=dev.fleetly.run` + `FLEETLY_BROWSE_GATEWAY_URL=http://10.124.0.3:9081`，2026-10-07 起）。
- **worker → k3s 生产实证节点（2026-10-08 改造，ADR-0055 决策 1）**：`ssh root@143.198.234.68`（VPC eth1=10.124.0.5）已退出 swarm 改纯 k3s 单机（k3s server + fleetlyd systemd，独立平台身份/数据根；docker daemon 停用 disable）。**swarm 现役拓扑 = manager 单节点**（node2 任务经 drain 全迁 manager，dogfooding 无断流；sec-test 等手工载体续跑 manager）。k3s 侧操作序/实录/回滚（还原 swarm worker）见 `k3s-runtime.md` staging 节；worker 侧 swarm 操作（RelayCommand 重跑等）自本改造起不适用。
- DNS：DNSPod 通配 CNAME `*.dev.fleetly.run → fleetly-dev.deeploop.net`（n0.dev 实证解析）。
- manager dockerd 带 drop-in `--insecure-registry 10.124.0.3:5000`（zot 走 HTTP；VPC 内网形态）；node2 dockerd 已停用（swarm 退出，k3s 用 containerd）。
- CLI 凭据在 manager `/root/.config/fleetly/credentials`（`FLEETLY_ADDR=127.0.0.1:9080` + 自动读凭据）；node2 k3s 平台 CLI 走 `/root/n7cli.sh`（FLEETLY_ADDR=10.124.0.5:9080）。

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

- **回环中继（manager）零动作在线**：daemon 起服即进程内回环中继连自身 gateway（10.124.0.3:9081，明文 VPC 形态）——`nodes list` manager 行 `relay_online=true`、无容器（与 worker 容器形态分立）。
- **worker 中继落地 = RelayCommand 原样执行**：`fleetly --json nodes enroll` 的 `relay_command` sed 抽取（纯单引号形态 JSON 转义恒等——契约实测成立）→ node2 `sh` 执行：busybox:1.37 载体 + 41MB binary 经 `/v1/platform/binary` 下载 + docker cp 注入 + docker.sock 挂载 `fleetlyd relay` 起服（Up 即连，`relay_online=true`）。
- **exec 真机全链**：worker 侧（probe/web，node2 经反向中继）one-shot `echo` + **TTY 交互 shell**（管道灌命令实测：pty 回显/执行/`exit 0` 退出码透传）双绿；manager 侧（n0reg/web 回环 + torchwood/server 真负载 `/bin/hostname` 返回容器名）；错误进程名 → `E_NOT_FOUND: no running instance ... <workload-id>` 诚实信封。
- **WS/binary 契约探针**：`/v1/exec/stream` 无票 401、票据换流 **101 升级**（HTTP/1.1 upgrade 经真 gateway）、同票复用 401（单用途）、`/v1/relay` 无凭证 401、`/v1/platform/binary` 坏 token 401；票据铸造 REST（POST /v1/exec/sessions）protojson snake_case 全形。
- **审计/事件**：`audit --action exec.` 行 actor/source（cli/api 分立）+ Detail 命令面；`exec.session_opened` 事件在 outbox（注意 events list 是 after_seq 游标语义——从窗头起查尾部事件要 `--after-seq`，walkthrough 坑①）。
- **走查咬出 W1（同日修复）**：Provider 哨兵（无在跑实例）未进受理位信封映射 → E_INTERNAL 吞错因（3ms 快败无诊断面）。修复 = capability 跨层哨兵 `ErrExecNoRunning` 单源 + engine 归一 E_NOT_FOUND + 信封带 workload-id。**错因面**：目标 app 的 process 名错用（probe app 的进程是 web——`docker service inspect` 标签核对是排障第一步）。
- **TTY stdin EOF 语义实测**（e2e 咬出 + staging 复证）：非交互 `shell </dev/null` = 连接半关闭 → daemon 收口 TTY exec（退出 137 形态，非挂死）——交互面不受影响（真终端 stdin 常开）。
- **中继运维面**：RelayCommand 幂等（重跑 = rm -f 旧容器重建——e2e 实证单容器收口）；**rotate 双 token 后旧中继失联待重跑**（C3 泄漏处置语义）；**平台升级后中继二进制滞后**——帧协议只增容忍、`nodes list` 的 `relay_version` 回显滞后，升级序补一步"worker 重跑 RelayCommand"（本批 node2 已重跑至 f32exec2 同版）。
- 残留清理（上批挂账）：torchwood 项目 buildprobe/staticprobe/railpackprobe 三 app 删除（级联拆载体）+ `fleetly-vol-archredis`/`fleetly-vol-probe-redis` 孤儿卷删除。

## 2026-10-06 记录·十二（F3.3 模板库批换装 9df27c9-f33tpl + 模板面真机全链）

**换装**（05df932-f32exec2 → 9df27c9-f33tpl，七 commit 9cb011c/…/9df27c9）：前置 Platform Backup `013d0362` + 卷 tar 五份（`/root/upgrade-f33/`）；**00026 迁移随批前滚**（template_catalog 单行快照表）。零扰动：受管 db task 行零新增（Running 8h 不变）；tw.dev 200 / n0.dev 200 / ml-api 415（预期形态）；worker RelayCommand 重跑（升级序既有步）→ 双节点 relay_online=true。走查判定 PASS，详见 `docs/reviews/2026-10-06-template-walkthrough.md`。

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

## 2026-10-10 记录·二十八（IA v3 一期+二期①-⑤a 换装 7a74b9f-iav3p2 + 真机走查）

- **换装**（69d7608-rolloutstall → **7a74b9f-iav3p2（现役）**，Console IA v3 一期 + 二期①-⑤a 五批）：零迁移（goose 停 28）、install.sh 零变更、config 键面零新增。前置 Platform Backup `129a0aff` + 六卷 tar（三库+zot+VL/VM 至 /root/upgrade-iav3/vols/）+ 旧二进制留存。drop-in 五件全存活。doctor 0 failed；tw.dev 冷窗后 200。
- **新面真机**：GetStatus components 五组件真健康（Managed Providers 页真探活 details/ingest freshness "1s ago" 活体）；GetAppSpec 冻结 Spec 回读（Variables tab）；logs --database 真库载体日志（torchwood-pg）；DownloadBackup 真下载（55287B 与台账一致）；RestartComponent zot 真重启（1 carrier，15s 自愈回 healthy；unknown 精确 E_NOT_FOUND）。走查全录：docs/reviews/2026-10-10-console-ia-v3-staging-walkthrough.md（W-1/W-2 crumb 文案小项随批 6 修）。
- **教训增补**：①console HTTP 在 9081 网关（9080 是 gRPC——HTTP/0.9 探针报错即探错口）；②tokens create 的 --role 取 ROLE_ID（builtin-owner）非角色名，且旗标必须前置（位置参数后旗标变位置参数——老坑新犯）。

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

## 2026-10-07 记录·十三（F3.6 数据浏览器批换装 b4953ca-f36browse + browse 面真机全链）

**换装**（9df27c9-f33tpl → b4953ca-f36browse，九 commit b7e8f5a/…/b4953ca）：前置 Platform Backup `08c13298` + 卷 tar 七份（`/root/upgrade-f36/`）；**00027 迁移随批前滚**（browse_sessions 回收台账）。零扰动：torchwood-pg task 行 Running 15h 不变；tw.dev/n0.dev https 200（`:80` 明文口 404 是既有 TLS 路由形态——探针必须走 443 -k）；worker RelayCommand 重跑 → 双节点 relay_online=true 同版。走查判定 PASS，详见 `docs/reviews/2026-10-07-browse-walkthrough.md`。

- **browse 面配置**：unit drop-in `browse.conf` 两 env（host_suffix=dev.fleetly.run 泛解析 ✓ / gateway_url=VPC 9081——traefik 容器可达即可，configEndpoint 同文化）；tls 缺省 none（明文 :80）。**轮询窗坑**：受理后即刻 curl 会撞 traefik 5s 配置轮询（404 假象）——走查链前 sleep 8s；e2e 探针已内置重试窗。
- **CLI 全链**：`databases browse <id>`（pgvector→pgweb、enforcement=session 回显）→ entry 302 + `Set-Cookie flt_browse=<sid>.<grant>`（HttpOnly/SameSite=Lax/MaxAge=剩余）→ 同票二次 **401**（单用途）→ 无 cookie 工具路由 **401**（ForwardAuth 门禁在 traefik v3.5.4 真机生效——middlewares 渲染面首发）→ cookie 取 pgweb 页 200 → `show default_transaction_read_only` = **"on"**（torchwood-pg 真簇上服务端只读执法实证）。
- **quota 面真机**：10min 窗内第 5 会话 → `E_QUOTA_EXCEEDED`（per-Team 并发 4）——信封 suggestion 是 E_QUOTA_EXCEEDED 共用文案（browse 语境下"等会话到期/空闲回收"已在 message 里）；走查会话靠空闲回收（10min 无接触），无需手工清理。
- **载体面**：`fleetly-browse-<sid>` 1/1（与 e2e 一致）；**硬 TTL 回收真机精确生效**——四走查会话在 created+30min 整点后 Remove 拆载体 + 删行（rows=0/carriers=0）+ 路由随发布消失。**空闲窗观察**：staging 走查会话活到硬 TTL（10min 空闲未提前收——最可能是走查探针的迟到接触；空闲判定 fake-clock 单测绿，硬 TTL 是外层 belt 且实证兜底）。
- **挂账**：E_QUOTA_EXCEEDED 的共用 suggestion 文案对 browse 语境欠贴切（后续批随 quota 文案分立收口）；mysql 只读角色铸造（服务端执法）与 redis/mongo 的角色铸造挂账（ADR-0051 决策 6）；Console enforcement 层级展示留后续批。

## 2026-10-07 记录·十四（四挂账浏览器级走查补档批 + W1/W3 修复换装 6ff4f08-w1fix）

F3.1/F3.2/F3.3/F3.6 四批走查的浏览器级挂账在后端在场的环境（ZCode IAB）一次收口：隧道 `127.0.0.1:19527`（显式绑 127.0.0.1，18081 wslrelay 劫持坑既有）；走查凭证 `console-walk-browser`（owner）+ `console-walk-sse`（member，SSE 活体专用），毕吊销（401 复核）。判据与证据全量见四份报告补档节（docs/reviews/2026-10-06-console-walkthrough.md 浏览器级补档节〔含 F3.2 终端页小节〕/ 2026-10-06-template-walkthrough.md / 2026-10-07-browse-walkthrough.md）。

- **走查咬出 W1（P0，当日修复 `6ff4f08`）**：Console Modal 壳层 `method=dialog` form 包业务表单 = 嵌套 form（非规范 HTML）——真实浏览器内层 submit 不冒泡出外层 form（捕获相达 root、冒泡相截断），**全部 Modal 表单（新建项目/应用/部署表单/五类资源/token/task/schedule/模板实例化）在真实浏览器提交零动作**，未 preventDefault 的默认提交还带 `?` 整页跳转；jsdom 冒泡不同 → 组件测试全绿漏网。修法 = 壳层去 form + ✕ 改 type=button；ui.test.tsx 四守卫同批。
- **W3（P1，同批修复）**：dialog close/cancel 不冒泡、React 19 委托收不到——Escape 关 Modal 后受控态失同步，再点同入口无响应（Templates grafana 详情实测死窗）。修法 = 原生 `close` 监听直挂 dialog 节点；Escape/✕/背板三路归一。
- **换装**（b4953ca-f36browse → 6ff4f08-w1fix，console-only commit）：前置 Platform Backup `6b403083` + 卷 tar 四份（`/root/upgrade-w1/`）；goose "current version: 27" 无迁移。零扰动带注：torchwood-pg Running 16h 不变、VL/VM 2 天不动；**zot+traefik（host 面受管服务）重启 reconcile 各滚一次**（STABLE-NO-FURTHER-ROLLS 复测达成；与用户域负载无关——host 端口面服务对 daemon 重启的已知形态，后续批再升级时留意是否复现）。**修复活体复验**：换装后同路径点击 New project → walk-verify 真创建+关窗+零跳转；合成 close 事件（真实浏览器 Escape 的规范事件）→ 态清 → 再开成功；✕ → 再开成功——W1/W3 双闭环。
- **W2（CLI 契约挂账）**：`templates instantiate --project <ID>` 不解析 ID——get-or-create 按名字把裸 ID 当新项目名静默建幽灵项目（实测 `01M49WV6C3…` 项目 + demo-site 落入；Console 项目下拉如实渲染裸 ID 行）；`apps create --project` 却只收 ID——两子命令语义相反，名字/ID 解析归一挂后续批。
- **Logs 修复复验三锚（F2.6 F1/F2/F3 换装后确认）**：text+follow 2.5s 出 200 帧积压 / Stop 翻 Start 且 fetch 计数恒 1 / 空流 `0 frames · ended` + 提示——300ec2d 与 f9d42f5 在 staging 全部生效。
- **走查环境怪癖实录（非产品缺陷，F2.6 双重曝光伪影同族）**：本 IAB 构建（Electron 41/Chromium 146）locator click 全超时（evaluate 直发等价）、**rAF 挂起**（visibility=visible 仍 0 帧——xterm 不落屏，数据面经 WS 帧解码验证：hostname 回真实容器名/exit 码透传/重开面）、**编程式 `dialog.close()` 不发 close 事件**（W3 活体验证改合成 close 事件路径——真实浏览器用户态关闭不受影响）、合成点击触发 `window.open` 被弹窗拦截器挡（browse 弹窗链经 REST 铸票 + 浏览器直访 entry 等价完成）。
- **残留清理**：walk-browser（bgwalk 蓝绿）/walk-qs（quickstart demo）/walk-verify（W1 复验）/幽灵项目（demo-site）四项目按 apps→project 序全删，无残留载体/卷；browse 会话 ×2 硬 TTL 回收（carriers=0）；compose 文件与 cleanup 脚本（/root/walk-cleanup.sh）留档。
- **观察项**：部署详情 observe_deadline 原始 UTC ISO 与相邻字段本地化并存（Logs 时间列 O1 同族）；蓝绿卡收口终局仍写 `← serving g1`；Tasks 子 tab 不共享项目输入；App.tsx "终端页不在导航"注释过时。

## 2026-10-08 记录·十五（N8 收口批换装 840d8da-n8final + staging 单机化口径）

**拓扑口径更新（用户）**：staging 收缩为单台 `fleetly-dev.deeploop.net`，泛域名 `*.dev.fleetly.run`（DNSPod 通配 CNAME 指向不变，本表 DNS 行维持）；swarm 现役 = manager 单节点。node2 的 k3s 生产实证环境（ADR-0055/0056 装机与实录）记录移 `k3s-runtime.md` 维护。

**换装**（6ff4f08-w1fix → **840d8da-n8final（现役）**，N4-N8 k3s 批 + traefik ACME 修复面；区间核验：无新迁移、install.sh 零变更、console dist 不在区间、config 键面零新增——k3s Provider 仅注册在册，`runtime.provider` 缺省 swarm 不装配）：按平台升级操作序五步走完——前置 Platform Backup `cdba17e1` + 五卷 tar（zot/torchwood-pg/proxy-acme/VM/VL 至 `/root/upgrade-840d8da/`）+ 旧版二进制留存（fleetlyd-6ff4f08）→ SIGTERM 排水 30s 干净收口 → 起服。

**零扰动断言（STABLE-NO-FURTHER-ROLLS 达成）**：

- **17/19 服务 task ID 逐位不变**（用户域 14 + VL/VM/cadvisor/zot）；零 drift 事件；doctor 10 ok / 0 fail（两 warn 既有形态）；journal 零 error；goose `current version: 27` 无迁移；tw.dev / n0.dev 200；torchwood-pg 数据锚 21 表。
- **traefik 滚一次** = 记录·十四已知形态（host 端口面服务对 daemon 重启），复测无后续滚动。
- **torchwood-pg 恰一次受监督滚动（新形态，预期内记档）**：区间投影 IR 新增 `Workload.EgressNetworks`（无 omitempty，json.Marshal 指纹形态变化）→ **db generation 指纹持久在库表**（`databases.EnsureGeneration`，与受管域 7232da4 的进程内播种续接不同轨）→ 指纹变更即恰一次滚动。**干净关停实证**：旧容器 `checkpoint complete → database system is shut down`（stop-first + 60s 宽限在位，3b3dd86 投资生效），1.2s 后新容器 ready，数据零损。教训：**投影 IR 字段增删对用户域 db = 一次性指纹滚动代价**，后续带 IR 形状变化的升级断言要把"db 恰滚一次（干净关停）"列入预期形态。
- zot 本次零滚——记录·十四的"zot 重启滚动"未复现（同型两样本形态不一，继续观察）。

## 2026-10-09 记录·十六（C1 可观测批换装 778e3a2-c1obs + Console 对齐 Dokploy 路线图开批）

**路线图**：`docs/research/2026-10-08-console-parity-dokploy.md`——地位裁决（**Console 与 CLI 同为一等能力面**；ADR-0044 钉形不变：纯静态瘦客户端、零专属服务端面），对标矩阵（Dokploy/Coolify × 后端 × console 现状）+ 三层缺口（L1 纯消费 / L2 后端小批伴生 / L3 真后端缺口）+ C1-C6 分批建议 + 不追随项（容器 CRUD / i18n / 多租户 Cloud）。待裁决：C6 认证形态（密码会话 vs SSO）、批次优先级序。

**换装**（840d8da-n8final → **778e3a2-c1obs（现役）**）：前置 Platform Backup `64b39b57` + 三卷 tar（zot/torchwood-pg/proxy-acme，VM/VL 沿上一轮 tars）；goose 无新迁移。**零扰动断言强于上轮：本轮零新滚动**（现役 19 task 逐位不变——db 无 IR 变更不滚；traefik 已知形态本轮也未复现）+ 路由 200/200 + torchwood-pg 21 表 + 零 drift 事件。

**C1 批内容（778e3a2）**：
- 后端 `QuerySeries` 多序列：capability.Metrics 返回 `[]Series`（原 decodeQueryRange 首条截断废除——proto 响应本就 repeated series，零契约变更）。多序列真机锚 = staging 单查询 **47 序列**（`container_memory_working_set_bytes{image!=""}`，每序列 13 点）。
- Console `Observability` 页（路由 +1）：metrics tab（项目→App→三指标预设〔cpu cores / cpu % of node / memory working set〕+ custom PromQL + 四档时间窗；零依赖 SVG 多序列折线 + 图例 + 峰值注记）+ alerts tab（规则表 firing 内联 + observed_value + 创建/删除；通道表 test 诚实呈现 delivered/error + 创建/删除——webhook/telegram 双形态，凭证只写不读）。
- Console HTTP 面核验：index 200（新指纹 `index-Ccu1QSDo.js` 对现役流量生效）/ asset 200 / 无凭证 `/v1/metrics` 401。
- 全门禁：go test -race 三 module 绿（本机 sandbox 环境性跳过 railpack 一用例——docker.sock 权限，CI 正常跑）+ lint 0 issues + buf breaking 绿 + console:verify 零漂移 + vitest 35 绿。

## 2026-10-09 记录·十七（C2 治理批换装 db9bc34-c2gov：身份管理页 + git hook 面板 + 平台备份台账）

**换装**（778e3a2-c1obs → **db9bc34-c2gov（现役）**）：纯 console 批（Go 零变更），前置 Platform Backup `c2f926fa` + 双卷 tar。**零扰动再度成立**（traefik task 保持 C1 时代 10h 龄不滚）；tw.dev 首探 000 = 重启后路由冷窗（45s 探太早——已知 ~60s 行为，60s 后连续三探 200），非回归。

**C2 批内容（db9bc34，Console 对齐 Dokploy 路线图第二批）**：
- **Identity 页（路由 +1，四 tab）**：users（建/删，team/role 归属）/ teams（建/删）/ roles（建/删——自定义 scope 表，builtin 只读）/ invitations（建——**secret 一次性展示** + `fleetly users accept --token …` 接引文案 / 列表含 consumed 态）。RBAC 的 UI 消费面首次闭环，动词面对齐 identity 上下文 CLI。
- **Apps 级 git hook 面板**（行动作 `hook…`）：get/set/rotate 三动词对齐 CLI hooks 组；首配表单由 E_NOT_FOUND 驱动；**webhook URL = `<console 同源>/v1/hooks/<secret>` 一次性揭示**（secret 兼 GitHub 签名密钥，配置面只回 token_prefix）；rotate 确认提示旧 URL 立即失效。
- **Settings 平台备份台账**：ListPlatformBackups 只读表（快照 id/time/hostname）——触发面（F3.1 既有）补齐台账面，触发写后即时失效。
- 全门禁：console:verify 零漂移 + vitest 37 绿（Go 面零变更未重跑，C1 全套在案）。

**Console 端点真机核验**：users/teams/roles/invitations/platform-backups/alerts-rules/channels 全 200；新 dist 指纹 `index-CrCyrUsC.js` 在役。

**路线图进度**：C1 ✓ C2 ✓；C3（git 源部署表单 + revisions diff + dokploy 导入 + runs 入口）、C4（nodes 页）、C5（DB verify/restore）待续；C6（凭证第二形态：密码会话 vs SSO）待裁决。

## 2026-10-09 记录·十八（C3 部署深化批换装 f5ed740→81619b6-c3mig2：dokploy 导入真机演练 + 端口声明修复）

**换装**（db9bc34-c2gov → f5ed740-c3mig → **81619b6-c3mig2（现役）**，二次换装带端口修复）：纯 console 批 + 一枚 CLI/平台伴生修复；Platform Backup `05a30b33`（c3mig 轮）+ 追加；零扰动（traefik task 持续保持 C1 时代不滚）；tw.dev 200。

**C3 批内容（f5ed740 + 81619b6）**：
- **dokploy 导入面（Apps 页 `Import dokploy…`）**：解析器 TS 移植（`internal/spec/dokploy.go` 叶子拷贝，fail-closed 语义同构），**parity 由 CLI golden 同款夹具钉死**（skip 文本逐位对齐——迁移钩子的诚实契约）；编排与 CLI create-from-dokploy 同序（project/db/app create-or-reuse → deploy → routes host 复用），计划预览→步报告→数据不搬移注记。
- **revisions diff 视图**（DeploymentDetail 卡片）：任意两代 R..Rn 字段级差异；真机锚 = 两个现役 App 的 diff 面（probe app R1→R2 三条/process 级差异、messaging app R10→R11 21 条 healthcheck 差异）。
- **路线图收口注记**：git 源部署 = C2 hook 面板承载（push 触发即 git 部署路径，CLI deploy 本就无 git 旗标）；Routes TLS 列与 runs 入口盘点为已有覆盖。

**真机导入演练咬出的修复（81619b6，console+CLI 同批）**：
- **image 直投缺端口声明**：首演 deployment succeeded 而路由 404——journal 三连 `route publish: backend unresolved (no endpoint for process "web" port 80)`。根因 = dokploy 编排（CLI 与 console 同病）把 domain 的 port 传给 route 却没传给 DeployRequest.port（字段在场；quickstart 直投同款教训再现）。修复 = image 路径取首条 route 的 port 作端口声明（compose 路径自带声明不适用）。**修复后重演全链绿**：deployment observing→succeeded、route HTTPS 200 "Welcome to nginx" 四连。
- **项目删除→同名重建的秒级竞速（记档未修，平台挂账）**：同名项目删除后立即重建并部署（drill 脚本同秒连发），新项目 default 网络的 create 与部署侧 ensure 撞 AlreadyExists——deployment 一次性诚实失败（`swarm ensure network … already exists`），网络在位后重部署即收敛。正常人间节奏（项目创建与部署间隔秒级以上）不触发；竞速窗根修（ensure 侧 AlreadyExists 容忍/串行化）挂后续平台批。

**门禁**：console:verify 零漂移 + vitest 40 绿（+3 dokploy parity 锚）+ Go dokploy golden 双形态绿。演练痕迹全清（migprobe 项目×2 级联删除）。

**路线图进度**：C1 ✓ C2 ✓ C3 ✓；C4（nodes 页）、C5（DB verify/restore 面）待续；C6（凭证第二形态：密码会话 vs SSO）待裁决。

## 2026-10-09 记录·十九（C4+C5 批换装 c603111-c45：Nodes 页 + 备份 verify/restore——纯消费批收官）

**换装**（81619b6-c3mig2 → **c603111-c45（现役）**）：纯 console 批（Go 零变更），Platform Backup + torchwood-pg 卷 tar；零扰动；tw.dev 200；新 dist `index-D6sMsU14.js` 在役。

**C4 Nodes 页（路由 +1）**：集群成员表（hostname/role/可用态 chip/中继活体 relay_online+版本/last seen）+ 运维三动词（drain 带真迁移确认、cordon/uncordon 按可用态切换）+ **enroll 材料揭示面**（join + relay 两条命令——等价集群成员权的警示文案；rotate join tokens 泄漏处置动词带确认）。动词面对齐 CLI nodes 组（F0.19/F3.2/ADR-0049）。真机锚：manager 在役行 available+relay 双 True；历史行 available 缺省 = runbook 既有记档形态（观测缓存非权威），页面全量诚实呈现。

**C5 数据面深化（Resources databases tab）**：备份行动作面——**verify**（`POST /v1/backups/{id}/verify` 重算 digest，ok/digest/error 诚实呈现；真机锚 = torchwood-pg 最新备份 verify ok=True digest 2abe86…）+ **restore**（行内表单按名建新库带 `restore_from_backup`，异步恢复注记；恢复是异步任务、库行先建后到数据——runbook 灾备路径口径）。

**门禁**：console:verify 零漂移 + vitest 41 绿。

**路线图进度**：C1-C5 全部收官（五批零回归、staging 五次换装零扰动）。剩 **C6 凭证第二形态**（密码会话 vs OIDC SSO）待裁决——见路线图文档 §5。

## 2026-10-09 记录·二十（C6 第一期密码会话换装 81dcced-c6pw：00028 前滚 + 登录全链真机绿）

**换装**（c603111-c45 → **81dcced-c6pw（现役）**）：Platform Backup + 双卷 tar；**00028_users_password.sql 干净前滚**（goose v28）；零扰动；tw.dev 200（冷窗照旧）。

**C6 第一期内容（先密码后 SSO 裁决；SSO 独立后续批）**：
- **凭证模型**：登录成功 = 服务端铸常规 API token（"password session"）——scope/审计/吊销面全量复用 tokens 面（authn 拦截器逐请求查表，吊销即 401），零独立会话表；bcrypt（zot htpasswd 同款）；users.password_hash 空 = 未设密（密码登录诚实拒绝）。
- **端点**：`POST /v1/auth/login`（PUBLIC 位，AcceptInvitation 先例）+ `POST /v1/users/{id}/password`（admin 重置，users:write）+ CreateUser 可选初始密码。诚实边界：login 无限速（多租户前；9080/9081 VPC-only 承载）；失败形态不区分哪半边错（用户名枚举面收窄）；自助改密随 SSO 批裁决。
- **CLI**：`login --name NAME [--password]`（stdin 兜底）+ `users set-password [--password] USER_ID` + `users create --password`。**旗标前置铁律再现**：首个实现把 --password 放在位置参数后，Go flag 停析即咬（runbook 三次记档的第四次实录）。
- **Console**：登录页 Password | API token 双形态；users 建号初始密码字段 + 行内设密面。
- **真机锚**：00028 前滚干净；set-password 200 → login（founder/builtin-admin，secret 一次性返回）→ 会话 token 调 whoami（user=founder role=admin）全链绿；错密码 E_UNAUTHENTICATED。**注记：staging founder 账号已设密码 `c6-live-pw-1`（演练值）——console 密码登录可直接体验；正式使用前建议轮换**（Identity 页 users 行 password… 动作即改）。
- 门禁：lint 0 + buf 绿 + cmd 全测绿（golden 双形态 + identity 密码三步场景）+ console:verify 零漂移 + vitest 41 绿。

**路线图**：C1-C5 ✓ + C6 第一期 ✓。C6 第二期（SSO）与自助改密独立批；Backlog 余项（preview deployments、environments 轴）维持。

## 2026-10-09 记录·二十一（W1'：密码登录嵌套 form 修复——用户报"无法登录"的浏览器级走查实录）

**症状**：用户报 Console 密码登录零动作。浏览器级走查（IAB + SSH 隧道 19527）复现：填表点 Sign in → 整页刷新回登录页、URL 带 `?`（默认 GET 提交签名）、localStorage 无 token——onSubmit 的 preventDefault 从未执行。

**根因（W1 同款复发）**：C6 重写 LoginPage 时**外层布局容器误用 `<form>`**，内层密码/API token 表单成嵌套 form——真实浏览器中内层 submit 归属外层无 handler 的 form，走默认提交。jsdom 冒泡行为不同 → 组件测试全绿漏网（W1 当年 Modal 壳层的漏网机制原样再现，只是换了页面）。**教训固化：form 永远只做业务表单容器，布局容器用 div——现守卫从 Modal 扩到页面级**（ui.test：LoginPage 渲染恰一 form 且无 form 祖先）。

**修复与验证**（761f516-w1p，console-only）：外层 form → div；守卫扩页级；vitest 42 绿。浏览器重走全绿：founder 密码登录 → 完整 Shell（14 路由 + 身份栏 `password session · admin`）→ 截图在案。staging 现役 761f516-w1p，新 dist `index-CfjOVfZB.js`。

**走查环境事实**：19527 隧道是易逝品（会话结束即断）——重走查先 `ssh -N -L 19527:127.0.0.1:9081` 重建；IAB 对本机回环可达（record·十四 口径不变）。

## 2026-10-09 记录·二十二（Console UI 完整浏览器走查：14 路由 PASS + 三修随批 09083ea-walk）

**换装**（81dcced-c6pw → 761f516-w1p → **09083ea-walk（现役）**）：W1' 密码登录修复 + 走查三修，均为 console-only；零扰动。判定 PASS，报告 = `docs/reviews/2026-10-09-console-full-walkthrough.md`。

- **W1'（P0 当轮修）**：用户报"无法登录"——浏览器级走查复现：C6 重写 LoginPage 外层布局误用 form，嵌套 form 致内层 submit 走默认 GET 提交（URL 带 `?`），登录零动作；jsdom 冒泡不同组件测试全绿漏网（W1 同机制换页面复发）。修：外层 div + 守卫扩页面级。教训固化：**form 永远只做业务表单容器**。
- **W2（Nodes 历史行混淆面，当轮修）**：历史注册行与现役行混排（现役沉底+同名 hostname+全带 drain/uncordon 按钮）。修：现役先行 + 历史行置灰 + "unavailable" 措辞（不臆断死活）+ platform_id 悬浮锚；复验现役行置顶。
- **F1/F2（当轮修）**：备份 Size toFixed(2)；指标图例中段截断保 task id 尾段。
- **活体锚**：密码登录全链（截图在案）/指标图表多序列（torchwood CPU）/备份 verify（digest 与记录·十九同值）/Events SSE（following+200 行）/relay 版本随换装实时回显。
- 走查环境事实：19527 隧道易逝（重走查先重建）；IAB locator click 偶发超时但动作实际生效（重读状态，勿盲目重试）。

## 2026-10-09 记录·二十三（Console 产物代码分割：首屏 183→86 KB gzip，9c1fe3d-split 现役）

**换装**（09083ea-walk → **9c1fe3d-split（现役）**，console-only + fleetlyd 重嵌 dist）：用户报"单 js 文件太大加载慢"（单 chunk 680 KB min / 183.7 gzip，Vite 早已警告）。

**修法**：App.tsx 全页面 React.lazy + Suspense（hash 路由零改动；LoginPage 静态进口——首屏态懒加载无收益）；manualChunks 函数式（node_modules 全量 → vendor；`@xterm` 单拆）。

**产物形态**：入口 index 21.9 KB（6.6 gzip）+ vendor 257 KB（79.8 gzip，react 全家桶——跨版本 immutable 缓存，后续发版只拉业务 chunk）+ 业务路由 chunk 2-7 KB/页按需 + xterm 290 KB（72 gzip）仅终端页按需。**首屏 183.7 → ~86 KB gzip（-53%）**；xlsx/xterm 的解析成本只在真正进入终端页时支付。

**浏览器复验**：reload → Nodes 路由懒加载渲染正常；终端页懒加载链（路由 chunk → xterm chunk）按需拉取全通、无控制台报错。门禁：console:verify 零漂移（注：verify 首跑曾报 dist 漂移——pnpm install 后 hash 不稳定的一次性形态，重建即齐）+ vitest 42 绿。

**教训**：console:verify 的漂移断言对"构建非确定性"敏感（同输入偶发异 hash）——复现为零 diff 即非产物问题；若再现按构建环境差异排查。

## 2026-10-09 记录·二十四（Console UI v2 重构换装前置走查：dev 面 + 隧道真数据）

**形态**：v2 尚未换装 staging 现役二进制（现役仍 9c1fe3d-split 的 v1 dist）——走查走 **vite dev + `FLEETLY_DEV_API=http://127.0.0.1:19527`**（vite.config 新增环境覆盖位）直连隧道打真数据；凭证 = founder 密码会话（演练值），走查毕吊销本会话 token（01M4FWKA…；用户自持的三枚 password session 未动）。报告 = `docs/reviews/2026-10-09-console-ui-v2-walkthrough.md`。

- **活体锚（v2 新 UI 全真机）**：密码登录全链 → 壳层（三域 Sidebar+⌘K+主题开关）→ 总览（Nodes 1/4 + 5 项目磁贴）→ /p/语境树（torchwood/messaging 真表）→ App 详情 tab 化 → 部署列表（两代真部署 5s 轮询）→ **部署旗舰页**（R3 全绿时间线 + 进程策略 DNS 面 + diff + Raw 折叠）→ **日志真流**（mlbridge JSON 帧 react-virtual 渲染 + Follow/Stop/Download）→ **指标真序列**（Memory 5 序列坐标轴图表）→ 双主题抽查。PASS。
- **当轮四修**（c7bdc9d）：切换器 store 种子化（展示项目与导航置灰表里不一）/面包屑 ULID 短显/总览 Deploy 死链摘除/根 404 诚实态。
- **F-B1 后端挂账**：`GET /v1/metrics` 对 `/ on(node) machine_cpu_cores` 除法查询 E_INTERNAL（error_id 1426e23def91215ae4340c1f5ac8f100，同形 curl 亦 500；无除法裸指标与 memory 预设正常）——查 VM 侧 machine_cpu_cores 序列与 on(node) join；C1 走查 47 序列锚时的行为需复核。与 Console 前端无关（旧页同预设同病）。
- **换装序（下一批）**：批 5（Identity/Settings/Nodes/Templates/Quickstart/Terminal/Events/Login 的 v2 reskin + Templates 非法 DOM 修复）落齐后 `console:gen && console:build` → fleetlyd 重嵌 dist → 按记录·十四换装序走 staging；届时反模式守卫扩全量 src/**。
- **环境事实增补**：vite dev 代理目标可用 `FLEETLY_DEV_API` 覆盖（走查直打隧道，不必先换装）；shadcn CLI 在本机对 ui.shadcn.com 的 fetch 恒被掐（curl/node fetch 均通）——组件增补走 `components.json` + registry 镜像自装（tools 与内容同 CLI）。

## 2026-10-09 记录·二十五（Console UI v2 换装：9c1fe3d-split → be8ec15-uiv2，零扰动实锤）

**换装**（console-only 变更：v2 dist 重嵌 + 版本戳；workload IR 零变化）：
1. **硬门先付**——平台备份 `cd7b77b1`（fleetly platform backup）+ 三受管库卷 tar 快照（/root/pre-uiv2-fleetly-db-{torchwood-pg,mlredis,twredis}*.tar.gz，13M/292B/1.9K）。
2. 交叉构建 `GOOS=linux CGO_ENABLED=0`（版本注入 be8ec15-uiv2）→ 嵌入校验（`grep index-CLXhB1zM bin/fleetlyd-linux` 与 dist 哈希对账）→ scp → 旧二进制留底 /root/fleetlyd-9c1fe3d-split.bak → /usr/local/bin 换装 → **drop-in 四文件全活核验**（registry/railpack/browse/11-metrics）→ restart。
3. 验证：HEALTHY + version be8ec15-uiv2；正式 dist 哈希在线（index-CLXhB1zM.js）；doctor 10 ok / 2 warn / 0 failed；Nodes 页 relay 版本回显 `online · be8ec15-uiv2`（新二进制经节点注册表实锤）。

**零扰动实锤（对照记录·十二的"换装全量滚动"预告）**：本轮换装窗内**唯一新任务 = sec-test.1 Complete**（一次性任务正常收口）——应用任务零重启（任务时间戳全为三天前）；原因：be8ec15 只改 console 资产与版本戳，workload spec 不漂移 → managedFingerprint 稳定 → 不触发 EnsureGeneration 滚动。**推论**：console-only 变更的换装天然零扰动（无需预付库卷快照级别戒备，但平台备份硬门照付——纪律不因二进制内容而打折）。

**遗留观察（换装前既有，与本轮无关）**：三服务 replicas 长期 N/1（quickstart web 4/1 ×3 + messaging 2/1 ×3，任务时间戳三天前）——desired 与实际失配三天未收敛，属平台缩放链路疑点（非本轮引入；换装窗内零相关任务），独立挂账查 EnsureGeneration 缩容链。

**v2 真机复验（正式 dist）**：密码登录 → 总览（5 项目磁贴）→ Nodes（active/unavailable 徽章 + relay 版本回显）→ Templates（合法 DOM 表 + grafana/nginx 目录）全 PASS；走查毕会话 token 全清（remaining: 0）。

## 2026-10-09 记录·二十六（F-B1 metrics 除法查询 500 收案：三病灶三修 + 全零序列图表修复，be8ec15-uiv2 → 12a28b1-fb1b）

**症状与诊断**（走查 F-B1 挂账，error_id 1426e23def91215ae4340c1f5ac8f100）：GET /v1/metrics 对带除法 join 的 PromQL 恒 E_INTERNAL；journal 只有脱敏信封（`stack:null`，cause 不落盘，4.4ms 失败）。服务器直 curl VM :8428 分层定位：**VM 返回 422 + 明确理由** `duplicate time series on the left side of / on(node)`——cadvisor 每容器序列（多）对 machine_cpu_cores（每节点一条）是 PromQL 多对一除法，裸 on(node) 无 group_left 依法被拒。`machine_cpu_cores` 序列在场且 join 键 node 正确；VM v1.152.0 / cadvisor v0.55.1 task 全部 4 天前启动（数据面零变更）——**历史锚闭案：C1 走查的 47 序列锚是 memory 查询，CPU % 预设从来就没绿过，非回归**。

**三修**（commit 6fb027d / 68f47c1 / 12a28b1）：
1. **透传层错误分流**（后端）：VM 4xx 查询错曾被整链吞成 E_INTERNAL 500 且 cause 不落盘，误导排查方向。capability 新增 `MetricsQueryError`（上游 4xx 类型化）→ provider 提取信封 error 字段 → mapStateError 分流 `E_INVALID_ARGUMENT` 带上游原文（5xx/网络错保持 E_INTERNAL——平台故障面诚实）。provider 双侧测试 + apitest 假面信封断言同批。
2. **预设补 group_left**（console）：`cpu_percent` 模板补 `group_left` 保留 per-container 出线（ADR-0041 cpu_percent 语义本就是 per-container）；staging VM 直查 5 序列正常出值。
3. **全零序列图表空白**（console，二次换装咬出）：首轮复验截图发现 2 series 返回但无线无轴——protojson 对 proto3 double 零值**缺省序列化**（REST 点只剩 time 字段），MetricChart 按 `value==null` 跳点，全零序列 rows 空。VM 实证 n0reg/web 双容器 CPU 全零（idle 常态，此 bug 对 CPU % 预设几乎必现）。`buildChartRows` 抽纯函数，`value ?? 0`（缺省即零），4 例 vitest 锚。

**随批**：guards 三红收口（23f0657，UI v2 批 5 遗漏——UsersService Login/SetUserPassword freeze 豁免 + idem 执法面入表；wording addon 白名单锚迁新 Terminal 路径 + shadcn registry 组件豁免；api-errors 的 tunnel 措辞改 connection）。

**换装两跳**（runbook 序全付）：平台备份 `c684ce09` + 10 卷 tar 快照（479M，/root/upgrade-68f47c1-fb1/）→ 68f47c1-fb1（Go+console）→ 12a28b1-fb1b（console-only 二跳）；doctor 10 ok / 2 warn / 0 failed ×2；受管域 task 全 4 天前（**两跳零滚动**，与记录·二十五 console-only 推论一致）。

**真机复验全绿**：①三预设经 /v1/metrics 全 200（专用 token fb1-verify，查毕吊销）；②坏查询（裸除法）400 `E_INVALID_ARGUMENT` 带上游 "duplicate time series" 原文；③Console 指标页三预设真浏览器出图（n0reg/web：CPU % 双容器 0 基线 + y 轴 0-4% 域 + Memory 8MiB/3MiB 末值图例 + CPU cores）；④Custom PromQL 坏查询呈现 Request rejected 态（非 500 万金油）。

**环境备忘**：本机（LIQIULIN-UBUNTU）qiulin 已入 docker 组（`sudo usermod -aG docker qiulin`，2026-10-09）——运行中会话组列表不刷新，railpack docker 面测试需重启会话后验证（CI 不受影响，本机恒红是旧组列表假象）。

## 2026-10-09 记录·二十七（缩放 N/1 疑案收案：paused 滚动僵尸 task 叠加 + workload.rollout_stalled 观测面，12a28b1-fb1b → 69d7608-rolloutstall）

记录·二十五遗留观察的收案。**交接单三处勘正**：①`docker service ls` 的 REPLICAS 列是 **running/desired**——「4/1」= 4 个活 task / 期望 1，是**超编（僵尸 task 叠加）不是缺编**（「slot 2..N 无任务行」实为同一 slot `.1` 的 `\_` 历史链，slot 2..N 从不存在）；②失配主体是 **torchwood**（dispatcher/server/worker 4/1 ×3）+ messaging（mlbridge/messageloop 2/1 ×2）共五服务，非 quickstart；③平台缩放链无缺口——DB revisions 现行 spec replicas 全 =1，最后部署 torchwood 10-02（gen 13）/messageloop 10-05（gen 2 rollback replay）。

**定性链（三面铁证）**：

- **触发**：journal 显示 10-06 09:21:27 旧版停机 → 09:22:28 `161b62b-f31console` 起服（记录·十换装）→ 09:22:31 baseline replay 对五服务做 ADR-0048 spec 收敛滚动（与五个 `UpdateStatus.StartedAt` 秒级吻合）——非人操作，无审计嫌疑。
- **机制**：滚动 start-first + 新 task `non-zero exit (1)`（停机窗内应用自崩，记录·十实录形态）→ swarm `FailureAction=pause`（translate 层）→ 滚动停中间态 → **start-first 语义下旧 task 永不退役**；10-08 node2 退 swarm 的 drain 迁移又给 torchwood 三服务各叠 task（4 = 10-06 残留 2 + 10-08 迁移 2）。
- **无感三天**：ServiceUpdate 已被接受、spec 面恒一致 → drift 只对比 spec 无感；部署状态机早已 succeeded；swarm `UpdateStatus` 无任何平台反馈面——路由照常 200（僵尸参与 DNS RR，流量打到旧代 task 无人察觉）。

**现场收口（12:51Z）**：快照前置（Platform Backup `164b2435` + zot 卷 tar 132M）→ `docker service update --force` ×5 → 全部 1/1 + `UpdateStatus: completed`（12:51:23→12:52:47 依序收口）→ 路由活体不变（tw.dev 200 / ml-api 415 / ml 404）。psql anchor 报 role 不存在 = 受管库模板自定义角色的探测姿势问题（pg 服务本批零接触，21 表锚在换装断言中补核）。

**平台修复（69d7608，观测面收口——不违 ADR-0005 opt-in，只发事件不自动纠正）**：

- `capability.WorkloadObservation` 新增 `RolloutStalled`/`RolloutDetail`（未观测面零值）；swarm `InspectWorkloads` 透传 `UpdateStatus=paused`（编排器 Message 原话进事件）。
- drift 扫描（compareSpecs）检测停摆 → 新事件 `workload.rollout_stalled`（独立签名去抖 + 恢复即清；与 spec drift 分立——停摆时 spec 恰是一致的，这正是三天无感知的根）。eventcode 注册表只增（Source 锚 compareSpecs）+ schemareg + 三 golden 同批（eventcode/assembly 自描述/CLI explain）。
- 测试同批：engine `TestRolloutStalledEmitsEventWithoutSpecDrift`（去抖/恢复清签名/不产 spec drift）+ swarm `TestInspectWorkloadsSurfacesPausedRollout`（fake daemon 种 paused→stalled、completed→不报）。

**换装**（12a28b1-fb1b → **69d7608-rolloutstall（现役）**）：平台备份 `9c803f03` + 三卷 tar（acme×2 + zot）；goose v28 无新迁移；**零扰动全绿**——六受管域 task id 逐位一致 + SERVICES-IDENTICAL + tw.dev 200 + torchwood-pg 21 表锚 + status healthy。三个中间 commit（1a84826 docs / 379747d CLI 动词面 / 69d7608 观测面）均不动 workload IR，零收敛滚动预期成立。

**分诊序沉淀（runbook 级——「服务 N/1」先读两字段再动手）**：`docker service inspect <svc>` 看 `.Spec.Mode.Replicated.Replicas`（spec 真源）与 `.UpdateStatus.State`：

1. spec.Replicas = 平台期望 + **UpdateStatus=paused** → 滚动停摆（本记录形态）：`docker service update --force <svc>` 恢复；**平台重部署/rollback 对 spec 相同走 no-op 断路器，不会 resume paused 滚动**。
2. spec.Replicas ≠ DB revisions 口径 → 真 drift（对照 revisions 表现行 spec）。
3. REPLICAS 分子 > spec.Replicas → 僵尸叠加（路由可能照常 200，勿以活体判断健康）。

**诚实边界**：①k3s 面暂不覆盖 RolloutStalled（observeFromPodSpec 未填，Deployment 滚动卡住检测留后续批）；②driftScan 只扫 App 域——受管域/Database 域的 paused 检测不在射程（受管域 60s 强制重放对 spec 相同同样 no-op 不 resume，同缺口另一面，受管服务健康概率低，挂账）；③恢复动作纯人工（--force 或带 spec 变更的重部署），不自动纠正。

**门禁**：test 三 module + lint 全绿；builders railpack 测试红 = 本机 docker 组旧会话假象（记录·二十六环境备忘在案，非本批引入）。

# staging 重装 runbook（新 fleetly 接管，2026-09-30 实录）

| 状态 | 日期 | 关联 |
|---|---|---|
| 现役：新 fleetly（N0 批尾 HEAD 起）双节点 | 2026-09-30 | 功能清单 F0.18/F0.19 真机验收、F1.15 前哨；归档仓 runbook `fleetly-archived/docs/runbooks/vps-dogfooding.md`（历史教训） |

## 拓扑与连接

- **manager** = `ssh root@fleetly-dev.deeploop.net`（146.190.58.0；VPC eth1=10.124.0.3；Debian 13 / 2C / 4G）。新 fleetlyd = systemd `fleetlyd.service`（数据根 /var/lib/fleetly；unit 另注入 `FLEETLY_EDGE_CONFIG_ENDPOINT=http://10.124.0.3:9082/edge/config` + `FLEETLY_EDGE_ACME_EMAIL`）。
- **worker** = `ssh root@143.198.234.68`（本机可直连；跳板形态 `ssh -J root@fleetly-dev.deeploop.net root@fleetly-node2.deeploop.net` 亦可）。VPC eth1=10.124.0.5。**零平台安装物**：只跑 docker daemon + swarm worker。
- DNS：DNSPod 通配 CNAME `*.dev.fleetly.run → fleetly-dev.deeploop.net`（n0.dev 实证解析）。
- 两台 dockerd 均带 drop-in `--insecure-registry 10.124.0.3:5000`（zot 走 HTTP；VPC 内网形态）。
- CLI 凭据在 manager `/root/.config/fleetly/credentials`（`FLEETLY_ADDR=127.0.0.1:9080` + 自动读凭据）。

## 现役资产

- swarm 双节点（manager advertise **10.124.0.3**——eth0 的 10.48.0.x 是跨 VPC 假象地址，advertise/raft 绝不可用）。
- 受管 edge：traefik（80/443，LE **staging** CA；ACME 卷 `fleetly-edge-acme`）。
- **受管仓库：zot**（F1.11，ADR-0019 附录 B）：受管 Workload 形态（fleetly/system/registry 域，发布 5000 routing mesh），镜像 `ghcr.io/project-zot/zot-minimal:v2.1.21` 钉版，数据卷 `fleetly-registry-zot`。平台凭证在 `/var/lib/fleetly/keys/registry.json`（0o600；轮换=删文件+重启 fleetlyd——htpasswd 载体指纹变→受管域滚动替换，三面自愈）。构建推送目标=`10.124.0.3:5000/<app>:r<seq>`，from_build 下发=`10.124.0.3:5000/<app>@sha256:<digest>`（digest 直存，绕开 docker29 tag 坑）。unit 注入 `FLEETLY_REGISTRY_ADDR=10.124.0.3:5000`。旧手工 `n0-zot` 已停（容器保留作回滚资料；F0.18 场景 13 证据在上表）。
- 验收应用：project `n0reg`（私有镜像双节点部署）、`n0probe`（exec 探针）、quickstart `n0demo`（route n0.dev.fleetly.run, tls auto）。

### 受管 zot 接管实录（F1.11 部署时操作序）

1. 让位端口：`docker stop n0-zot`（受管 zot 发布 5000 routing mesh 会绑全部节点 5000，与 host network 的 n0-zot 冲突）。
2. unit 注入 env：`systemctl edit fleetlyd` 加 `Environment=FLEETLY_REGISTRY_ADDR=10.124.0.3:5000` → `systemctl restart fleetlyd`（两台 dockerd 的 `--insecure-registry 10.124.0.3:5000` 已在位，无需动）。
3. 验证：`fleetly doctor`（registry Provider healthy）；`docker service ls` 出 `fleetly-registry-zot`；curl `http://10.124.0.3:5000/v2/` 带 Basic 认证返回 `{}`（密码在 keys/registry.json）。
4. 全链实证：`fleetly deploy`（git 或 --from-dir 源）→ 构建日志出 push 步骤 → node2 `docker images` 出 `<digest>` 拉取产物。

## 2026-09-30 重装实录（顺序敏感处加粗）

1. 归档版退役：备份 `/root/fleetly-archive-20260930.tar.gz`（数据根）+ `-config.yaml` + `-services.txt`（18 服务台账）→ **停容器形态控制面必须 `docker stop fleetlyd`（--restart unless-stopped 会 12s 重拉 pkill 后的进程）** → `docker service rm` 全部（卷保留）→ secrets/configs 清理 → buildkit 漏网容器补清。
2. 数据根让位：`mv /var/lib/fleetly /var/lib/fleetly.archived-20260930` → install.sh（FLEETLY_BIN_DIR + **FLEETLY_ADVERTISE_ADDR=10.124.0.3**）→ unit 注入 Edge env → `fleetly init`（bootstrap 即吊销）→ doctor 零 fail。
3. node2：daemon 已活则直接 join；**join 材料地址必须核验是 10.124.0.3:2377**——若出现 10.48.0.6（eth0）说明 raft 播报错网，修法 = `docker swarm leave --force && docker swarm init --advertise-addr 10.124.0.3`（manager）+ node2 leave/join。
4. **dockerd 旗标坑**：`--advertise-addr` 不是 dockerd 旗标（swarm 子命令专属）；写错触发 systemd "Start request repeated too quickly" 限速——`systemctl reset-failed docker` 后再起。

## 真机验收记录（N0 批尾）

| 验收 | 结果 | 关键证据 |
|---|---|---|
| F0.18 场景 13 私有镜像双节点拉取 | ✅ | Secret `registry:10.124.0.3:5000` → replicas=2 双节点 Running；node2 `docker images` 出现私有镜像；载体 spec 零凭证（inspect grep 密码零命中）。overlay 数据面双副本互通 = **UDP 4789/7946 已放行**（W3-F2 解除） |
| F0.19 RuntimeAdmin | ✅ | drain（task 真迁移：node2 Shutdown/manager 接管）→ cordon(Pause) → uncordon(Active)；审计 node.drain/cordon/uncordon（source=cli）；陈旧平台 ID 被 E_NOT_FOUND 拒（ID 永不复用执法） |
| 探针 | ✅（exec）/ 记档（http/tcp） | exec：compose healthcheck → L1 门真机通过。http/tcp 探针 CLI/API 无声明面（compose 只出 exec）——翻译正确性由单元钉死，声明面随 API 批 |
| F0.15 ACME | ✅ | `issuer=Let's Encrypt CN=(STAGING) Ersatz Emmer YR2`、`CN=n0.dev.fleetly.run`（LE staging + HTTP-01 经受管 traefik；生产 CA 轮换另批） |
| F0.3 quickstart --tls auto | ✅ | TLS 握手 + router + 证书全通；2026-09-30 首验时后端 502（受管 edge 挂项目网当时是空桩）——B1 实装 `activeProjectNetworks` 后 **HTTPS 200 全通**（2026-10-01 双视角复验，见下节） |

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
- **受管 zot 无钉住**（B.5②边界真咬人）：任何 spec 变更的滚动替换都可能把 task 漂到无卷节点 preparing 打转。临时操作序：`docker node update --availability=drain fleetly-node2` → 待 task 落 manager Running → `active` 恢复。**平台侧钉住=挂账**。
- **build 行按 (revision, builder) 内容寻址复用**：改源（哪怕加注释）才铸新 build；排查"修复不生效"先想到旧 build 行重放。
- **swarm secret 载体已 1300+**（zot 风暴遗产）——孤儿清理面挂账；run 服务残留（终态 task 的 0/1 service）清理面挂账。
- torchwood 镜像 sha-78ea1a4 的 `configs/config.yaml` 不会被 ENTRYPOINT 自动加载——addr 类键必须显式 env（v0.1 形态同款教训再现）。

**挂账（F1.15 收口时的诚实边界）**：torchwood dispatcher 客户端仍是 v0.1 vendored 契约（对新 Tasks API 的移植=torchwood 侧独立批；池语义已按 ADR-0012 以平台 API 面真机回归）；mlbridge→torchwood 的 E2E 凭据接线（torchwood 首管引导+scoped key）未走完（ml-tw-projects 现为占位值）；GHCR 私有镜像直投未实证（机制=zot 私拉 F0.18 同款 registry: 凭证，无私有 GHCR 镜像可测）。

## 教训与边界

- **本机（Windows 工作机）出站对该 VPS 全端口受限**（80/443/8420 全 000；node2 路径全通）——外部验证走 node2 或 check-host 类服务，勿信本机 curl。
- 归档版残留：`/var/lib/fleetly.archived-20260930`、`/root/fleetly-archive-*`、`/opt/fleetly`（旧二进制+config）——回滚资料，退役不迁移；确认不再回滚后可清。
- manager 内存 4GB 是硬约束：受管面每实例 ~86MB 级，任何"逐拍滚动替换"类回归都会很快显形。
- nodes 表会保留历史行（swarm 重建前后平台 ID 不同、旧行 available=false）——观测缓存非权威、ID 永不复用，CLI 侧按 available=true 取现行。
- **受管 zot 边界（ADR-0019 附录 B.5）**：数据卷节点本地无钉住（zot 重调度=镜像丢失，重部署触发重建自愈；运行中服务不受影响）；镜像无 GC（只增）；新 worker 加入时 dockerd 必须带同款 `--insecure-registry 10.124.0.3:5000`。
- **ssh 命令里的 `$()`/管道在 Windows 侧会被转义吃掉**——远程复杂操作一律写脚本→scp→sh（本 runbook 2026-10-02 的全部诊断脚本在 manager `/root/dogfooding/`）。

### 端口暴露矩阵（操作者责任）

控制面与受管数据面的端口**全部只允许 VPC/内网可达**（云防火墙/安全组封公网入口；下表是本 runbook 拓扑的核对清单，任何新端口入网前先在此登记）：

| 端口 | 面 | 认证形态 | 暴露要求 |
|---|---|---|---|
| 9080 | fleetlyd gRPC（控制面 API） | Token（authn 拦截链） | 仅 VPC/内网；CLI 经 manager 本机回环或跳板访问 |
| 9081 | REST gateway（SSE/幂等等同源面） | Token | 仅 VPC/内网 |
| 9082 | Edge config 拉取端点（traefik HTTP provider） | **无认证**（traefik HTTP provider 不支持凭证的既知形态） | 仅 VPC/内网，**公网可达 = 任意人可改写全量路由** |
| 5000 | 受管 zot（镜像仓库） | HTTP 明文 + 单一平台凭证（htpasswd） | 仅 VPC/内网；两台 dockerd 的 `--insecure-registry` 同依赖此形态 |

已知边界（记档不遮掩）：

1. **zot 平台凭证全域可读**：任何租户可拉他人镜像——单租户窗口下接受；多租户前必须按租户隔离或经 Edge 前置认证（随 ADR 批，不静默升级）。
2. **9082 无认证**：traefik HTTP provider 无凭证机制的既知形态，绑定面收窄（回环/Unix socket/防火墙白名单）随后续批次；当前防线只有网络位置（VPC 内网）。

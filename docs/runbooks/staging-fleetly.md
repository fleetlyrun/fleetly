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
- zot 私有仓（验收用，非受管）：容器 `n0-zot`（host network :5000，htpasswd 认证，数据 /root/n0-zot-data）。F0.18 场景 13 验收载体，留用。
- 验收应用：project `n0reg`（私有镜像双节点部署）、`n0probe`（exec 探针）、quickstart `n0demo`（route n0.dev.fleetly.run, tls auto）。

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
| F0.3 quickstart --tls auto | ✅（TLS/路由/ACME）/ 边界（后端 502） | TLS 握手 + router + 证书全通；**HTTPS 200 差受管 edge 挂项目网——`activeProjectNetworks` 是 C5 留空桩**，挂网实装随网络批次（N0 后端连通=traefik 未挂项目 overlay） |

## 本批真机逼出的修复（均在仓，随批推送）

1. **受管域 Generation 逐 tick +1 → traefik 繁殖 115 实例打穿 4GB**（managed.go：指纹未变 gen 不推进）——内存雪崩杀死 sshd/网关，主机硬重启恢复。
2. **docker API hang 卡死单写者 managedLoop**（daemon 重启窗口静默卡死直至进程重启；ManagedStepTimeout=30s 带界）。
3. **node label 服务端过滤对点号键恒不命中**（`label=k=v`/`node.labels.k=v` 都不行；admin.go 改全量 NodeList + 内存精确匹配）。
4. quickstart 镜像直投无端口声明 → route 后端永解析不到（改 compose 形态带 ports）。

## 教训与边界

- **本机（Windows 工作机）出站对该 VPS 全端口受限**（80/443/8420 全 000；node2 路径全通）——外部验证走 node2 或 check-host 类服务，勿信本机 curl。
- 归档版残留：`/var/lib/fleetly.archived-20260930`、`/root/fleetly-archive-*`、`/opt/fleetly`（旧二进制+config）——回滚资料，退役不迁移；确认不再回滚后可清。
- manager 内存 4GB 是硬约束：受管面每实例 ~86MB 级，任何"逐拍滚动替换"类回归都会很快显形。
- nodes 表会保留历史行（swarm 重建前后平台 ID 不同、旧行 available=false）——观测缓存非权威、ID 永不复用，CLI 侧按 available=true 取现行。

# deploy — 安装器与引擎门禁（T2.1）

一条命令在干净 Linux VPS（amd64/arm64，root）上装出可运行平台：引擎门禁 →
获取二进制 → 隐式 `docker swarm init` → systemd 自启 → 安装报告。

设计依据（只读）：architecture §2.6（运行时 = 单节点 Swarm，安装时隐式
init）、§4.2（引擎门禁 / 安全默认基线 / 底座端口加固）、delivery-pipeline
§2.4（`curl | sh` 的完整性基线：checksum 必验 + 签名尽力而为）。

## 文件

| 文件 | 作用 |
| --- | --- |
| `install.sh` | 安装器（POSIX sh；systemd unit 经内嵌同源 heredoc 生成） |
| `uninstall.sh` | 卸载器（应用数据默认保留，`--purge` 才删） |
| `fleetlyd.service` | systemd unit 参考模板（与 install.sh 内嵌 heredoc 逐字一致，test-install.sh 做 diff 防漂移） |
| `test-install.sh` | dind 内验收套件（A1-A9，51 断言） |
| `run-dind-test.sh` | 宿主编排：交叉编译 → 起 dind → exec+stdin 注入 → 跑套件 → 清理 |

## 一条命令安装

```sh
# 最新 stable（缺省形态；release 制品链落地前 GitHub releases 尚无产物）
curl -fsSL https://fleetly.dev/install.sh | sudo sh -

# 钉定版本
curl -fsSL https://fleetly.dev/install.sh | sudo sh - --version v0.1.0

# 离线 / 开发形态：直接使用本地已备好的二进制（跳过下载，仍打版本信息）
sudo sh install.sh --bin-dir ./dist
```

### 获取形态与完整性

| 形态 | 参数 | 完整性 |
| --- | --- | --- |
| latest stable | （无） | checksums.txt sha256 必验；cosign 签名尽力而为 |
| 版本钉定 | `--version vX.Y.Z` | 同上 |
| 离线/开发 | `--bin-dir <dir>` | 本地直取（自备完整性） |

下载形态的 release 契约（T2.24 制品链需满足）：release 附
`fleetly_<tag>_linux_<arch>.tar.gz`（tar 包根下有 `fleetlyd`、`fleetly`）、
`checksums.txt`（`<sha256>  <文件名>` 行式）、`checksums.txt.sig`（cosign
对 checksums.txt 的签名，按 GitHub OIDC keyless 校验：
`--certificate-oidc-issuer https://token.actions.githubusercontent.com`、
`--certificate-identity-regexp ^https://github.com/fleetlyrun/fleetly/`）。
cosign 缺失时安装器**显式警告降级**（不静默）；cosign 在而验证失败即中止；
`--skip-signature-verify` 仅调试用。

## 引擎门禁（不满足即拒绝，exit 1）

| 检查 | 判定 | 依据 |
| --- | --- | --- |
| 平台 | Linux + x86_64/aarch64；root | 安装器目标形态 |
| Docker Engine | `docker version --format '{{.Server.Version}}'` ≥ **29.8.1** | architecture §4.2（Docker 29.x 破坏史，下限是硬门禁） |
| iptables 后端 | 默认 `iptables` 为 legacy，**或** `iptables-legacy` 可用；nftables-only 拒绝并给修复提示 | architecture §4.2（nftables 暂不支持 Swarm 节点） |
| 基础工具 | docker、curl/wget、tar、sha256sum | 下载与健康等待 |

## 安装动作

1. **目录约定**：`/opt/fleetly/bin`（二进制 0755）、`/opt/fleetly/etc`
   （config.yaml + unit 参考副本）、`/var/lib/fleetly`（数据根 0700——
   SQLite/主密钥/git bare 仓库/构建缓存/日志/证书全落这里）；
   `/usr/local/bin/fleetly{,d}` 符号链接入 PATH。
2. **配置生成**（已存在则保留不覆盖）：HTTP 面 `0.0.0.0:8420`（可
   `--http-addr` 覆盖）、gRPC 只绑 `127.0.0.1:8421`、git SSH `0.0.0.0:8424`
   （对外提供 git push；关闭改 `127.0.0.1:8424`——config-example.yaml 注释
   口径）、数据路径全部显式指向 `/var/lib/fleetly`；其余键回落
   `internal/*` 各 `Config.Normalize` 的平台默认。
3. **隐式 swarm init**（已初始化则跳过）：advertise-addr 优先私网 IP
   （RFC1918；排除 loopback/link-local 与 docker*/br-\*/\*gwbridge\* 网桥
   接口地址），无私网才取公网 IP 并在报告**警示暴露面**（§4.2 底座端口加固；
   `--harden-firewall` 为后续版本能力占位，本阶段不改写 iptables）。
4. **systemd**：`After=docker.service` + `Restart=on-failure` +
   `WorkingDirectory=/var/lib/fleetly` + 适度加固（`ProtectSystem=strict` +
   `ReadWritePaths` 放行数据根、`PrivateTmp`、`NoNewPrivileges` 等，逐项
   注释见模板）。enable + start + `/healthz/liveness` 轮询（60s，失败输出
   systemctl status/journalctl 诊断）。无 systemd 环境 → `--no-systemd`
   明示跳过并打印手动启动命令（不出 unit、不启服）。

## 端口暴露面（安装报告逐项输出）

| 端口 | 用途 | 缺省绑定 | 暴露判定 |
| --- | --- | --- | --- |
| 8420/tcp | HTTP REST/gateway + healthz + Console | `0.0.0.0:8420` | PUBLIC（配合防火墙/反代） |
| 8421/tcp | gRPC（CLI/SDK 直连） | `127.0.0.1:8421` | loopback only |
| 8422/tcp | Traefik HTTP provider 配置端点 | `0.0.0.0:8422`（平台默认） | bearer token 强制 |
| 8424/tcp | git push(SSH) | `0.0.0.0:8424` | PUBLIC（安装报告明示；不需要时改回 127.0.0.1） |
| 80,443/tcp | Traefik 入口（host 模式） | 宿主端口 | PUBLIC（应用入口，预期） |
| 2377/tcp, 7946/tcp+udp, 4789/udp | Swarm 成员/VXLAN | advertise-addr | 私网 → LAN only；公网 → 报告红色警示 |

## 首启 bootstrap token

控制面首启（库内无任何 token 时）生成 bootstrap admin token 并**只打印
一次**：systemd 形态
`journalctl -u fleetlyd.service | grep "bootstrap admin token"`；手动形态
在 daemon stdout/日志文件里 grep 同关键字。随后
`FLEETLY_ADDR=127.0.0.1:8421 FLEETLY_TOKEN=<token> fleetly apps list` 验通。

## 卸载

```sh
sudo sh uninstall.sh           # 停服 + 删 unit/二进制/符号链接/配置
                               # /var/lib/fleetly 保留并明示
sudo sh uninstall.sh --purge   # 连应用数据一起删（不可逆）
```

swarm 底座不默认拆除——提示 `docker swarm leave --force` 由操作者显式执行
（后果：单节点 Swarm 拆掉、运行中的应用服务失去调度）。

## dind 验收（本阶段核心验收，主会话可复跑）

Git Bash（Windows）或 Linux shell 上一条命令：

```sh
sh deploy/run-dind-test.sh
```

编排内容：交叉编译 linux/amd64（`CGO_ENABLED=0`，可 `TI_SKIP_BUILD=1 +
TI_BIN_DIR=<dir>` 复用现成二进制）→ 特权 `docker:29.8.1-dind` 容器 →
exec+stdin 注入脚本与二进制（**禁用 docker cp**——Engine 29.x 宿主向特权
dind 会 exit 0 但文件不落盘，e2e/README.md 已知问题；二进制不 sed 剥 CR，
否则 ELF 被破坏会 segfault）→ 容器内跑 `test-install.sh` → 失败 dump 日志
→ 清理。

`test-install.sh` 断言组（详细清单见脚本头注释）：A1 `sh -n` 语法；
A2 `--bin-dir --no-systemd` 正路径安装（目录/二进制/配置/unit 同源/
符号链接/报告字段）；A3 隐式 swarm init（active + advertise 私网）；
A4 daemon 手动启动 liveness 200 + 8424/8421/8420 监听面 + SIGTERM 退出
码 0；A5 bootstrap token 从日志抓取 → CLI `apps list`；A6 重装幂等
（swarm 跳过 init、config 保留）；A7 假 docker（28.3.2）版本门禁拒绝；
A8 nftables-only shim iptables 门禁拒绝；A9 卸载（数据保留/明示 +
`--purge`）。

amd64 运行验证即覆盖门禁主路径；arm64 交叉编译产物存在性由构建证明
（release smoke 属 T2.24）。

## 已知边界（后续阶段）

- 二进制签名验证的 release 制品链（cosign keyless）随 T2.24 落地；
  本阶段按上方契约预留探测与降级路径。
- `--harden-firewall`（自动 iptables 放行规则）为 architecture §4.2
  预留，本阶段只提示不写规则。
- unit 以 root 运行（需要 docker.sock；专用用户 + docker 组收敛属后续
  加固项）。
- Engine 升级回归矩阵（V7）与自升级（T2.23）不在本阶段。

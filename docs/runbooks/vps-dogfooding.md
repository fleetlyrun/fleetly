# VPS dogfooding runbook（T1-V2.1~T1-V2.3 实录，2026-09-20）

| 状态 | 日期 | 关联 |
|---|---|---|
| 进行中（核心链路全通；V5 演练与 Playwright 待做） | 2026-09-20 | [v0.2 规划 W1](../plan/2026-09-20-v0.2-plan.md)；[交付流水线 §2.6](../design/2026-09-17-delivery-pipeline.md)；基线产物 v0.1.0-rc1（公开 release） |

staging：`root@fleetly-dev.deeploop.net`（146.190.58.0，Debian 13 / 2C / 4G / 79G）；域名 `dev.fleetly.run` + 通配符 `*.dev.fleetly.run`（DNSPod，CNAME 到主机名）。

## 1. 环境准备（顺序敏感！）

```bash
# ① Docker 引擎（Debian 13）
curl -fsSL https://get.docker.com | sh
# ② 切 iptables legacy —— 必须在 dockerd 首启前或切换后重启 docker（见 §4-F3）
update-alternatives --set iptables /usr/sbin/iptables-legacy
systemctl restart docker          # 若 ① 时 daemon 已起
# ③ 环境体检（含 nft 双栈残留检测）
sh verify-vps.sh dev.fleetly.run
```

## 2. 安装与配置（公开 release 全真路径）

```bash
curl -fsSL -o /root/install.sh \
  https://github.com/fleetlyrun/fleetly/releases/download/v0.1.0-rc1/install.sh
sh /root/install.sh --version v0.1.0-rc1
```

安装报告全绿（引擎门禁/checksum/swarm init 私网 advertise/systemd/healthz）；bootstrap token 在 journalctl（rc1 形态，见 F1）。

配置增补（`/opt/fleetly/etc/config.yaml`）：`console.static_dir=/opt/fleetly/console`（main 构建 dist 经 scp 上传）；`acme.email`（`ca_dir_url` 缺省即 LE 生产目录，零 Pebble 痕迹）。

**平台镜像预拉**（v0.1 不代拉；W0 预拉台账的 digest 形态须补 tag——见 F4）：

```bash
docker pull alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc
docker pull traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2
docker tag alpine@sha256:d9e85… alpine:3.20        # 运行时 spec 按 tag 引用
docker tag traefik@sha256:16acb… traefik:v3.5
docker pull traefik/whoami:v1.10                    # demo 应用镜像
```

## 3. 已验证链路（全部真路径）

| 链路 | 结果 |
|---|---|
| 公开 release 安装（下载+checksum+门禁+systemd） | ✅（签名轨见 F1） |
| REST /v1（外网 Bearer）+ /ui/（console 托管） | ✅ http://146.190.58.0:8420 |
| CLI deploy（hello-web，域名变更 ×4 次部署） | ✅ running/succeeded/revision 链 |
| **git push 部署**（ssh://git@host:8424/<app>.git，TOFU+指纹审计+push 即 queue） | ✅ ×2（初推+空提交重触发） |
| rollback（快照重放，--to 指定 revision） | ✅（服务端异步完成，见 F6） |
| **LE 生产证书**（HTTP-01 经 Traefik 反代→集中签发→台账） | ✅ hello.dev + demo.dev 双证，issuer=Let's Encrypt CN=YE2，有效期至 2026-12-19 |
| HTTPS 服务 | ✅ 双域名 200（whoami 实答） |
| 平台热备 | ✅ verified snapshot 自动落账（daily） |

Console 入口：`http://dev.fleetly.run:8420/ui/`（8420 为明文 HTTP——平台自身 TLS 属 E1 平台子域范畴）。

## 4. 发现与挂账（F 编号，v0.1.x/v0.2 候选）

| # | 发现 | 处置建议 |
|---|---|---|
| F1 | **rc1 版本时间差**（tag 于 S13-S20 落地前）：安装器只有 cosign 轨（无 cosign 即 degraded skip，S14 openssl 双轨在 v0.1.0）；bootstrap token 仅 journal（B5 文件落盘在后）；空视图拒绝下发（S13 noop 兜底在后，首个应用部署前 sweep WARN） | v0.1.0 自然消除；无需代码动作 |
| F2 | **收敛/证书重试与续期扫描共用 12h 周期**：首装缺镜像/ACME 一次失败后，自然重试窗口过长（实测靠 redeploy 人为触发才签成） | v0.1.x 候选：收敛失败与 cert 失败用短退避（如 1min 起指数），续期扫描保持 12h |
| F3 | **iptables 后端切换时机**：daemon 以 nft 首启后再切 legacy，残留 nft 表挂内核钩子——宿主端口通、**DNAT 发布端口（80/443）全死**，云防火墙排查方向会被带偏（实测） | verify-vps.sh 已加检测；install.sh 可在 A8 门禁中加同款提示（v0.1.x 候选） |
| F4 | **digest 形态 pull 不落 tag**：运行时 spec 按 tag 引用镜像，digest pull 后须显式 `docker tag` | 预拉台账（image-prepull.md）已隐含；runbook 本节显式化 |
| F5 | CLI flag 顺序纪律（FZ-11）在真实使用中即踩（flag 必须先于位置参数） | 既有纪律，交互提示可随 v0.2 CLI 打磨 |
| F6 | CLI 等待期被杀（Ctrl-C/会话断开）服务端照常完成——异步语义正确，但操作者感知与实际状态可能不一致 | 观察项；Console/事件流已可核对状态 |

## 5. 剩余步骤（W1 未完）

- T1-V2.4 V5 真路径演练（单 manager 故障 + `--force-new-cluster` 恢复；演练前 fresh pre 备份）
- T1-V2.6 Console Playwright smoke（目标：真 staging 的 /ui/）
- T1-V2.5 发布检查单回填 + v0.1.0 正式 tag（裁决 V2-4：dogfooding 验收后）

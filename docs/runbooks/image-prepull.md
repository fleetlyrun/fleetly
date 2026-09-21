# Runbook：平台镜像预拉与 digest 台账（T0-V2.3）

适用：fleetly v0.2 W0（2026-09-20）。背景：zane-ops 的 CI 用维护者个人
fork 镜像 + `canary` 可变 tag——供应链反面教材（docs/research/
2026-09-20-zane-ops-comparison.md R7/A6）；本仓 S20-F1 已把 GitHub Actions
钉 commit SHA，本票把同一纪律扩展到**容器镜像引用**并补 arm64 冒烟
（release.yml `smoke-arm64` job）。

## 0. 纪律与形态

- 引用形态：`image:tag@sha256:<64hex>`——tag 保留作可读性，**digest 为准**。
- digest 取**多架构 index**（manifest list）摘要：amd64 / arm64 通吃，
  与 release 的 arm64 冒烟口径一致。
- 纪律范围：`deploy/**` 与 `.github/workflows/**` 中平台运行时/测试夹具
  引用的容器镜像（下表全集）。**不得**引用无 digest 的平台镜像。
- 门禁：`deploy/check-image-pins.sh`（已接入 pr.yml `deploy-scripts` job，
  无 digest 引用即红）。豁免走 `deploy/image-pin-allowlist.txt`（§5）。

## 1. 平台镜像台账（全集，2026-09-20 解析）

| # | 镜像（钉定形态） | digest（sha256 前缀） | 用途 | 引用位置 |
| --- | --- | --- | --- | --- |
| 1 | `docker:29.8.1-dind` | `3f3c01aa…283f0` | dind 测试底座（与引擎门禁下限一致） | pr.yml e2e；nightly.yml 顶层 env + V7 矩阵；deploy/run-dind-test.sh、run-calibration.sh、run-journey-test.sh、run-upgrade-test.sh（DIND_IMAGE 默认值）；deploy/cal-inner.sh、test-journey.sh（DIND_TAG 默认值） |
| 2 | `docker:29.7.2-dind` | `3ef33f2e…74cb6` | V7 引擎矩阵上一受支持 minor | nightly.yml engine-matrix |
| 3 | `alpine:3.20` | `d9e853e8…4b6bc` | 辅助镜像（预拉暖机 / fixture sidecar / cert 卷检查容器） | deploy/cal-inner.sh；test-journey.sh（预拉 + sidecar spec `image:` + cert 卷检查）；test-upgrade.sh |
| 4 | `alpine:3.22` | `5291449c…5e8fa8` | fleetlyd 容器运行层 | deploy/Dockerfile.fleetlyd |
| 5 | `golang:1.26-alpine` | `51a7c389…f59f1ae` | fleetlyd 容器构建层 | deploy/Dockerfile.fleetlyd |
| 6 | `traefik:v3.5` | `16acb89c…9bf1d2` | ingress 暖机（cert seed 与 Traefik 服务不自动拉镜像——T2.15 已知边界） | deploy/cal-inner.sh；test-journey.sh；test-upgrade.sh |
| 7 | `moby/buildkit:v0.32.2` | `28a89871…bb41d8` | fleetly-buildkit 构建器 warm 路径依赖 | deploy/cal-inner.sh |
| 8 | `ghcr.io/letsencrypt/pebble:latest` | `ddf23064…78199` | ACME 测试 CA（journey 链路代演） | deploy/test-journey.sh（`PEBBLE_IMG`） |
| 9 | `curlimages/curl:latest` | `58adaa4e…166777` | HTTPS 探针（journey J4） | deploy/test-journey.sh |
| 10 | `ghcr.io/project-zot/zot:v2.1.21` | `6b69512c…f48c8` | 平台 registry（zot，E1-4 部署器钉版缺省；多节点 manager 平台组件） | internal/ingress/registry.go `DefaultZotImage`（Go 常量字面，不在 `deploy/**`/`.github/**` 扫描口径内——钉版形态由本行与本常量双锚，改动须同步） |
| 11 | `restic/restic:0.19.1` | `136600b6…d510` | 状态备份远端上传轨（restic 钉版容器一次性执行，E3-3/D-S3-3；首次上传按需拉取，预拉可选） | internal/statebackup/restic.go `DefaultResticImage`（Go 常量字面，不在 `deploy/**`/`.github/**` 扫描口径内——钉版形态由本行与本常量双锚，改动须同步；2026-09-21 解析） |

台账与实际引用集的一致性以门禁扫描为准：

```sh
sh deploy/check-image-pins.sh -l    # 列出全部识别到的引用与钉定状态
```

（2026-09-20 实跑：23 处引用全部钉定，对应上表 9 个镜像；#10 zot 为
E1-4 起的 Go 常量钉版引用，不在门禁扫描口径内，改动须同步本表——
见该行引用位置注记。）

## 2. digest 解析与独立复验

解析（取输出首部 `Digest:` 行 = 多架构 index 摘要）：

```sh
docker buildx imagetools inspect traefik:v3.5
# Name:      docker.io/library/traefik:v3.5
# MediaType: application/vnd.oci.image.index.v1+json
# Digest:    sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2
```

独立复验（钉定形态可被解析且 digest 与台账一致；两命令任一即可）：

```sh
docker buildx imagetools inspect traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2
docker manifest inspect traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2
```

解析不到 digest（网络/仓库原因）的镜像**保持 tag 引用**并列遗留，
不得编造 digest——本票无此情况（9/9 解析成功）。

## 3. 预拉命令

### 3.1 在线主机（干净 VPS 跑平台测试前）

```sh
# 全集预拉（与台账一致；平台脚本 run-* 已内置各自所需子集的预热）
for ref in \
  docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0 \
  docker:29.7.2-dind@sha256:3ef33f2e220b79ed3ef3b99d81746f06f306cd6340e2cb7331d17ae996e74cb6 \
  alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc \
  alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 \
  golang:1.26-alpine@sha256:51a7c389a5ddaf82f527191a1e9bff9928655130a44e4975dd1d7e0acf59f1ae \
  traefik:v3.5@sha256:16acb89c6db341182970d6fdafece31303b0a380a8ed7aa51682e225229bf1d2 \
  moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8 \
  ghcr.io/project-zot/zot:v2.1.21@sha256:6b69512c00dceaad05b1144e6079aac6aa7309d7fd200f9947ecb1de09cf48c8 \
  ghcr.io/letsencrypt/pebble:latest@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199 \
  curlimages/curl:latest@sha256:58adaa4e8dca9c988bae2aba4ab3434a0bb2da16bbe3f92dec39ec7785166777
do docker pull -q "$ref" || exit 1; done
```

### 3.2 离线环境（air-gapped VPS / dind）

```sh
# 在线机器导出（digest 钉定形态 save/load 后 digest 关系保持）
docker pull -q traefik:v3.5@sha256:16acb89c…9bf1d2
docker save traefik:v3.5@sha256:16acb89c…9bf1d2 | gzip > traefik-v3.5.tar.gz
# 离线机器导入
docker load < traefik-v3.5.tar.gz
```

dind 内预热同理：`docker exec <dind> docker pull -q <ref>`（nightly
run.sh 与 run-calibration.sh 已内置并行暖机，形态与 §3.1 一致）。

## 4. 换版流程（升级基镜像 / dind 引擎）

1. 选定新 tag，用 §2 命令解析其 digest；**禁止只换 tag 不换 digest**。
2. 全部引用同步（全局搜旧 tag：`grep -rn "<old-tag>" deploy/ .github/workflows/`）；
   dind 引擎换版须 V7 矩阵两腿（29.8.1 / 29.7.2）一起裁决。
3. `sh deploy/check-image-pins.sh` 必须绿；负路径抽检一条新引用未钉形态应红。
4. 跑受影响套件（deploy/run-dind-test.sh 起）后更新 §1 台账（digest 与日期）。

## 5. 门禁与豁免

```sh
sh deploy/check-image-pins.sh              # 扫描既定范围，无 digest 引用即非零退出
sh deploy/check-image-pins.sh -l           # 列表模式（台账一致性核对）
sh deploy/check-image-pins.sh FILE...      # 只扫指定文件（负路径自证用）
```

- 扫描口径与已知盲区（间接拼装引用、printf 动态 Dockerfile 等）见脚本
  头注释；识别不到 ≠ 允许——新增镜像引用优先用字面 `name:tag@sha256:…`。
- 负路径自证（2026-09-20 本机实跑）：构造含 `FROM alpine:3.22` 与
  `docker pull traefik:v3.5` 的临时文件 → 退出 1 并逐条标注 file:line；
  豁免清单（`-a` 换临时清单）命中一条后仍对未豁免引用退出 1；全部豁免
  则退出 0。
- 豁免清单：`deploy/image-pin-allowlist.txt`，每行固定子串命中
  `<路径>:<引用>` 即豁免，**必须同行注释理由**（如故意验证「可变 tag
  被拒」的负路径用例）。当前条目：无。

## 6. 范围外与遗留

- `e2e/nightly/*.sh`（infra-b.sh、resource-sample.sh、run.sh、
  conformance-builder.sh、n-stamp.sh）仍以 tag 形态引用 alpine:3.20 /
  traefik:v3.5 / `DIND_IMAGE` 默认 `docker:29.8.1-dind`——本票禁改
  `e2e/**`，未钉；nightly workflow 传入的 `DIND_IMAGE` 已钉 digest。
  列入 v0.2 后续票据收口。
- `pebble:latest` / `curlimages/curl:latest` 浮动 tag 的漂移风险已被
  digest 钉定消除，但升级仍须按 §4 主动换版（digest 不会自更新）。
- `smoke-arm64`（release.yml）无容器镜像依赖（原生 go build + 单测），
  与本台账无交集；其 runner 口径（ubuntu-24.04-arm，公共仓库免费）见
  job 注释。

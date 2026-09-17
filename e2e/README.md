# e2e — dind E2E 骨架（T0.4 / 交付流水线 M0）

在 `docker:29.8.1-dind` 容器内拉起 edgefleetd 并验证最小生命周期：启动 →
liveness 就绪 → ping 契约断言 → SIGTERM 优雅关闭。这是交付流水线 PR 轨道
第 5 项「集成 E2E（单节点 dind）」的骨架底座，后续随 Spike/T2 在此扩展
fixture 部署链（install → 部署 fixture → 对账 → health gate → 路由 → 回滚 →
自升级）。

设计依据（只读）：

- `docs/design/2026-09-17-delivery-pipeline.md` §2.2 第 5 项、P2（E2E 宿主
  用 dind）、§4 M0
- `docs/plan/2026-09-17-task-breakdown.md` T0.4

## 文件

| 文件 | 作用 |
| --- | --- |
| `smoke.sh` | 冒烟脚本（POSIX sh，容器内执行；CI 与本地手动同一入口） |
| 本目录之外：`.github/workflows/pr.yml` 的 `e2e` job | CI 编排（交叉编译 → 起 dind → 容器内跑 smoke → 清理） |

## smoke.sh 断言清单

1. 前置：`EDGEFLEETD_BIN` 存在且可执行
2. 后台启动（`--addr "$HTTP_ADDR"`），日志落 `SMOKE_LOG`
3. `/healthz/liveness` 在 `SMOKE_TIMEOUT_S` 秒内返回 200（轮询，含进程
   提前退出检测）
4. `GET /v1/system/ping` 应答 JSON 的 `service == "edgefleetd"`（sed 提取，
   容忍 protojson 的不定空白；**不依赖 jq**）
5. `SIGTERM` 后进程退出码为 0（lynx Runner 托管的优雅关停契约）

任一断言失败输出 FAIL、脚本退出码非零；全部通过输出 PASS 汇总、退出 0。

HTTP 客户端取 `curl` 或 `wget` 之一（`docker:29.8.1-dind` 基于 Alpine，
自带 busybox wget，无需 apk add）。脚本内不出现任何 Docker 引擎版本号
（引擎版本门禁由 workflow 的镜像 tag 固定：`docker:29.8.1-dind`）。

## 参数（环境变量）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `EDGEFLEETD_BIN` | （无，必填） | edgefleetd 二进制路径 |
| `HTTP_ADDR` | `127.0.0.1:8420` | HTTP 面监听地址（传给 `--addr`） |
| `SMOKE_TIMEOUT_S` | `15` | liveness 就绪 deadline（秒） |
| `SMOKE_LOG` | `${TMPDIR:-/tmp}/edgefleetd-smoke.log` | 二进制日志落盘路径 |

注意：gRPC 面监听 `127.0.0.1:8421` 只能经 config（`grpc.addr`）改，无
flag；同一网络命名空间内**并发跑两份 smoke 会撞 gRPC 端口**。

## CI 路径（`.github/workflows/pr.yml` 的 `e2e` job）

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/edgefleetd ./cmd/edgefleetd
docker run -d --name edgefleet-e2e-dind --privileged docker:29.8.1-dind
# 轮询 docker exec edgefleet-e2e-dind docker info（60s deadline，验证 P2 的
# 特权 dind 可用性；smoke 本身不依赖 dockerd）
docker exec -i edgefleet-e2e-dind sh -c 'cat > /tmp/smoke.sh'   < e2e/smoke.sh
docker exec -i edgefleet-e2e-dind sh -c 'cat > /tmp/edgefleetd' < dist/edgefleetd
docker exec edgefleet-e2e-dind sh -c 'chmod +x /tmp/edgefleetd \
  && EDGEFLEETD_BIN=/tmp/edgefleetd sh /tmp/smoke.sh'
docker rm -f edgefleet-e2e-dind   # if: always()；失败时先 dump 容器内外日志
```

二进制是 `CGO_ENABLED=0` 的静态可迁文件，exec+stdin 流式写入 Alpine 容器
即可直接执行，无需装任何运行时。

> **已知问题（docker cp 静默丢文件）**：Engine 29.x 宿主向特权 `docker:*-dind`
> 容器 `docker cp` 会 **exit 0 但文件不落盘**——CI 首跑（Linux runner）与
> Windows Docker Desktop 29.7.2 双复现（2026-09-17，run 35234788587）。故
> CI 与本地一律走 exec+stdin；此现象属引擎门禁知识库素材（V7 回归矩阵可
> 考虑加 docker cp 探针）。

## 本地复跑（Windows Docker Desktop）

前置：Docker Desktop 运行中（本机 Engine 29.7.2 低于 CI 门禁 29.8.1，跑
冒烟没问题；dind **镜像**固定 `docker:29.8.1-dind`，与 CI 一致）；仓库根
目录交叉编译。

```bat
:: 1) 交叉编译（产物放仓库外，避免污染工作区）
mkdir "%TEMP%\edgefleet-e2e"
set GOOS=linux&& set GOARCH=amd64&& set CGO_ENABLED=0&& go build -o "%TEMP%\edgefleet-e2e\edgefleetd" ./cmd/edgefleetd

:: 2) 起 dind 容器（与 CI 同款：特权 + 默认 dockerd 入口）
docker rm -f edgefleet-e2e-dind
docker run -d --name edgefleet-e2e-dind --privileged docker:29.8.1-dind

:: 3) 等 dind 内 dockerd 就绪（可选，CI 有同款 60s 门）
docker exec edgefleet-e2e-dind docker version --format "inner engine: {{.Server.Version}}"

:: 4) 送入二进制与脚本（exec+stdin 直传，字节保真；docker cp 在 Engine
::    29.x 宿主 → 特权 dind 上静默丢文件，见上方已知问题，勿改回 cp）
docker exec -i edgefleet-e2e-dind sh -c "cat > /tmp/edgefleetd" < "%TEMP%\edgefleet-e2e\edgefleetd"
docker exec -i edgefleet-e2e-dind sh -c "cat > /tmp/smoke.sh"     < "%TEMP%\edgefleet-e2e\smoke.sh"

:: 4') 校验完整性（两侧 sha256 应一致）
certutil -hashfile "%TEMP%\edgefleet-e2e\edgefleetd" SHA256
docker exec edgefleet-e2e-dind sha256sum /tmp/edgefleetd /tmp/smoke.sh

:: 5) 跑冒烟（与 CI 同一入口）
docker exec edgefleet-e2e-dind sh -c "chmod +x /tmp/edgefleetd /tmp/smoke.sh && EDGEFLEETD_BIN=/tmp/edgefleetd sh /tmp/smoke.sh"

:: 6) 清理（每次复跑建议重建容器，避免残留进程干扰断言）
docker rm -f edgefleet-e2e-dind
```

Linux/macOS 宿主：`go build` 产出后同样走 exec+stdin 送入（与 CI 完全一致
的命令路径，见上方 CI 段）。

## 方案取舍记录（为什么没有 Dockerfile.smoke）

候选两案（交付物冻结时裁决）：

- **已选：裸 dind 容器 + `docker exec`**（推荐案）。二进制与脚本经
  exec+stdin 流式送入官方 `docker:29.8.1-dind` 容器直接执行。改动面最小、
  无镜像构建环节（PR 轨道时长预算友好）、smoke 与本地手动完全同一条命令。
- 备选（未选）：把 edgefleetd 打进基于 alpine 的镜像（即原
  `Dockerfile.smoke` 形态），在 dind 内 `docker run`。多一次镜像构建/传输，
  且 dind 内 build 需要先把构建产物送进 dind 的存储，链路更长。**何时切
  过去**：当 §2.2 第 5 项推进到「平台以容器形态被 edgefleet 自己安装/管理
  在 dind 内」（install → 部署链）时，本目录再补一个把平台二进制打成镜像
  的 Dockerfile，服务于「被管平台」路径；冒烟断言脚本（smoke.sh）不变。

## 已知限制

- **端口盲区**：liveness/ping 是对 `HTTP_ADDR` 的黑盒断言，若同命名空间
  内有**残留的同款进程**占着端口，前两项断言可能被它满足——兜底是断言 5
  （对脚本自己拉起的进程做 SIGTERM 退出码校验，残留场景必然失败）。CI 每
  次 job 用全新容器，无此风险；本地复跑请按上面第 6 步重建容器。
- **SIGTERM 时序**：二进制冷启动（34 MB 静态文件首次 page-in）可能需要
  数百毫秒；deadline 内若 SIGTERM 落在 lynx 信号注册之前，进程会以 143
  （未捕获信号）退出并被断言 5 判 FAIL——这是真实缺陷信号（启动过慢或
  阻塞），不是脚本误报。
- 引擎版本：CI 门禁 = dind 镜像 `docker:29.8.1-dind`；本地 Docker Desktop
  引擎版本允许低于门禁（本阶段本地实测 29.7.2 可完整复跑）。

## 后续扩展点

- **fixture 部署链**（§2.2 第 5 项终态）：smoke 通过后，在同一 dind 容器
  内执行 install → 部署 fixture 应用（compose）→ stack 对账（增/删服务、
  受管字段拒绝）→ health gate → 路由 → 回滚 → 平台自升级；逐段以独立脚本
  落在 `e2e/`，由 workflow 的 e2e job 串联。
- **Spike B/C 底座**（task-breakdown T1.2/T1.3，建议接 T0.4 加速）：发布
  语义全链路验证、失败矩阵断言脚本、Swarm 底座行为验证均复用「dind 容器
  + exec」宿主形态。
- **nightly 复用**（交付 §6）：引擎矩阵（dind 29.8.1 × 存储驱动 × 上一
  minor）在本骨架上展开为矩阵 job；升级 E2E（v_n → v_{n+1}）沿用同一
  宿主脚本族。

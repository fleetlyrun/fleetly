// Package build 是构建管线（task-breakdown T2.8 构建管线产品化 + T2.9
// 镜像身份 v0.1 免 registry）：compose 服务的 build/image 两模式到本机
// 镜像 digest 的生产链——Builder 端口 + Railpack/Dockerfile 双实现 +
// 构建队列（并发 ≤2、资源限额）+ plan/日志归档 + 本机镜像登记与
// preflight（供发布引擎消费）。
//
// ── buildkit 接入选型（架构 §2.2 构建行「Railpack + BuildKit」，D6）──
//
// v0.1 单机形态 = 平台自管的 buildkitd 容器（moby/buildkit 钉版镜像，
// privileged + cgroup 内存/CPU 硬限额 + 缓存持久化卷），经
// moby/buildkit/client 以 `docker-container://<name>` connhelper 连接
// （connhelper 经 docker CLI `exec -i <ctr> buildctl dial-stdio` 建流，
// docker CLI 在 PATH 是 fleetly 目标用户「会 Docker」的既定前提）。
// 理由（2026-09-17 实证）：
//  1. dockerd 内嵌 buildkit 的直连形态在本机被探针否定：Windows named
//     pipe 上 buildkit gRPC 的 h2c preface 被 HTTP/1.1 应答拒绝
//     （dockerd 未在主 socket 暴露 Control 服务）；
//  2. Railpack 产物是 LLB，只能经 buildkit Solve 执行——moby /build
//     路由无 LLB 通道，且 secrets session 管线只有 buildkit client 成形；
//  3. Spike A 的全部证据（本地层缓存 85s→4s、secrets-hash 失效、
//     1GiB/1.5CPU 限额下构建可完成）都在该形态取得。
//
// 端点可配（config build.buildkit_host）：指向外部 buildkitd（隔离升级，
// Spike A 独立 buildkitd 容器形态）或未来 dockerd 原生端点都是纯配置
// 变更，不动代码。
//
// ── Spike A 硬约束落点 ──
//   - provenance/sbom 必须关闭（#9：attestation manifest list 与 swarm
//     本地 digest 引用不兼容，任务崩溃循环）：本包导出走 docker 导出器 +
//     客户端侧管道（docker-format tar 结构上不可能携带 manifest list），
//     且 frontend attrs 显式不请求任何 attest:*（见 buildopt_test.go 断言）；
//   - 缓存是默认能力不是优化（双持久化：buildkitd 内部层缓存命名卷 +
//     宿主侧 local cache 目录，路径可配；缓存键含 secrets-hash，凭证变化
//     正确失效——E2a 形态。客户端侧机制勘误见 config.go 头注释）；
//   - 构建在 cgroup 限额内（默认 1GiB/1.5CPU，Spike A E4 实证值）；
//   - 镜像不自动清理（T2.9 策略：release-semantics §2.4「不自动清理镜像」，
//     见 imagestore.go）。
//
// 核心无第三方概念纪律（架构 §2.8）：moby/buildkit、railpack 类型只存在
// 于本包适配层（solve.go / railpack.go），出口是本包核心类型。
package build

# Task 双形态（one-shot / resident）与属主租约

2026-09-30 用户裁定：torchwood/messageloop 可配合 fleetly 更新（旧 gRPC 契约可重构，不追求二进制兼容），但 fleetly 的基础能力必须满足其需求。决定：Task 建模为双形态程序化工作负载——`one-shot`（创建即一次执行）与 `resident`（维持期望并发数的常驻实例池）；Run 状态机 `pending → running → stopping → stopped | failed`，终态携带停止原因枚举（completed 自然退出 / failed / stopped_by_user / ttl_expired / lease_expired / owner_revoked / platform_drained）；resident Run 由属主 Token 的心跳租约（Owner Lease）保温，失联超宽限即排空回收；平台契约提供 per-Task 稳定 DNS（池级轮询活 Run）与 per-Run 稳定 DNS；TTL 上限 86400s，janitor 兜底收口平台自建残留。

## dogfooding 能力清单（N1 验收标准）

torchwood/messageloop 所需基础能力，fleetly 必须全部满足：镜像直部署（含 GHCR 私有）、build-from-upload、Task TTL + Owner Lease 续期、双级稳定 DNS、Task 网络组互通 + App Process 跨挂 + 跨 Project 互通、停止原因分类（自然退出/失败/回收可区分）、h2c 路由、Postgres（含 percona/pgvector 发行版）与 Redis 托管、程序化 API（幂等键 + 事件流 + Wait 原语）。

## Consequences

- 续期 API（RenewTask）与属主吊销处置（默认宽限排空，ADR-0017）进 Automation 契约面。
- torchwood 按新词汇表迁移其已编码客户端（stopping/stopped 保留，stopReason=exited → completed 等映射随契约重构）。

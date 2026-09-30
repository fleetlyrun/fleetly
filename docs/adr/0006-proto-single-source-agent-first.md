# proto 单一契约源；Agent 面走 CLI + Skills，不做 MCP

人类与 AI Agent 同为一等用户，三面同等能力：Console UI、API、CLI。决定：一份 proto 服务定义生成 gRPC + REST(gateway) + OpenAPI + Console TS 类型，杜绝双真源；Console 无私有服务端面；CLI 是瘦 API 客户端且与 API 能力对等（写面全覆盖、全命令 `--json`）。对 Agent 的支持**不建 MCP 服务**（2026-09-30 用户直裁：MCP 协议体验不佳），改由两件东西承载：①机器可读的 CLI 契约——稳定 JSON 输出、错误信封（errcode + 处置提示）、稳定退出码、`--wait` 等待原语、`events follow` 流式订阅；②随仓版本化的专属 Skills（`skills/`，包装 CLI 工作流：部署诊断、数据库开通、Task 池管理等）——Skills 承载程序性知识，CLI 是唯一执行面。API 层的幂等键（Idempotency-Key）与事件流保留，它们同样是 CLI 脚本化的地基。

## Consequences

- CLI 输出成为对外契约：字段名稳定性进 golden；`--json` 全命令覆盖是 CI 守卫（归档项目 23 命令缺 `--json` 的缺口不重演）。
- Skills 与平台版本同批演进：破坏性 CLI 变更必须同步更新 Skills；无 MCP 双真源维护成本。

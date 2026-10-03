# proto 单一契约源；Agent 面走 CLI + Skills，不做 MCP

人类与 AI Agent 同为一等用户，三面同等能力：Console UI、API、CLI。决定：一份 proto 服务定义生成 gRPC + REST(gateway) + OpenAPI + Console TS 类型，杜绝双真源；Console 无私有服务端面；CLI 是瘦 API 客户端且与 API 能力对等（写面全覆盖、全命令 `--json`）。对 Agent 的支持**不建 MCP 服务**（2026-09-30 用户直裁：MCP 协议体验不佳），改由两件东西承载：①机器可读的 CLI 契约——稳定 JSON 输出、错误信封（errcode + 处置提示）、稳定退出码、`--wait` 等待原语、`events follow` 流式订阅；②随仓版本化的专属 Skills（`skills/`，包装 CLI 工作流：部署诊断、数据库开通、Task 池管理等）——Skills 承载程序性知识，CLI 是唯一执行面。API 层的幂等键（Idempotency-Key）与事件流保留，它们同样是 CLI 脚本化的地基。

## Consequences

- CLI 输出成为对外契约：字段名稳定性进 golden；`--json` 全命令覆盖是 CI 守卫（归档项目 23 命令缺 `--json` 的缺口不重演）。
- Skills 与平台版本同批演进：破坏性 CLI 变更必须同步更新 Skills；无 MCP 双真源维护成本。

## 附录：CLI 取参形态裁决（2026-10-03，N1 收尾批 D25）

每个 CLI 动词的取参形态只有两种合法形态，不立第三种：

1. **旗标形态**：全部参数走 `--flag`，usage 不含位置参数。多余位置参数一律
   用法错误（退出码 64），不裸放行——位置参数被 Agent 误当合法取参面而静默
   吞掉，是隐式契约的滋生口（守卫：`TestFlagVerbsRejectPositionalArgs` 逐一
   枚举全部旗标形态动词）。
2. **位置参数形态**：宾语本体（资源 ID/名称）是位置参数，元数固定（通常恰
   一个），元数错即 64（先例：`projects create NAME`、`networks approve
   PEER_ID`、`databases get DATABASE_ID`）。

不立第三种的具体禁令：

- 同一动词不得同时接受"旗标或位置参数"两种取参路径（混用形态不注册；
  skills 守卫的 flag-after-positional 红灯实验钉死旗标落位纪律）。
- 序列值不做隐式切分魔法：列表参数用可重复旗标（每次出现一个元素），不用
  逗号等分隔符拆单值（2026-10-03 `--command` 退役逗号形态为可重复旗标，
  D24 裁决——`--env`/`--secret-ref`/`--watch` 同款既有形态）。
- 守卫两路执法：Go 侧 `TestFlagVerbsRejectPositionalArgs`（动词表）+
  skills 侧围栏命令逐字解析（元字符禁令防 shell 劈链：反引号/`|`/`&`/
  `$`/`;`/`\`/`>`/`<`）。

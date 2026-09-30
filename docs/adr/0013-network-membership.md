# Network 成员模型：挂靠、跨 Project 引用与 egress 语义

Network 保留 Project 级默认互通；成员分三类：①Project 内 Process（默认全通）；②Task Run 加入所属 Task Network Group——**创建时刻**挂靠（per-run 运行时动态挂网的禁令维持，源自 2026-09-14 队列卡死事故）；③显式跨 Project 引用（双向声明、接收方批准，服务 messageloop ↔ torchwood 投递互通）。App Process 可显式挂靠指定 Task Network Group（torchwood dispatcher↔runner 通道，继承旧 DT-5：每网一次性摊销挂靠、失败 fail-closed 重声明）。

网络可声明 `egress: none`（不可信代码隔离语义）。诚实边界：swarm Provider v1 只实现弱隔离（独立 overlay + 不发布端口 + 不注入跨网 DNS，出网不阻断，文档与 Console 明示）；k8s Provider 用 NetworkPolicy 原生满足。编排器没有的语义不假装有。

## Consequences

- `processes[].networks[]` 引用三种目标：project-local 名 / `taskGroup:<name>` / 跨 Project 引用；跨 Project 挂靠计入双方审计。
- k8s Provider 落地时 egress:none 从弱隔离升级为强隔离，API 语义不变（能力发现端点暴露差异）。

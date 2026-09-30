# Agent 治理刹车：配额、速率、change freeze 与属主吊销处置

持写 Scope 的 Token 是自动化地基，也是失控面（失控 Agent 可无限 CreateTask 打爆集群）。决定四件刹车：①per-Project 的 Task/Workload 数量配额；②per-Token 创建速率限制；③change freeze——API 级变更冻结窗（按资源/动作/条件封禁，命中返回带原因的拒绝，继承 tsuru Block 语义）；④属主 Token 吊销时，名下 Task 默认宽限排空（可配置为跑完 TTL），处置进审计。Scope 语义维持"读默认开放、写显式授权"（write 蕴含 read 不变）。

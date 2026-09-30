# 词汇冻结：CONTEXT.md 词条在实现期内不更名

归档项目因"机制先于词汇"付出十余次全库更名（duty→管理器、apply→deploy、restore→replay、sweep 词表侵蚀、PrjSlug 双拼）。决定：本次词汇先行（CONTEXT.md 在第一行实现代码之前冻结），术语 `Deployment/Revision/Task/Run/Schedule/Runtime/Capability/Provider/Workload/Route/Edge/Spec/Replay/Restore/Enrollment` 等进入禁改清单；实现期内发现词条不合适，视为设计缺陷走显式 ADR 更名（一次性、全库、含 golden），不接受局部混用。

## Consequences

- CI 禁词扫描以本清单为源（duty、substrate、ingress、apply、缩写词等）。
- 新概念必须先入 CONTEXT.md 再写代码——评审顺序强制。

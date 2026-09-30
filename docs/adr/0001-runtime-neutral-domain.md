# 领域模型零编排器概念，Spec 是唯一运行时边界

归档项目的头号失误是把 Swarm 的形状当成领域模型（命名公式、label 契约、放置约束语法、ServiceSpec 投影都住在核心包），导致"换底座=重写半个平台"。决定：`internal/model` 与 `internal/spec` 中禁止出现任何编排器概念（service/pod/task/label/namespace/约束语法），载体命名、`fleetly.*` 标记、约束编译全部是 Runtime Provider 的私有实现；归属与漂移判定以平台 ID 查权威表，永不解析载体命名。

## Consequences

- k3s/nomad Provider 落地时，领域与 API 面零改动（验收场景见领域模型 §5 场景 3）。
- 守卫测试静态扫描 spec/model 的字段黑名单，违反即 CI 红。

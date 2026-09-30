# Spec 作为版本化中间表示，Revision 为冻结快照

各参考 PaaS 在"用户意图"与"编排器资源"之间要么没有中间表示（coolify/dokploy 直拼命令），要么模型合一（caprover）。决定：定义带 `schemaVersion` 的规范化 IR——AppSpec / TaskSpec / DatabaseSpec——作为一切 Provider 的唯一输入；源（Compose 受控子集 / API 创建 / 上传产物）先归一化成 Spec，应用时冻结为不可变 Revision，部署基线永远以 Revision 为准，不回读源文件。schemaVersion 演进采 check-strategy：可读旧版并提示，拒绝跳代。

## Consequences

- 换 Provider 或回滚都不需要重新解释源文件。
- 加字段必须走 schemaVersion 评审，防 IR 野蛮生长。

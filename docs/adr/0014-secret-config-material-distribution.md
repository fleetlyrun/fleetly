# Secret/Config 一等实体与材料分发

Secret 与 Config 升格为一等实体（Project 级）：Secret 以 age 信封加密存储（KEK 在数据根、可轮换、轮换 Procedure 入 runbook），**值永不回显、只回指纹**，读写全审计；Config 为版本化明文挂载文件（继承旧 OT-3：可回读、配额）。注入面覆盖 App Process、Task、Database（`credentialsRef` 落到 Secret）。

Runtime 契约补强：Ensure 携带平台侧已解析的镜像拉取凭证与注入材料，Provider 负责按节点分发（swarm `--with-registry-auth` 等价）；凭证不得以载体 label 或明文环境变量形式落盘（旧 DT-2 真机教训：不传 auth 私有镜像逐节点 404）。Platform Backup 含密封密钥，恢复时解封闭环（KEK 单独保管提示）。

## 附录 A.2：数据库连接材料经载体 spec env 下发（2026-10-14 追认）

`ProcessSpec.database_refs`（ADR-0072 批）声明的托管库引用，在 materialize 序列由平台解析为 `FLEETLY_DB_<NAME>_*` 环境变量并**随载体 spec env 下发**（swarm/k3s 的 ContainerSpec.Env；值取 credentials_ref 单源连接串 + DatabaseDNSName 铸名，值不进 Spec/Revision/日志）。本附录追认：**数据库连接材料与镜像拉取凭证（RegistryAuth，同文件前文）同信任域随载体 spec 下发**——集群 API 访问权即 root 信任，二者暴露面无差别；通用 `secret_refs` 的文件注入面（`/run/secrets/<名>`）维持不变。选型理由：env 形态是 compose 模板生态的 dokploy 事实标准（服务 label `fleetly.databases` 的消费者即进程 env），文件契约会迫使第三方脚本改 sourced 形态，破坏模板库"原样承接"目标。变量集与名字规范化见 `internal/engine/databaseenv.go`。

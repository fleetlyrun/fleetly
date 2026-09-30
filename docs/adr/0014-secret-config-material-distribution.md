# Secret/Config 一等实体与材料分发

Secret 与 Config 升格为一等实体（Project 级）：Secret 以 age 信封加密存储（KEK 在数据根、可轮换、轮换 Procedure 入 runbook），**值永不回显、只回指纹**，读写全审计；Config 为版本化明文挂载文件（继承旧 OT-3：可回读、配额）。注入面覆盖 App Process、Task、Database（`credentialsRef` 落到 Secret）。

Runtime 契约补强：Ensure 携带平台侧已解析的镜像拉取凭证与注入材料，Provider 负责按节点分发（swarm `--with-registry-auth` 等价）；凭证不得以载体 label 或明文环境变量形式落盘（旧 DT-2 真机教训：不传 auth 私有镜像逐节点 404）。Platform Backup 含密封密钥，恢复时解封闭环（KEK 单独保管提示）。

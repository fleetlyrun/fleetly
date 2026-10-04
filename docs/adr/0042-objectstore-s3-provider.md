# ADR-0042: ObjectStore S3 Provider——外置 S3 兼容目标，platform_backup.s3 五元组复用

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-04 | CONTEXT.md ObjectStore/Backup/Platform Backup 词条、ADR-0020（本地目标开箱即用 + 同机诚实边界）、ADR-0021（minio 系选型先例）、ADR-0039 决策 1/9/12（备份双轨、restic S3 仓、F2.8 钉形——决策 12 的 `server.object_store` 块形态由本 ADR 修订）、ADR-0040/0041（受管自宿范式与装配 ctx 注入范式）、F2.8（checklist） |

## 背景

F2.8 收口 ObjectStore 的第二 Provider：外置 S3 兼容目标。端口（`capability.ObjectStore`：
Put/Get/Stat/List/Delete + 键契约 + `ErrObjectNotFound` 哨兵）与第一实现 localObjectstore 已在
F2.2 随 ADR-0039 落地；消费方（engine 备份执行器）经 `capability.Build(KindObjectStore, "")`
取唯一在册实现。五件设计点（config 块形态/RustFS 是否随批/凭证落点/S3 客户端选型/装配
选择机制）需本 ADR 先行裁决；checklist 原文还带"RustFS opt-in 自宿"。

## 决策

1. **config 形态 = 复用 `platform_backup.s3` 五元组**（修订 ADR-0039 决策 12 钉的
   `server.object_store` 独立块形态）：
   - v1 依据：**Backup 是 ObjectStore 的唯一消费方**（engine 备份执行器是仅有的
     Put/Get/List/Delete 调用面）；restic 仓与 ObjectStore 指向同一桶时天然一致——
     配一次 s3 = 双轨齐离机（数据面对象直写 S3 + 控制面 restic 仓推 S3）。
   - 零新配置面：五元组已在册（endpoint/bucket/prefix/access_key_id/secret_access_key，
     proto 无改动）；「同机备份非灾备」告警的解除谓词（`PlatformBackupS3() != nil`）
     与 Provider 选择谓词是同一谓词——告警归位路径（engine/metrics.go
     evaluateSystemRules 与 doctor 消费面）无需改动。
   - **prefix 字段归 restic 仓**：ObjectStore 不消费 prefix，对象键落桶根 `backups/`
     命名空间（端口键契约天然以 backups/ 开头）。同桶双命名空间共存
     （`<prefix>/` restic 仓 + `backups/` 对象键）；边界：prefix 勿取 `backups`
     （会与对象键首段冲突）。产物面（uploads/ 等）成为 ObjectStore 消费方时再议
     独立块与多目标（挂账，见"后果"）。
   - 修订理由记录：决策 12 钉形时 ObjectStore 曾预期多消费面；F2.8 落地时消费面
     仍是备份单家，独立块 = 重复配置面 + 两个谓词（告警解除 vs 目标选择）可能
     漂移的长期负担。ADR-0039 其余决策不受影响。

2. **装配选择 = 在场驱动**（本批真正的装配层改动）：localObjectstore 恒在册
   （本地备份目标开箱即用，ADR-0020）；s3 Provider 注册名 `s3`。
   `assembly.NewObjectStore` 按 `cfg.PlatformBackupS3()` 切换：在场 → `Build(ctx, "s3")`
   （五元组经装配 ctx 注入工厂，`WithObjectStoreS3`——config 是唯一契约源，
   WithLoggingAddr/MetricsAddr 范式，无 env 旧通道），缺席 → `Build(ctx, "local")`
   （升级零扰动：未配 s3 的存量部署行为与现状零差）。s3 工厂读到 ctx 无配置 =
   精确启动失败（fail-fast，不静默回退 local——配置了 s3 却落本地是静默降级）。

3. **RustFS 不随批（挂账）**：v1 只做外置 S3。理由：又一受管自宿域的全生命周期
   成本（FacesOf 子面协商/材料字节稳定/凭证绝不进 argv/带卷钉控制面节点/
   per-域 Generation——zot/VL/VM 三批各是一整批的量）；本批核心价值是"备份离机
   可达"，任何现成 MinIO/云 S3 都能交付，无 S3 的用户保有本地目标开箱即用 +
   持续告警提醒。RustFS 受管 Provider（install.sh 物化 + 受管域 + opt-in）走
   后续批。

4. **凭证落点 = 沿用 config 明文（0600）**：secret_access_key 已是 config 明文形态
   （restic 仓在用，ADR-0039 决策 9）。信封化（age 信封，ADR-0014 范式）需要 config
   解密层与密钥生命周期，是独立批的改动面。边界记录：install.sh 的 env 文件
   0600 已有；生产建议为平台铸专用低权 access key。升级信封化时五元组与
   Notification Channel（ADR-0041 同边界）一并议。

5. **S3 客户端 = minio-go v7（钉 v7.3.0）**：S3 兼容生态标准客户端（MinIO/RustFS/
   各云厂商 S3 兼容端点同面），传递依赖克制（7 个）；aws-sdk-go-v2 重（数十
   module）违背架构 §10 依赖克制文化。端点 scheme 语义与 restic 对齐：`http://`
   前缀 = 明文，无 scheme 或 `https://` = TLS。region/path-style 不配（minio-go
   自行协商：非 AWS 端点 path-style、bucket region 自动探测）——五元组即全部面。

6. **行为语义 = 对齐 localObjectstore**（端口契约同构执法）：
   - **键钳制**：safeKey 同款（正斜杠分层/每段非空/禁穿越段/禁反斜杠与盘符）
     ——S3 键无路径语义，但端口契约是全 adapter 同构语义，穿越形态在列举前缀
     与跨 adapter 迁移面仍是攻击面。
   - **Put 流式回执**：TeeReader 流式铸 sha256 + minio-go 未知尺寸 multipart 流式
     上传（size=-1，内存界 ~64MiB/部件，磁盘零落）；失败 = minio-go 整体 abort
     multipart，无回执。回执 Digest/Size = verify 锚（与 local 同形）。
   - **Get**：ctx 贯通（取消即断流）；**Stat/Get** 的 NoSuchKey → `ErrObjectNotFound`
     哨兵；**Delete 幂等**（S3 DELETE 本身幂等：缺席对象 204）；**List** 前缀同受
     钳制、按键稳定排序。
   - **Health = BucketExists 探测**（不自动建桶：AWS 面建桶是账号级动作，平台不
     代作）；缺桶 = unhealthy + 精确 Details。Describe Notes 诚实记录：外置目标、
     凭证明文边界、切换前的存量对象仍在本地目录。
   - **Provider 切换不迁移对象**：切 s3 后，切换前的台账行对象仍在本地
     `backups/`（restic 备份集捎带其离机）；这些行的 verify/restore 对 S3 端点
     报 object not found（诚实失败）。跨 Provider 迁移面留真实需求出现再议。

## 后果

- `capability` 增 `ObjectStoreS3Config` + 装配 ctx 载键（叶子纯度：仅标准库）；
  `internal/providers/s3objectstore` 新成员（client 六操作缝：生产 minio-go /
  测试 fake——resticExec/fileSync 函数值缝文化的接口形态）。
- proto/config 生成物零改动；go.mod +minio-go（架构 §10 记账）。
- e2e dind-backup.sh 追加 S3 腿（dind 内 S3 兼容假端点 = **silo**，钉版
  `pgsty/silo:RELEASE.2026-09-16T00-00-00Z`——MinIO 社区版 2026-02 EOL 后的
  社区续命版，镜像自带 mcli 客户端，建桶与断言零额外镜像）：换装 s3 env →
  备份对象落桶（object_key/size 精确断言 + 本地目录零新增）→ verify →
  恢复到新库 → 数据断言 → restic 外置仓同桶 roundtrip → 内置告警行 ok。
  CI e2e-backup job 扩腿（六 job 结构不变）。
- 挂账：RustFS 受管自宿（决策 3）；ObjectStore 多目标/独立块 + 产物面消费
  （决策 1）；五元组信封化（决策 4）。

## 验收锚

- [x] s3 Provider 五操作行为语义与 localobjectstore 同构：键穿越拒绝、Put 流式
      digest 回执、NoSuchKey→ErrObjectNotFound、幂等删、List 前缀钳制 + 稳定排序
      （fake 缝单测；另本地真机对 silo RELEASE.2026-09-16 二进制 roundtrip 全绿）
- [x] 装配选择：未配 s3 = local（现状零差）；配 s3 = s3 工厂装配（ctx 注入），
      配置缺席被选 s3 = 精确失败（objectStoreSelection 纯函数测试 + 工厂测试 +
      五元组 env 覆盖常驻测试——viper 裸 Unmarshal 不吃 env-only 嵌套键的坑钉死）
- [x] 配 s3 后 `databases backup` 产物落 S3 桶且 verify 绿；恢复到新库数据断言
      （e2e S3 腿，CI run 37217410590：object_key/size 桶内精确断言 + 本地目录
      零新增 + S3 件恢复数据断言 DRILL_S3）
- [x] 内置规则 platform-offsite-backup 不 firing（消警路径零改动回归：e2e
      alerts list 断言 state ok；engine 侧 24h 窗测试既有覆盖未动）
- [x] e2e S3 演练进 CI（e2e-backup job 扩腿；三修实录：mcli alias set 自身是
      签名探针需重试环——nc -z 探到 dockerd userland-proxy 非服务器本体；
      protojson int64 带引号形态；run 37217410590 六 job 全绿，silo 钉版
      RELEASE.2026-09-16T00-00-00Z 经 restic 0.19.1 s3 backend init/backup/check
      本地预验证）

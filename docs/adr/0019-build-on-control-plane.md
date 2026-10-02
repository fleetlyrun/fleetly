# 构建执行面 v1：恒在控制面节点

Build 恒在控制面节点执行（诚实单点口径的自然延伸）：BuildKit + 本机 daemon，并发上限可配（队列 admission 同 ADR-0016），构建缓存只在本机。多节点构建缓存分发与独立构建节点（dokploy/coolify 的 build server 形态）显式延后，不进 v1 叙事。上传构建（build-from-upload）走流式 tar + 大小上限，断点续传延后。

## 附录 A：build-from-upload 落地裁决（F1.10，2026-10-02）

Source 第三形态（上传产物）的契约、执法、存储与清理裁决。词汇真源：UploadSource（spec.proto 已冻结词条）；行叫 source upload，blob 叫上传产物。

### A.1 契约形态

- **UploadSource 是仓内首个 client-streaming 写面**，落在 BuildsService（上传是构建材料接入，不是部署动词）：首帧 meta（project_id），余帧 chunk（tar 字节流），EOF 后内容寻址落库并返回 `{id, digest, size_bytes, deduplicated}`。gRPC-only：client-streaming 无 HTTP 注解面（grpc-gateway 不支持），REST/Console 形态随 Console 批次另行裁决（先例=ReceiveWebhook 无 HTTP 绑定）。
- **ListUploads 读面**带 `after_upload_id + limit`（ADR-0026 List 惯例，新 List 面一律从之）。
- **DeployRequest 增第三源**：`upload_id + dockerfile`（与 image/compose_yaml 互斥；dockerfile 缺省 "Dockerfile"）。归一化产物=Source.upload + BuildSpec(dockerfile) + 单 web 进程 from_build（与 git 源 webhook 路径同构，gitDeploySpec 对偶）。
- **Upload 是 project 级材料**（与 Secret/Config/Volume 同口径）：meta 只带 project_id；Deploy 受理位校验 upload 行归属同 Project（跨项目引用拒绝）。

### A.2 流式面怎么过执法链

- **authn**：metadata 面，既有 `Stream()` 原生适用。
- **freeze**：Upload 是变更动词，进封禁面。新增 `FreezeGuard.Stream()`——首帧 RecvMsg 包装执法（拦截器包装 stream 的 RecvMsg，首次调用解析 Team 并查冻结；拒绝以首帧 Recv 错误面呈现，handler 零字节写入即返回）。Team 解析锚=首帧 project_id（scopeProject 链复用）。流式链从仅 authn 升为 authn→freeze。
- **幂等表豁免（显式裁决）**：Upload 动词不在创建型前缀集（idem 守卫天然不咬），也不进 EnforcedMethods。理由：claim 语义需要执行前的请求体指纹，而流面体指纹（digest）只在 EOF 后可知——先认领后执法等于空转；内容寻址给出**强于**键重放的天然幂等：同字节重传返回同一 upload 引用（deduplicated=true），无 24h 窗口、无键纪律要求。
- **速率预算不计数**：per-Token 创建预算是 unary 面（重放不耗预算的计数口径建立在幂等面之上）；流面刹车=单上传大小上限 + 项目存量配额 + freeze，已足够有界。
- **无 unary 30s 超时**：512MiB 慢链路合法超 30s；流生命周期由取消信号管理，外加 15m 流硬上限（与构建超时同量级）。

### A.3 存储语义

- **blob 全局内容寻址**：`DataRoot/uploads/<sha256hex>`；接收期写 `tmp-<ulid>`，EOF 校验后原子 rename。跨项目同内容共享 blob（refcount=行数）。
- **行按 (project_id, digest) 唯一**：同项目同内容重传返回同一行；跨项目各行。行只登记事实（id/digest/size/created_at），不设状态机——上传是一次性动作，不存在在途语义（流中断=tmp 丢弃，无行）。
- **限额**：单上传 512MiB（`E_UPLOAD_TOO_LARGE`，诚实错误码——死于 gRPC 传输层 RESOURCE_EXHAUSTED 的不透明形态是 Q-11 限额错位教训）；项目存量 4GiB（`E_QUOTA_EXCEEDED`，distinct digest 求和、读在 finalize 事务内——ADR-0024 配额 TOCTOU 口径）。分帧 chunk（≤1MiB）远小于 grpcMaxRecvMsgSize（32MiB），单条消息永不撞传输层上限。
- **保留窗 7d**：未引用行清扫（引用=Revision spec 内含 upload id——spec 体内 26 字符 ULID 字面量对账；revision 冻结先于 engine.Submit，部署路径的上传天然受保护）。末行删除时 blob 一并删除；孤儿 tmp（>1h，崩溃遗留）由 retention janitor 顺带清理。

### A.4 构建链

- building 驱动时 blob → **tar 安全解包**到 `contexts/<revision>`（幂等：目录已存在即跳过——Revision 冻结体 + 内容寻址 blob = 解包是纯函数，与 git 检出同位）。解包白名单=普通文件+目录；路径逃逸（`..`）、绝对路径、symlink/hardlink、设备文件拒绝（tar-slip 防护）；解包总量计数不越单上传上限（未压缩 tar 的展开体积上界=tar 自身体积）。
- 上传 tar 形态=未压缩（digest 即内容恒等，无压缩参数漂移面）；CLI 侧 tar 写出确定性归一（固定 mtime/uid/gid，`.git` 目录不进上传——纯 VCS 元数据，构建永不引用）。`.dockerignore` 类过滤面延后。

### A.5 错误码与事件

- `E_UPLOAD_TOO_LARGE`（ResourceExhausted）：单上传超限，信封带 limit 与已收字节。
- `E_UPLOAD_UNAVAILABLE`（FailedPrecondition）：行在 blob 失（清扫后残留行 / 数据根损坏）——部署引用的诚实形态。
- 事件 `upload.stored`（payload: id/digest/size_bytes/deduplicated/project_id）；审计 action=`upload.create`。

### 验收锚（F1.10 落地 2026-10-02，证据=测试在树）

- [x] 同字节重传返回同一 upload id（deduplicated=true）且不重写 blob（apitest TestUploadSourceLifecycle + upload 单测 TestStoreCommitDedupAndDigest）
- [x] 超单上传上限 → E_UPLOAD_TOO_LARGE（错误码信封，非不透明 RESOURCE_EXHAUSTED；REST 413）；超项目存量 → E_QUOTA_EXCEEDED（apitest TestUploadSourceLimits + errcodeToHTTP 守卫）
- [x] 冻结期间 UploadSource 被拒（E_CHANGE_FROZEN，首帧即拒，零字节写入——freezestream 单测 + apitest TestUploadSourceFreeze）
- [x] deploy --from-dir 全链：上传 → 部署 → 构建输入=解包后目录（FakeBuilder ContextDir 断言）+ 跨项目 upload 引用被拒（apitest TestUploadSourceLifecycle / TestDeployUploadCrossProject + CLI golden deploy-from-dir 双形态）
- [x] tar 路径逃逸（../ 前缀 / 绝对路径 / symlink / hardlink / 反斜杠 / 重复条目）拒绝（upload 单测 TestExtractTarRejectsUnsafe）
- [x] 未引用上传过保留窗清扫（含 blob 末行 GC）；被 Revision 引用的上传不清扫（sourceupload 单测 TestSweepUnreferenced；janitor 接线 assembly sweepUploads）
- [x] freezeguard 三桶分类含 UploadSource；AssertAllRegisteredHavePolicy 通过（authz 注解在位）（guards 全绿 + apitest 夹具 fail-closed 断言）

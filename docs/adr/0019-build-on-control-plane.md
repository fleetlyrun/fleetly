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

## 附录 B：受管 zot 自宿与 digest 直存（F1.11，2026-10-02）

构建产物从"本机 daemon 导入"升为"推送受管仓库、worker 从仓库拉"。词汇真源：Registry（CONTEXT.md，OCI 镜像仓库 Capability，拉取来源与推送目标）；行/产物词汇沿用 Build/Digest。

### B.1 受管形态

- **zot 以受管 Workload 形态自宿**（ADR-0004 通用 reconciler，traefik 先例）：`internal/providers/zot` 实现 Registry 端口 + Managed 声明 + MaterialsSource 子面（config/htpasswd 经 Materials.SecretFiles 走 ADR-0014 载体通道，容器内 `/run/secrets/`；值变更=新载体指纹名=service spec 变更=滚动替换，凭证轮换因此自愈）。受管域 ns=`fleetly/system/registry`；**不挂项目网**（zot 无需触达用户后端，可达性=发布端口），与 Edge 挂网语义的差别在 reconciler 内显式分叉。
- **镜像钉版**（ADR-0021 口径）：`ghcr.io/project-zot/zot-minimal:v2.1.21`（minimal 变体：htpasswd 认证是核心能力非扩展；升级经 Platform 升级序，ADR-0015）。
- **端口发布 5000**（routing mesh；`Workload.Publish` 声明——发布面仍是受管形态专属不变式）；数据卷 `fleetly-registry-zot`（swarm 具名卷自动建，本地卷）挂 `/var/lib/registry`。

### B.2 地址与网络

- **镜像引用地址 = 环境变量 `FLEETLY_REGISTRY_ADDR`**（含端口，如 `10.124.0.3:5000`；与 `FLEETLY_EDGE_CONFIG_ENDPOINT` 同款 env 注入文化，install.sh 物化进 unit）。地址是平台级配置，**不进 Revision 冻结体**——Revision 冻结 `from_build` 引用与 Build digest，完整引用在投影期组合（换址后重放即用新址，无迁移面）。
- **HTTP 明文（集群内网）**：全部节点 dockerd 带 `--insecure-registry <addr>`（install.sh 写 daemon drop-in；worker 加入时同款——runbook 责任）。TLS 面随内部 CA 批次另裁（IP 形态地址拿不到 LE 证书是硬约束）。
- 未配置 `FLEETLY_REGISTRY_ADDR` → Registry Provider 不装配（Edge 同款诚实降级）：镜像直投部署不受影响，build 源部署在 prepare 精确失败（见 B.5①）。

### B.3 凭证面

- **单一平台凭证**：username=`fleetly` + 首启随机密码，落 `<DataRoot>/keys/registry.json`（0o600，与 KEK 同目录文化，备份随数据根）。htpasswd=bcrypt。
- **分发三面同源**（平台凭证是唯一真源）：①zot 自身（htpasswd 材料经 MaterialsSource）；②构建推送（`BuildRequest.PushCred` → daemon ImagePush 的 X-Registry-Auth）；③worker 拉取（`materialsFor` 对 managed host 直接注入平台凭证 → swarm EncodedRegistryAuth——F0.18 场景 13 真机实证过的通道）。
- **managed host 上项目级 Secret `registry:<host>` 不参与**：平台凭证覆盖（用户无法也不需要为平台仓库自配凭证）；其余 host 走既有 Secret 查询通道不变。
- **轮换** = 删凭证文件 + 重启（B.1 载体指纹链自愈滚动）；自动化随配置面批次。

### B.4 digest 直存与引用形态

- **推送后冻结 manifest digest**（`Build.Digest`）；from_build 投影为 `<addr>/<app-id>@sha256:<digest>`；tag 形态 `<addr>/<app-id>:r<seq>` 仅作推送目标与本机缓存命名。`LocalImageRef`/`LocalImageDigestRef` 是唯一命名真源（勿散落副本）。
- **动机 = docker29 真机坑**（digest-pull 后 digest 不落 tag、digest-pull save/load 丢 tag）：Revision 冻结 digest + 下发 digest 引用，绕开 tag 语义全部坑；重放/回滚天然钉版。
- **ADR-0022 漂移对照核对**：digest 引用不可变，对照只会更稳；"swarm 不重写 spec.Image"的既有实证结论对 digest 形态同样成立（tag 形态的 CLI 钉版假 drift 面不适用于 digest 引用）。

### B.5 诚实边界

1. **无单节点退化形态**：无 Registry Provider 时 build 源部署精确失败（"managed registry is required"）。理由：N0 本机导入形态的 digest=裸 image ID，真机 swarm 的 service create 根本不可拉取（本就不可用），诚实拒绝优于静默死路。
2. **数据卷节点本地、无钉住**（traefik acme 同款边界）：zot 重调度到别的节点=镜像丢失；运行中服务不受影响（镜像已在节点上），新部署触发重建自愈。受管 Workload 数据钉住另批。
3. **镜像 GC 延后**：zot 内镜像只增不清（Revision 引用对账 + 清理面随保留窗批次）。
4. **单平台构建**（控制面架构）；多 arch 延后（原口径"随 F1.14 构建器扩展"经 ADR-0032 重裁：F1.14 是 railpack/static 双 builder 从空面到真面，多 arch 仍延后至真实多 arch 节点出现）。

### B.6 推送机制

- **docker-driver（daemon 内嵌 buildkit）不支持 image exporter push**——推送走 **daemon ImagePush**（`docker push` 同路径）：Solve（moby exporter 导入本机，缓存与本地调试保留）→ ImagePush(target, X-Registry-Auth) → digest 从推送流 aux（`containerimage.digest` 面）回填，RepoDigests 兜底。本机导入保留（构建缓存复用 + 推送即 `docker push` 语义）。

### 验收锚（F1.11，证据=测试在树 / runbook 记录）

- [x] zot Provider：凭证持久（同 dataRoot 二次构造同密码）、htpasswd bcrypt 可验、ManagedWorkloads 钉版形态（image/publish/volume）、Materials 含 config+htpasswd（zot 单测）（F1.11 批 zot 单测；重启滚替收口 662f783——keys/registry-htpasswd 持久化 0o600，F1.15 真机 zot 四连修 9-11 同批闭环）
- [x] build 源 + 无 registry → prepare 精确失败；有 registry → BuildRequest.Target=`<addr>/<app>:r<seq>` 且 PushCred 注入；from_build 投影=`<addr>/<app>@sha256:<digest>`（engine 单测）（F1.11 批 engine 单测；推送链 digest 回退链 hermetic 化 a113721）
- [x] managed host 平台凭证注入且项目 Secret `registry:<host>` 不参与；非 managed host 走 Secret 通道不变（engine 单测）（F1.11 批 engine 单测——materialsFor managed host 直注平台凭证）
- [x] reconcileManaged 双 Provider：zot 域无项目网挂靠、Materials 透传（engine 单测）（F1.11 批 managed_edge_test——指纹覆盖完整下发集）
- [x] dockerbuild 推送凭证编码 + digest 提取（单测）；推送链真机实证随 staging/F1.15（runbook 记录）（F1.11 批单测 + a113721 hermetic；真机 F1.15 ⑤ runbook 2026-10-02 节）
- [x] install.sh：daemon insecure-registry drop-in + unit 注入 FLEETLY_REGISTRY_ADDR（存在 daemon.json 时不覆盖，打印人工指引——诚实不破坏）（F1.11 批；runbook 拓扑节在位实证——双节点 drop-in + unit env）
- [x] staging 真机：受管 zot 起服（停手工 n0-zot 让位端口）+ 构建推送 + 双节点 digest 拉取（runbook 记录）（F1.15 ⑤：buildprobe 构建→push zot→node2 以 `@sha256:3ba27221...` 拉起 Running）

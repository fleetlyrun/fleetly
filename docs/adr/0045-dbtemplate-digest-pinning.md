# ADR-0045: dbtemplate 目录化收口 + 数据库引擎镜像 digest 钉定门禁

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-05 | DT-9（domain-model §8）、ADR-0021（钉版口径）、ADR-0029（模板注册表与 digest 空槽）、ADR-0039（备份执行链对模板的消费）、P7（digest 确定性纪律，optimization-proposals §P7）、F2.7（checklist） |

## 背景

F2.7（DT-9）要求 dbtemplate 目录化 + 数据库引擎镜像 digest 钉定。两项前提事实：

- **目录化已提前兑现**：2026-10-03 架构评审第二轮（候选 1）把
  `internal/engine/dbtemplate.go` 单文件拆为 `internal/engine/dbtemplate/` 子包——
  per-engine 接口 adapter + 注册表零 switch（ADR-0029 决策 2 落位修订已记档）。
  本 ADR 只做收口裁决，不重做。
- **tag 级钉已被真机咬过两次**：`postgres:17-bookworm` 是 mutable tag，上游刷新
  曾带来 18+ 目录布局入口（docker-library/postgres#1259，dind 实证拒启）与镜像
  VOLUME 声明遮蔽命名卷（2026-10-04 staging 空库事故）——两次都是"tag 悄悄换
  镜像、模板假设失效"的同一类事故。ADR-0029 后果实已预留 digest 空槽
  （`Template.ImageDigest`），后果段明记"digest 级钉随 F2 裁决"。

P7 把本项列为 fleetly 三处 digest 消费点的第三处，口径已钉：**registry digest
透传，不经我们计算，只核对传递完整性**——digest 由 registry 铸（内容寻址），
我们记录、传递、断言形态，不派生。

## 决策

1. **目录化收口裁决**：目录化的物理形态 = 既有 per-engine adapter 子包（已落地，
   不再演进成数据文件/外部清单目录）。"清单形态"由两层承载：
   - digest 钉定对（tag + digest 常量）住在各引擎 adapter 文件内——per-domain
     钉版常量先例（railpack 住 builders、restic 住 platformbackup、zot tag 住
     providers/zot）；
   - `dbtemplate_test.go` 的 TestTemplateFaces 逐字钉板即**可枚举清单视图**
     （五引擎镜像引用 + digest 全量 golden）。
   否决中心化 pins map（"lock file"形态）：与引擎名反扫守卫的 locality 原则
   （per-engine 知识住在 adapter）相悖，且引入第二张 engine→镜像 map 的同步面，
   无增益——bump 常见形态本就是单引擎单文件。

2. **digest 形态**：`Image()` 返回 `repo:tag@sha256:<digest>`（tag + digest 双段
   引用；digest 主导、tag 是可读性面——`docker service ps`/事件流里版本可见）。
   钉的 digest = **OCI index / manifest-list digest**（multi-arch 真源；registry
   API `Docker-Content-Digest` 头的原值），不钉单 arch manifest digest。`ImageDigest()`
   返回带算法前缀的 digest 串（P7"输出带算法前缀"条款）。首版钉定集（2026-10-05
   经 Docker Hub registry API 核对，全部 content-type
   `application/vnd.oci.image.index.v1+json`）：
   - postgres `postgres:17-bookworm` → `sha256:639ab7ceb90e13123085b741fb31ef493fba25463002f6da665352e7b534b652`
   - pgvector `pgvector/pgvector:0.8.6-pg17-bookworm` → `sha256:cf134a767f474095eeba57e0117be8e568e011a63f33fbf252f14c9b760f8e6f`
   - redis `redis:7.4` → `sha256:c6eabf748fc7a61dbb5a705c78bcf3d6377b1127a97d0ce965c11c44ba46896f`
   - mysql `mysql:8.4` → `sha256:6ea90827b1100f8f2ae306a539f86d2c264a26ed435a2a9f75551dd5c3aeb242`
   - mongo `mongo:8.0` → `sha256:d0d926f94df099bff534b7ee5b5986458131a22489dfff8664509af0c1e2ca9c`
   DT-9 原文的 "percona/pgvector" 沿 ADR-0029 事实修正：上游真源是
   `pgvector/pgvector`，钉定覆盖该镜像。

3. **执法位置 = 引用面冻结 + 测试门禁，否决部署期核对**：
   - **引用面冻结（主执法）**：digest 进 `Image()` 返回值后，全部消费面
     （databaseLoop 投影的 DB Workload、backup/restore 工具容器——engine 侧
     `tpl.Image()` 三处调用点单源收编）下发即内容寻址引用；编排器按 digest
     拉取，registry 协议本身核对内容完整性。这是 P7"只核对传递完整性"的
     落法：我们不核对镜像内容，核对的是**钉定值在传递链上不被改写**——
     断言面 = `Image()` 恰为 `tag@digest` 拼装、`ImageDigest()` 与之一致、
     全值域非空（测试门禁）。
   - **测试门禁**：dbtemplate 测试断言全引擎 digest 在场、形态合法
     （`^sha256:[0-9a-f]{64}$`）、Image/ImageDigest 自洽——新增引擎漏钉即红
     （TestReservedSlotsVacant 空槽断言随本批改写为钉定断言）。
   - **否决部署期 registry 核对**（拉取前另查 tag 当前 digest 比对告警）：给
     收敛环引入 Docker Hub API 依赖与匿名令牌面，而 digest 引用拉取已由
     registry 协议保证内容一致，零增益纯成本。
   - P7 两条 canonical 性质测试（1000 次稳定 + map 序无关）**不随本批新增**：
     第三消费点是透传，无新 digest 计算面（既有性质测试已覆盖 fingerprint/
     CanonicalJSON 计算面）；透传完整性的"性质"由上述一致性断言承载。

4. **digest 变更的 Migration 序（升级时 digest 演进）**：digest 住模板（随二进制
   发布），变更即平台升级批的一部分，序列复用 F2.3 升级序既有机制、零新增迁移面：
   1. bump = 评审过的刻意动作：改 adapter 常量，同 commit 更新 TestTemplateFaces
      钉板与下游断言（database/backup 测试的镜像串）；
   2. 升级后 `databaseLoop` 指纹（`managedFingerprint` 含 `Workload.Image`）变化 →
      `EnsureGeneration` 推进 → stop-first 滚一次（卷钉住、数据存活、60s 停止
      宽限）——F2.3 staging 实录"仅 db 因 DataTarget 变更滚一次"的同一机制；
   3. 回滚 = 旧二进制重钉旧 digest 再滚回（同 major 内 patch 漂移数据兼容）；
   4. **e2e 升级矩阵的条件化零滚动断言**：`dind-upgrade.sh` 比较 HEAD~1/HEAD 两
      worktree 的 dbtemplate digest 集——无差 = 严格零滚动（原断言）；有差 =
      准许恰一次受监督滚动，滚后 `running` + `pg_isready` 活体断言不变（数据
      面回归锚保留）。digest bump 批次因此不再打红升级矩阵，同时滚动行为本身
      被钉进断言（不是放行）。
   bump 射程约束：digest bump 只吸收**同版本契约内的**上游刷新（patch 级、
   基座/目录布局/VOLUME 面不变）；major/suite 变更是新模板版本矩阵的事
   （ADR-0029 决策 2 口径），须独立 ADR。

5. **边界与诚实注记**：
   - 受管 Provider 镜像（traefik/zot/VL/VM/cadvisor/caddy）维持 tag 级钉
     （ADR-0021 口径）——DT-9 射程是数据面模板（用户数据被静默换镜像的风险
     面）；受管面是否推广 digest 钉随未来批次按需裁决，本 ADR 不承诺。
   - digest 钉不防 registry 侧删除（上游清退 digest 后重拉失败）；缓解路径
     （镜像入受管 zot 做平台镜像仓）超射程，记 runbook 侧运维事实。
   - e2e/CI 的镜像预拉通道（宿侧 pull → save|load 注入 dind）只暖**层**不暖
     **index**（save/load 不保留 RepoDigests，F1.11 坑实录同源）：digest 引用
     在 dind 内解析需 registry 可达一次（层已本地、零字节下载）。CI 有网常态
     无扰；纯离线环境 digest 引用拉不进新 index——诚实边界，预拉注释记档。
   - 备份/恢复工具容器同用 `tpl.Image()`，digest 钉定随模板单源覆盖备份链
     （ADR-0039 对模板的依赖面零改动）。

## 后果

- 下游断言面更新：engine 侧 database/backup 测试的镜像串改 digest 形态；
  swarm 层 translate/utility 测试用自有夹具串，不受影响。
- API/CLI/golden 零变化：模板回显面只有 version/port（ADR-0029 决策 2 注——
  Info 不含镜像），digest 不进任何回显面。
- `docker service ps` 等载体面显示的镜像引用变长（tag@digest 全形态）——可读性
  换确定性的既定取舍。
- staging 换装批（F2.9/F2.6/F2.7 一批）将首次在真机走"tag 钉 → digest 钉"的
  跨代配对：预期库任务各滚一次（受监督序），实录随换装批记 runbook。

## 执法补强（2026-10-05 评审批 P2-1）

N2 评审 P2-1 指出：决策 4 的 bump 射程约束（"VOLUME/目录布局面不变"）是承诺
不是执法——TestVolumeShadowContract 的预期表是硬编码快照，从不读镜像真实
VOLUME，digest bump 后上游改 VOLUME 声明该测试照绿（既有兜底只有 bump 批 CI
的 e2e-backup 任务替换存活锚，动态且滞后）。补强落地：

- **live 核对测试**（`TestVolumeShadowContractLive`，internal/engine/dbtemplate/
  dbtemplate_live_test.go）：经 Docker Hub registry API 匿名拉取面（token →
  钉定 index → 本平台 arch manifest → config blob）读五引擎钉定镜像的
  `config.Volumes` 键集，与 **volumeShadowTable**（唯一预期表）对账——上游
  VOLUME 面任何增删即红。repo/tag/digest 从 `tpl.Image()` 拆解取得（adapter
  钉定对的唯一暴露面 = 平台实际部署的引用），live 侧零重抄钉定值；表由
  TestVolumeShadowContract（静态面）与本测试（镜像真源面）同源消费，禁止
  第二份。
- **用法**：缺省 SKIP（env gate `FLEETLY_DBTEMPLATE_LIVE`，CI 零网络依赖
  不红）；本地与 **bump 批必跑**：

  ```sh
  FLEETLY_DBTEMPLATE_LIVE=1 go test ./internal/engine/dbtemplate/ -run TestVolumeShadowContractLive
  ```

  （需出网可达 registry-1.docker.io 一次，与决策 5 的 e2e index 解析同条件。）
  红了的处置：按决策 4 评估 bump 射程——patch 内不变量被破坏（VOLUME 面变化）
  = major 级变更，走版本矩阵独立 ADR（ADR-0029 决策 2 口径），**不许静默
  改表**。runbook 侧 bump 检查单同步记档（教训与边界节）。
- **首跑实录（执法价值的即时证明）**：2026-10-05 首跑即红一枚——mongo 钉定
  镜像另声明 `/data/configdb`（在 DataTarget `/data/db` 之外），静态表自
  建表以来从未对账过全集。该路径即 N2 评审 P2-4 匿名卷泄漏台账的镜像遗产面
  （每次任务替换铸一枚匿名卷、无数据丢失面）：表按实测全集记档，静态契约
  升级为"声明路径按与 DataTarget 关系三分派生"（精确重合 / 嵌套逃逸 /
  之外记档），五引擎 live 复跑全绿。

## 验收锚

- [x] 五引擎 digest 全钉：`Image()` 为 `tag@sha256:<64hex>` 形态、
  `ImageDigest()` 非空且与 Image 自洽（dbtemplate 测试门禁，全值域反扫）
  （TestImageDigestsPinned + TestTemplateFaces digest 列，2026-10-05）
- [x] 消费面零改动收编：投影/备份/恢复三处 `tpl.Image()` 调用点即 digest 引用
  （既有测试断言更新后全绿；无第二真源）（engine 侧 database/backup 断言面
  同批更新，mise test 三 module 全绿）
- [x] e2e 升级矩阵条件化零滚动断言落地：digest 集无差严格零滚动、有差恰一次
  滚动 + 活体断言（本批跨代配对 tag→digest 在本地 dind 实战走一遍）
  （2026-10-05 本地 dind：3def22e→本批 squash 配对——"digest set changed"
  分支实战触发，db task delta = 1 (budget 1)，pg_isready 活体、零 drift，
  UPGRADE PASSED；CI 常态随 e2e-upgrade job）
- [x] e2e backup 演练全绿：四引擎 digest 引用拉取 + 备份/verify/恢复数据断言
  不变（CI e2e-backup job 常态）（2026-10-05 本地 dind：四引擎 digest 拉取 +
  stream/preseed 恢复 + 平台备份 roundtrip + S3 离机腿 ALL DRILLS GREEN）
- [x] golden 零漂移：databases 面 CLI golden 不含 digest（回显面只有 version）
  （本批零 golden 改动即证；apitest Version 断言不变）
- [x] mise run test + lint 全绿；改散文后守卫 `go test -count=1 ./internal/guards/`
  （2026-10-05 全绿；golangci 0 issues + buf breaking 过 + 守卫含散文扩面
  ADR/checklist/e2e 注释全过）
- [x] VOLUME 契约 live 核对落地：五引擎钉定 index 实测 VOLUME 集与唯一
  预期表（volumeShadowTable，静态/live 两测试同源）对账，env-gated 缺省
  SKIP、bump 批必跑（TestVolumeShadowContractLive，2026-10-05 首跑五引擎
  全绿；首跑发现 mongo /data/configdb 未入表，按实测全集记档并升级静态
  契约为三分派生）

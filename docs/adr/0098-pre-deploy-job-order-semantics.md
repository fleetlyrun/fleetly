# ADR-0098：pre-deploy job 序语义——compose 受理面 after 承载 + 确定性拓扑排

日期：2026-11-05 立法（提案 `docs/proposals/2026-11-04-volume-carrier-keying-and-job-order.md` §3；用户裁定机制补全不打补丁）｜ 状态：Accepted（B2 批同批落地）。占号复核：0093-0096 被 F5 托管数据服务并行占用、0097 已用（B1），本篇占 0098。

## 背景

staging 重播批咬出**处女库冷启动死锁**（runbook 记录·六十一坑①）：F1.13 起 pre-deploy job 严格串行（job i 成功才铸 i+1，ADR-0030），而 composeintake 按服务名**字母序**收集 `fleetly.job: init` 方言 job——`db-bootstrap` 字典序先于 `migrate` 串行执行，db-bootstrap 的 5s×48 等待环等的是 migrate 迁移落库的角色，串行下 migrate 永远轮不到，游标卡死到 ttl 超窗。历史部署未踩到（割接期 dump/restore 预置 + 已迁移库上幂等重放）。当批操作解 = 手改冻结 Spec 的 `pre_deploy_jobs` 序走 `--spec-file` 重部署——绕过成功本身是诊断证据。

探明（排序真源）：

- **引擎吃声明序**：`drivePreDeployJobs` 按 `spec.GetPreDeployJobs()` 的 repeated 下标串行推进，全程无重排；`--spec-file` 重排一次过即实证。
- **唯一丢序点在 compose 受理面**：顶层 `x-fleetly-pre-deploy-jobs` 是列表，声明序保留；**服务 label 方言**（`fleetly.job: init`）的 job 集从 compose 服务 map 收集——map 本无序，compose-go `ServiceNames()` 恒按名字典序返回，声明序在 YAML map 处就已不存在（不是实现疏漏，是形态缺失）。

## 决策

1. **序的唯一平台承载 = spec repeated 位次**（零 proto 变更）：`pre_deploy_jobs` 列表位次即执行序（引擎已如此消费）。**不在 JobSpec 加依赖字段**——序语义已有承载，加字段 = 一语义双真源。spec_file 面继续以位次声明序（当批操作解的形态化）。
2. **compose 形态引入显式依赖承载 `after`**（受理语言面，非 spec 面）：
   - 顶层 job 条目键 `after`：单名字符串或名字列表（`command` 的 string/list 双形态同先例）；
   - label 方言 label `fleetly.job.after`：逗号分隔多名（`fleetly.databases` 同律）；
   - `after` 指名**同声明的其他 job**（跨形态可引：顶层 ↔ label 方言），受理时消费、**不进冻结体**——解析产物就是列表位次。
3. **确定性拓扑排**（受理咽喉单点）：自然序 = 现状收集序（顶层声明序在前，label 方言按服务名字典序在后）为基，Kahn 拓扑排按**最小自然序选拍**（同名次恒同输出，双跑确定性律 ADR-0090 同款要求）；**无任何 after 承载时自然序原样返回**——存量受理零漂移（corpus digest 逐字节不动，确定性不回退）。
4. **断环 fail-closed（E_INVALID_ARGUMENT）**：环即受理拒绝，经 `spec.ValidationError` 通道（`mapValidationError` 既有映射）——理由**字典序点名全部环参与者**（剩余集中沿 after 可自达者，自环含内）；after 引用未声明的 job 名、受理集内 job 重名（after 在场时先行，受理源可寻址）同拒。死锁形态从「ttl 超窗后的部署失败」提前为「受理即拒」。
5. **陈旧拒绝面文案回正**：job 方言 `depends_on` 拒绝文案的「jobs run in parallel」自 F1.13 串行化起即失真——改为指向 after 承载；顶层 job 条目的 `depends_on` 从泛化「unsupported field」升为同款指向性拒绝。

## 后果

- 处女库冷启动死锁由构造消除：等待环依赖经 `after` 声明进受理序，migrate 先铸先完成，db-bootstrap 一次过（不再依赖割接期预置或 `--spec-file` 手改绕过）。
- compose 无 `after` 的存量声明行为零变化（自然序 = 今日字典序/列表序）；`after` 是纯增量受理面（corpus 存量 49 样本 digest 逐字节零漂移实证）。
- label 方言 job 的执行序不再隐含等于字典序——声明者对序负责（`after` 显式化），平台对确定性负责（拓扑排 + 最小自然序选拍）。
- 落地形态：pre-deploy job 受理面整体拆出 `internal/application/composeintake/jobs.go`（intake.go 触 1200 行 size 预算守卫；包内既有 genproto 引用点的文件位移动，ADR-0088 O4 基线同 commit +1 条目机械刷新——**包面 genproto 表面零变化**，受理喉仍是 FromYAML 单源）。
- 债与后手：spec_file 面如需依赖式声明（位次之外的 Ergonomics），另行提案——本篇裁决序真源在位次，依赖承载只在无序形态（compose map）存在。

## 验收锚

- [x] intake 单测：自然序钉死（label 方言字典序 / 顶层在前）；after 承载两形态（label 逗号分隔、顶层 string/list）重排；跨形态引用；链式依赖四跑恒定。
- [x] intake 拒绝面单测：未知引用 / 环（自环 + 双环 + 指名参与者）/ after 在场时重名受理源先行 / 载体形状执法 / depends_on 两形态指向性文案。
- [x] corpus：正样本（staging 死锁形：db-bootstrap after migrate 字典序反转）+ 负样本（双 label 互引成环）登记，`-update` 同批；存量条目零漂移。
- [x] apitest：重播死锁形复现——双 label 方言 job 字典序自然死锁形，`fleetly.job.after` 后 migrate 先铸（首锚 ensure 命令断言）、db-bootstrap 次铸、一次过到 succeeded、done 游标清锚。
- [x] 全套门禁：`mise run test` 三 module -race + lint:go + lint:proto（守卫全集）绿。

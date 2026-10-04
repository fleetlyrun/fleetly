# ADR-0043: SharedVariable 实体与两级合成细则（ADR-0027 收口）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-05 | ADR-0027（合成时机裁决，本 ADR 收口其留白细则）、ADR-0002（Revision 冻结/回放唯一实现）、CONTEXT.md Variable 词条（词汇冻结）、F1.5（Variable=env 直传形态落地）、F2.9（checklist） |

## 背景

F2.9 落地两级变量合成。ADR-0027 已钉死合成时机（**归一化期合成，Revision 冻结最终形态**），
但把五件细则显式留给本批裁决：①变量值进不进 Revision 明文（ADR-0027 后果节留的隐私口径
口子，"细则随 F1.x Variable proto 评审定"）；②App 级 Variable 的形态（F1.5 落地的是 env
直传，是否升格为 CRUD 实体）；③合成算法与作用面（哪些进程、哪个咽喉点）；④受影响 App
提示的计算口径；⑤SharedVariable 实体面的 CRUD/回显/配额/事件形态。

## 决策

1. **值进 Revision 明文（收口 ADR-0027 隐私口径口子）**：变量的非敏感契约在 F1.5 已由
   proto 注释钉死（`spec.proto` env "非敏感环境变量"；敏感值走 secret_refs/Secrets）——
   分界不因两级合成改变。合成后的最终生效 env 集**含值**冻结进 Revision：这是案 (a) 的
   唯一自洽形态（Drift 对照/Revision diff/Replay 回放共用同一真源，值缺席即掏空）。
   SharedVariable 值同口径：**可回显**（List 返回值，Configs 形态；非 Secrets 指纹形态）。

2. **App 级 Variable = 部署源 env 直传，不立 CRUD 实体**：两级中的 App 层就是部署源里
   声明的 per-process env——compose 的 `environment` 既有；本批补齐 image/upload 形态的
   `DeployRequest.env`（`map<string,string>`，CLI `--env KEY=VALUE` 可重复，tasks create
   同款）。理由：部署源是 App 声明的单一来源（归一化两源统一 AppSpec 的既定模型）；App 级
   变量另立存储会造出第二真源，且改它同样要重部署才生效（合成在归一化期），无独立 CRUD
   收益。compose 形态携带 `env` 拒绝（compose 自带 environment 声明面；与 http_probe 旗标
   同款先例）。新增面（DeployRequest.env 键、SharedVariable 名）按 POSIX env 键形态校验
   （`[A-Za-z_][A-Za-z0-9_]*`，≤64 字符）；存量 compose env 键不追溯（行为面不回撕）。

3. **合成算法 = flat union，咽喉点 = freezeRevision**：SharedVariable 层在下、App 层
   per-process env 覆盖（`spec.MergeSharedEnv` 纯函数，spec 叶子包）；作用于 AppSpec 内
   **全部进程**（Processes + FirstBootJobs 的 Process——迁移 job 同样要拿共享连接串）。
   合成点收在 `freezeRevision`（Deploy 三源与 webhook git 源共用的唯一冻结咽喉）——
   任何路径冻结出的 Revision 都是合成后最终形态。确定性：protojson 规范序列化 map 按键
   排序，同 Source 同变量状态两次归一化逐字节相等（内容寻址复用直接命中）。
   **Task 不参与**：Task 是运行时实体（显式 env、无 Revision 冻结面），合成是部署链语义
   ——边界显式记录，非缺口。

4. **受影响 App 提示 = 近似口径（诚实命名）**：project 内有 ≥1 Revision 的 App，其最新
   Revision 任一进程 `env[KEY]` 与变更后状态不同（缺席亦算——重冻结会补进）即列入
   `affected_apps`。App 层覆盖同键的 App 会误报：冻结形态是扁平 map，无键来源账本；为
   提示建账本是契约污染（提示是 DX 附注不是行为承诺，误报代价 = 一次幂等重部署）。提示在
   Put/Delete 响应与 CLI 输出；不触发任何自动重部署（ADR-0027 既裁）。

5. **实体面 = SharedVariablesService（structure 上下文，scope 资源 `shared_variables`）**：
   - `PutSharedVariable`（upsert；幂等面纳入——Put 前缀创建型动词）、
     `ListSharedVariables`（值回显；after_name+limit 惯例）、`DeleteSharedVariable`（软删，
     Secrets 同款 tombstone）。行级授权（ADR-0035 authorizeProjectID）与父项目存活校验
     同族面（Secrets/Configs）。
   - 配额：per-Project 100 条 + 单值 16KB（Configs 100/256KB 先例；env 值面比文件面小
     一个量级）。事件 `variable.updated`/`variable.deleted`（secret.* 同款三链）。
   - 冻结封禁面纳入 Put/Delete（project_id 链）；member 角色写面纳入（部署材料，与
     secrets/configs 同桶）。项目删除不级联清理共享变量——与既有库材料（secrets 等）同
     挂账口径（F2.8 runbook 记录·五），不在本批翻案。

## 后果

- 部署期之外唯一改运行 env 的路径消失（改共享变量必须重部署，行为可解释、可 diff）。
- Revision 明文含共享变量值：与 compose env 既有形态一致（备份含 SQLite 即含值）；
  敏感值的保护边界继续由 Secret 分界承载（ADR-0014 不动）。
- 受影响提示是近似集：文档与响应文案明示"may need a redeploy"。

## 验收锚

- [x] 同 Source 同变量状态两次归一化逐字节相等（digest 相等，内容寻址复用命中——性质测试）〔spec TestMergeSharedEnvByteDeterminism + apitest TestSharedVariableMergeAtNormalization〕
- [x] App 层覆盖同键生效（compose environment 与 DeployRequest.env 两形态各有测试）〔TestSharedVariableComposeOverride / TestSharedVariableMergeAtNormalization〕
- [x] FirstBootJobs 进程参与合成（job env 同享共享层）〔TestMergeSharedEnvLayers〕
- [x] 改/删 SharedVariable 后既有 Revision 不变且无新部署行（行为不变钉死）〔TestSharedVariableChangeNoRedeployButAffectedHint / TestSharedVariableDelete〕
- [x] Put/Delete 响应含 affected_apps（近似口径：覆盖误报可接受，文案记录）〔apitest 断言 + golden 双形态（put 提示两 App、delete 提示一 App 的近似集形态在 golden 可见）〕
- [x] 守卫全绿：幂等面含 PutSharedVariable、冻结面含 Put/Delete、scope 词表含
      shared_variables、CLI 新动词双形态 golden、事件名入册〔variable.updated/deleted 三链（eventcode + schemareg + golden）同 commit〕

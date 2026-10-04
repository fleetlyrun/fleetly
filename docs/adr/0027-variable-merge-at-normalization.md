# ADR-0027: 两级变量归一化期合成（Revision 冻结最终形态）

| 状态 | 日期 | 关联 |
|---|---|---|
| Accepted | 2026-10-01 | R-5 裁决（批 0.5）、ADR-0002（Revision 冻结/回放唯一实现）、ADR-0011（环境即 Project）、CONTEXT.md Variable 词条 |
| 实施收口 | 2026-10-05 | F2.9 落地；细则裁决见 **ADR-0043**（隐私口径/App 级形态/合成算法/受影响提示/实体面五件留白收口） |

**排期**：功能清单 F2.9（N1 诚实挂账收口批）。现状 = F1.5 的 Variable 为 env 直传形态（两级实体未建）；SharedVariable 实体与归一化期合成落地时闭本 ADR 验收锚。〔2026-10-05 收口：合成咽喉 = freezeRevision（Deploy 三源 + webhook git 源共用），`spec.MergeSharedEnv` 纯函数作用于全部进程（含 firstBootJobs）；后果节的隐私口径口子由 ADR-0043 决策 1 收口（值随最终生效集明文冻结——非敏感契约不变，敏感值继续走 Secrets）〕

## 背景

两级变量（Project 级 SharedVariable 在下、App 级 Variable 覆盖）的合成时机两案：**(a) 归一化期合成**——Revision 冻结最终形态；**(b) Ensure 前动态解析**——改共享变量即时生效但 Revision 不再是行为真源，Drift/diff 语义被掏空。`spec.proto:67-68` 注释押注 (a) 但代码未写，N1 引入 Variable 前必须显式裁决。

## 决策

**归一化期合成（案 a）**：Variable 与 SharedVariable 在归一化时合成最终生效集进 AppSpec，Revision 冻结的即最终形态——与 ADR-0002"基线永远以 Revision 为准"一致；Drift spec 对照、Revision diff、Replay 回滚共用同一真源。改共享变量需重部署才生效（可解释、可 diff）。

**DX 补偿**：改 SharedVariable 时 API 响应 / CLI 输出提示受影响 App 列表（哪些 App 需重部署取新值）。

## 后果

- 改共享变量不触发任何自动重部署（显式操作语义维持）。
- 变量值不进 Revision 明文的隐私口径：Revision 只冻结合成后的键与来源指纹（细则随 F1.x Variable proto 评审定，本 ADR 只钉合成时机）。

## 验收锚

- [x] Revision 内容 = 最终生效变量集（性质测试：同 Source 同变量状态两次归一化逐字节相等）〔TestMergeSharedEnvByteDeterminism + TestSharedVariableMergeAtNormalization（内容寻址复用命中——仍只一条 Revision）〕
- [x] 改 SharedVariable 后未重部署的 App 行为不变（测试钉死）〔TestSharedVariableChangeNoRedeployButAffectedHint：Revision 集不变 + 部署行数不变 + 冻结值仍旧值〕
- [x] 改 SharedVariable 响应含受影响 App 提示〔Put/Delete 响应 affected_apps；近似口径细则随 ADR-0043 决策 4〕

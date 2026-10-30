// 确认面风险分级（B4a，docs/design/2026-10-30-zeabur-oss-borrowing.md §4）：
// 纯函数模块——触发面 × 影响面 → 确认弹窗形态档。借鉴 zeabur 的 risk
// 分级形态，档位语义按 fleetly 收敛模型定形（「抄机制不抄赌注」）：
// 确认弹窗（ConfirmDialog）按档变形，档位即弹窗形态契约——
//   standard        常规窄框（默认形态）；
//   wide            宽框 + 「后果先行」段（级联/保留清单披露，B1 Teardown
//                   预览联动预留）；
//   type_to_confirm 输入资源名（或等价确认词）才激活确认钮——仅 Project
//                   删除与平台级操作。
// 本模块是分级规则的唯一真源：消费点按动作语义取轴值，不各自跳档。

import type { ReactNode } from "react";

/** 触发面：动作锚定的对象层级。 */
export type RiskScope = "resource" | "project" | "platform";

/** 影响面：动作落定的波及范围。 */
export type RiskBlast = "single" | "cascade" | "platform";

/** 三档形态（弹窗形态契约，见 ConfirmDialog）。 */
export type RiskTier = "standard" | "wide" | "type_to_confirm";

/** 分级入参：纯值类型（触发面 × 影响面）。 */
export interface RiskInput {
  scope: RiskScope;
  blast: RiskBlast;
}

// 确认弹窗的 risk 元数据形状（判别联合：编译期执法「档位携带档位专属
// 元数据」——wide 必带后果段文案，type_to_confirm 必带确认串）。
// B1 W4：wide 档 consequences 放宽为 ReactNode——拆除面由 PreviewTeardown
// 载荷组结构化清单（级联/保留 ul）；字符串调用点零感（Node ⊇ string）。
// type-only import，运行时纯度不破。
export type ConfirmRisk =
  | { tier: "standard" }
  | { tier: "wide"; consequences: ReactNode }
  | { tier: "type_to_confirm"; confirmName: string };

// 分级规则（唯一真源）：
// - type_to_confirm：Project 级与平台级动作——触发面上移即最重档（受管
//   组件 Restart 归平台级；集群凭证全量轮换同理）；影响面打到平台可用性
//   的动作同样入此档。
// - wide：资源级但有级联影响面——删 App（载体 + hook/route 行级联）、
//   删 Database（数据语义：卷保留、同名重建复用）、节点 drain（承载
//   workload 实迁移）。
// - standard：单资源破坏动作——Revoke token/relay、Delete route/secret/
//   variable、Stop task/run、凭证轮换等。
export function riskTier(input: RiskInput): RiskTier {
  if (input.scope === "project" || input.scope === "platform" || input.blast === "platform") {
    return "type_to_confirm";
  }
  if (input.blast === "cascade") {
    return "wide";
  }
  return "standard";
}

// type_to_confirm 档的输入匹配：键入串与期望确认串（资源名或等价确认词）
// trim 后大小写不敏感地精确相等才放行。取舍说明：大小写不敏感降低「照着
// 抄还抄错」的挫败（GitHub 同款口径），空白裁边吸收输入法尾随空格；
// 刻意不做子串/前缀匹配——那是打字机安全垫，挡不住误确认。
export function confirmNameMatches(typed: string, expected: string): boolean {
  return typed.trim().toLowerCase() === expected.trim().toLowerCase();
}

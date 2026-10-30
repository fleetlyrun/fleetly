import { describe, expect, it } from "vitest";
import { confirmNameMatches, riskTier } from "./risk";

// risk 分级模型单测（B4a，docs/design/2026-10-30-zeabur-oss-borrowing.md §4）：
// 分级规则唯一真源 riskTier 的分组用例（触发面 × 影响面 → 三档），外加
// type_to_confirm 档 confirmName 匹配口径的单独组。消费点的档位赋值以
// 本表为规格——改档先改这里的用例。

describe("riskTier：type_to_confirm（Project 级与平台级动作）", () => {
  it("Project 删除（scope=project）→ type_to_confirm", () => {
    expect(riskTier({ scope: "project", blast: "cascade" })).toBe("type_to_confirm");
  });

  it("受管组件 Restart（scope=platform，平台可用性受波及）→ type_to_confirm", () => {
    expect(riskTier({ scope: "platform", blast: "platform" })).toBe("type_to_confirm");
  });

  it("集群凭证全量轮换（scope=platform）→ type_to_confirm", () => {
    expect(riskTier({ scope: "platform", blast: "single" })).toBe("type_to_confirm");
  });

  it("影响面打到平台可用性即入最重档（blast=platform 压过 scope=resource）", () => {
    expect(riskTier({ scope: "resource", blast: "platform" })).toBe("type_to_confirm");
  });
});

describe("riskTier：wide（资源级 + 级联影响面）", () => {
  it("删 App（载体 + hook/route 行级联）→ wide", () => {
    expect(riskTier({ scope: "resource", blast: "cascade" })).toBe("wide");
  });

  it("删 Database（数据语义：卷保留、同名重建复用）→ wide", () => {
    expect(riskTier({ scope: "resource", blast: "cascade" })).toBe("wide");
  });

  it("节点 drain（承载 workload 实迁移）→ wide", () => {
    expect(riskTier({ scope: "resource", blast: "cascade" })).toBe("wide");
  });
});

describe("riskTier：standard（单资源破坏动作）", () => {
  it("Revoke token / Delete route / Delete secret / Delete variable → standard", () => {
    expect(riskTier({ scope: "resource", blast: "single" })).toBe("standard");
  });

  it("Revoke relay / Stop task / Stop run / 删 schedule / 凭证轮换 → standard", () => {
    expect(riskTier({ scope: "resource", blast: "single" })).toBe("standard");
  });
});

describe("riskTier：轴覆盖穷尽（三档恰为全量值域）", () => {
  it("3×3 轴组合只产出三档之一，且各档落点与规则一致", () => {
    const tiers = new Set(
      (["resource", "project", "platform"] as const).flatMap((scope) =>
        (["single", "cascade", "platform"] as const).map((blast) => riskTier({ scope, blast })),
      ),
    );
    expect(tiers).toEqual(new Set(["standard", "wide", "type_to_confirm"]));
  });

  it("resource 轴三影响面按 single/cascade/platform → standard/wide/type_to_confirm 递进", () => {
    expect(riskTier({ scope: "resource", blast: "single" })).toBe("standard");
    expect(riskTier({ scope: "resource", blast: "cascade" })).toBe("wide");
    expect(riskTier({ scope: "resource", blast: "platform" })).toBe("type_to_confirm");
  });
});

describe("confirmNameMatches：type_to_confirm 的输入门", () => {
  it("精确相等放行（含大小写不敏感）", () => {
    expect(confirmNameMatches("Traefik", "Traefik")).toBe(true);
    expect(confirmNameMatches("traefik", "Traefik")).toBe(true);
    expect(confirmNameMatches("TRAEFIK", "Traefik")).toBe(true);
  });

  it("首尾空白裁边后比对（输入法尾随空格不卡人）", () => {
    expect(confirmNameMatches("  traefik ", "Traefik")).toBe(true);
    expect(confirmNameMatches("traefik", " TraefIK ")).toBe(true);
  });

  it("非精确相等一律拦下（空串/子串/前缀/他词）", () => {
    expect(confirmNameMatches("", "Traefik")).toBe(false);
    expect(confirmNameMatches("Trae", "Traefik")).toBe(false);
    expect(confirmNameMatches("Traefik extra", "Traefik")).toBe(false);
    expect(confirmNameMatches("zot", "Traefik")).toBe(false);
  });

  it("内部空白不归一（只裁首尾——挡住「看似输入了」的宽松匹配）", () => {
    expect(confirmNameMatches("trae fik", "Traefik")).toBe(false);
  });
});

import { describe, expect, it } from "vitest";
import { parseHash } from "./router";

// 路由 v2 解析锚：平坦路由、参数段（部署详情）、未知/空回退默认页。

describe("parseHash", () => {
  it("parses flat routes", () => {
    expect(parseHash("#/deployments")).toEqual({ page: "deployments", detailId: "" });
    expect(parseHash("#/logs")).toEqual({ page: "logs", detailId: "" });
    expect(parseHash("#settings")).toEqual({ page: "settings", detailId: "" });
  });
  it("parses the deployment detail parameter", () => {
    expect(parseHash("#/deployments/01M4ABC")).toEqual({ page: "deployments", detailId: "01M4ABC" });
  });
  it("falls back to deployments for empty or unknown hashes", () => {
    expect(parseHash("")).toEqual({ page: "deployments", detailId: "" });
    expect(parseHash("#/")).toEqual({ page: "deployments", detailId: "" });
    expect(parseHash("#/nonsense")).toEqual({ page: "deployments", detailId: "" });
  });
  it("ignores deeper segments beyond the first parameter", () => {
    expect(parseHash("#/deployments/01M4/extra")).toEqual({ page: "deployments", detailId: "01M4" });
  });
});

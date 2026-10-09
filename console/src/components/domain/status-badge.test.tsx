import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  StatusBadge,
  alertTone,
  deploymentTone,
  isActiveDeploymentState,
  nodeTone,
} from "./status-badge";

// 状态色单源守卫：值域映射只在此处，徽章形态全站一致（UI v2 契约）。
describe("status-badge", () => {
  it("maps deployment states to semantic tones", () => {
    expect(deploymentTone("succeeded")).toBe("success");
    expect(deploymentTone("failed")).toBe("danger");
    expect(deploymentTone("canceled")).toBe("warning");
    expect(deploymentTone("queued")).toBe("neutral");
    for (const active of ["running", "observing", "releasing", "preparing"]) {
      expect(deploymentTone(active)).toBe("info");
    }
  });

  it("marks active deployment states (cancelable window)", () => {
    expect(isActiveDeploymentState("observing")).toBe(true);
    expect(isActiveDeploymentState("succeeded")).toBe(false);
  });

  it("maps node availability (W2 wording: unavailable is history, not failure)", () => {
    expect(nodeTone("available")).toBe("success");
    expect(nodeTone("cordon")).toBe("warning");
    expect(nodeTone("drain")).toBe("warning");
    expect(nodeTone("unavailable")).toBe("neutral");
  });

  it("maps alert states", () => {
    expect(alertTone("firing")).toBe("danger");
    expect(alertTone("ok")).toBe("success");
  });

  it("falls back to neutral for unknown values (no guessed semantics)", () => {
    expect(deploymentTone("warp-speed")).toBe("neutral");
    expect(deploymentTone(undefined)).toBe("neutral");
  });

  it("renders with pulse only for live states", () => {
    render(<StatusBadge tone="info" pulse>running</StatusBadge>);
    const badge = screen.getByText("running").closest("span")!;
    expect(badge.querySelector("span.animate-pulse")).toBeTruthy();
  });
});

import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import {
  DeployPreviewPanel,
  actionLabelOf,
  diffPreviews,
  groupWarnings,
  previewDeltaIsEmpty,
  rolloutBadgeOf,
} from "./deploy-preview";
import type {
  PreviewDeployPayload,
  PreviewDiffEntry,
  PreviewWarning,
  PreviewWorkloadChange,
} from "@/kit/deployments";

// DeployPreviewPanel 与其纯函数半边（B1 W4 预览步载荷渲染，设计稿 §1.3/
// §1.6）：分组/徽章/增量 diff 是纯函数单测锚；面板渲染钉三件事——
// rollout 徽章逐 Workload 在场、warnings 分组含 LIVE_DRIFT 与
// IN_FLIGHT_DEPLOYMENT 的指引文案、增量块（added/dropped 两组）。
// 载荷形状对齐 v1PreviewDeployResponse（apitest/preview goldens 同口径）。

afterEach(() => {
  cleanup();
});

const change = (overrides: Partial<PreviewWorkloadChange> = {}): PreviewWorkloadChange => ({
  workload_id: "A1-web",
  action: "WORKLOAD_CHANGE_ACTION_CHANGED",
  image: "nginx:1.27",
  replicas_from: "1",
  replicas_to: "3",
  rollout: "WORKLOAD_ROLLOUT_SCALE_ONLY",
  ...overrides,
});

const warning = (kind: string, detail: string): PreviewWarning => ({ kind: kind as PreviewWarning["kind"], detail });

const diffEntry = (path: string, oldValue?: string, newValue?: string): PreviewDiffEntry => ({
  path,
  old_value: oldValue,
  new_value: newValue,
});

const payload = (overrides: Partial<PreviewDeployPayload> = {}): PreviewDeployPayload => ({
  spec_diff: [diffEntry("processes.web.image", "nginx:1.27", "nginx:1.31")],
  workload_changes: [
    change(),
    change({ workload_id: "A1-worker", action: "WORKLOAD_CHANGE_ACTION_ADDED", rollout: "WORKLOAD_ROLLOUT_SCALE_ONLY", replicas_from: "0" }),
    change({ workload_id: "A1-old", action: "WORKLOAD_CHANGE_ACTION_REMOVED", rollout: "WORKLOAD_ROLLOUT_REPLACE", replicas_to: "0" }),
  ],
  warnings: [
    warning("PREVIEW_WARNING_KIND_LIVE_DRIFT", 'live replicas 2 for process "web" differ from the declared 1'),
    warning("PREVIEW_WARNING_KIND_IN_FLIGHT_DEPLOYMENT", "0 in-flight and 1 queued deployment(s) exist"),
    warning("PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT", "1 pre-deploy job(s) will run"),
  ],
  ticket: "tkt-1",
  ticket_expires_in: 600,
  ...overrides,
});

describe("rolloutBadgeOf / actionLabelOf", () => {
  it("maps the three rollout verdicts and degrades unknown values honestly", () => {
    expect(rolloutBadgeOf("WORKLOAD_ROLLOUT_ZERO_TOUCH").label).toBe("zero-touch");
    expect(rolloutBadgeOf("WORKLOAD_ROLLOUT_SCALE_ONLY").label).toBe("scale-only");
    expect(rolloutBadgeOf("WORKLOAD_ROLLOUT_REPLACE").label).toBe("replace");
    expect(rolloutBadgeOf(undefined).label).toBe("unknown");
    expect(rolloutBadgeOf("SOMETHING_NEW").label).toBe("unknown");
    expect(actionLabelOf("WORKLOAD_CHANGE_ACTION_ADDED")).toBe("add");
    expect(actionLabelOf("WORKLOAD_CHANGE_ACTION_REMOVED")).toBe("remove");
    expect(actionLabelOf("WORKLOAD_CHANGE_ACTION_CHANGED")).toBe("change");
  });
});

describe("groupWarnings", () => {
  it("groups by kind in first-appearance order and keeps per-kind guidance", () => {
    const groups = groupWarnings([
      warning("PREVIEW_WARNING_KIND_LIVE_DRIFT", "d1"),
      warning("PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT", "d2"),
      warning("PREVIEW_WARNING_KIND_LIVE_DRIFT", "d3"),
    ]);
    expect(groups.map((group) => group.key)).toEqual([
      "PREVIEW_WARNING_KIND_LIVE_DRIFT",
      "PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT",
    ]);
    expect(groups[0].items).toHaveLength(2);
    // 指引文案在场是规格点（§1.3：LIVE_DRIFT/IN_FLIGHT_DEPLOYMENT 必带指引）。
    expect(groups[0].guidance).toMatch(/converges the cluster back to the declared count/);
    const inflight = groupWarnings([warning("PREVIEW_WARNING_KIND_IN_FLIGHT_DEPLOYMENT", "x")])[0];
    expect(inflight.guidance).toMatch(/stale this ticket/);
    // 未知 kind：回退原始枚举词（服务端新增 kind 不吞）。
    const unknown = groupWarnings([warning("PREVIEW_WARNING_KIND_SOMETHING", "y")])[0];
    expect(unknown.label).toBe("PREVIEW_WARNING_KIND_SOMETHING");
  });
});

describe("diffPreviews（重预览增量）", () => {
  it("partitions workloads / diff paths / warnings into added and dropped", () => {
    const seen = payload();
    const next = payload({
      spec_diff: [diffEntry("processes.web.image", "nginx:1.27", "nginx:1.32"), diffEntry("processes.web.replicas", "1", "3")],
      workload_changes: [
        change({ image: "nginx:1.32" }), // 同 workload：无增删
        change({ workload_id: "A1-new", action: "WORKLOAD_CHANGE_ACTION_ADDED" }),
      ],
      warnings: [
        warning("PREVIEW_WARNING_KIND_PRE_DEPLOY_JOBS_PRESENT", "1 pre-deploy job(s) will run"), // 同款留存
        warning("PREVIEW_WARNING_KIND_BLUE_GREEN_WINDOW", "blue-green window"),
      ],
      ticket: "tkt-2",
    });
    const delta = diffPreviews(next, seen);
    expect(delta.workloadsAdded.map((item) => item.workload_id)).toEqual(["A1-new"]);
    expect(delta.workloadsDropped.map((item) => item.workload_id)).toEqual(["A1-worker", "A1-old"]);
    expect(delta.diffAdded.map((item) => item.path)).toEqual(["processes.web.replicas"]);
    expect(delta.diffDropped.map((item) => item.path)).toEqual([]);
    expect(delta.warningsAdded.map((item) => item.kind)).toEqual(["PREVIEW_WARNING_KIND_BLUE_GREEN_WINDOW"]);
    expect(delta.warningsDropped.map((item) => item.kind)).toEqual([
      "PREVIEW_WARNING_KIND_LIVE_DRIFT",
      "PREVIEW_WARNING_KIND_IN_FLIGHT_DEPLOYMENT",
    ]);
    expect(previewDeltaIsEmpty(delta)).toBe(false);
  });

  it("identical payloads produce an empty delta (ticket-refresh-only re-preview)", () => {
    const same = payload();
    const delta = diffPreviews(same, payload());
    expect(previewDeltaIsEmpty(delta)).toBe(true);
  });

  it("missing collections are treated as empty (protojson 缺席形态)", () => {
    const delta = diffPreviews({ ticket: "t" }, payload());
    expect(delta.workloadsDropped).toHaveLength(3);
    expect(delta.workloadsAdded).toHaveLength(0);
  });
});

describe("DeployPreviewPanel 渲染", () => {
  it("renders rollout badges per workload, warning groups with guidance, and the diff summary", () => {
    render(<DeployPreviewPanel payload={payload()} />);
    // rollout 徽章词逐 Workload 在场（replace/scale-only 等）。
    expect(screen.getAllByText("scale-only").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("replace")).toBeTruthy();
    // diff 条目 + path。
    expect(screen.getByText(/processes\.web\.image/)).toBeTruthy();
    // 警示分组词 + 两条规格点名指引。
    expect(screen.getByText("Live drift")).toBeTruthy();
    expect(screen.getByText(/converges the cluster back to the declared count/)).toBeTruthy();
    expect(screen.getByText("In-flight deployments")).toBeTruthy();
    expect(screen.getByText(/stale this ticket/)).toBeTruthy();
    // 票 TTL 提示（600s → ~10 min）。
    expect(screen.getByText(/Ticket valid for ~10 min/)).toBeTruthy();
  });

  it("renders the incremental block only when a delta is passed", () => {
    const seen = payload();
    const next = payload({
      workload_changes: [change(), change({ workload_id: "A1-brand-new", action: "WORKLOAD_CHANGE_ACTION_ADDED" })],
      ticket: "tkt-2",
    });
    const delta = diffPreviews(next, seen);
    render(<DeployPreviewPanel payload={next} delta={delta} />);
    expect(screen.getByText("Since the preview you last saw")).toBeTruthy();
    expect(screen.getByText(/New in this preview/)).toBeTruthy();
    expect(screen.getByText(/No longer in this preview/)).toBeTruthy();
    expect(screen.getByText(/add A1-brand-new/)).toBeTruthy();
    expect(screen.getByText(/remove A1-old/)).toBeTruthy();
  });

  it("renders honest empty states for a no-op preview", () => {
    render(<DeployPreviewPanel payload={{ ticket: "t", ticket_expires_in: 600 }} />);
    expect(screen.getByText(/No workload changes — the deploy writes no carriers/)).toBeTruthy();
    expect(screen.getByText(/No field-level changes/)).toBeTruthy();
  });
});

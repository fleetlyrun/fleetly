import { useEffect, useMemo, useRef, useState } from "react";
import { useCommand, useRecord } from "@lynxland/uikit-core";
import type { ApiError, CommandResult } from "@lynxland/uikit-core/kernel";
import { SlidersHorizontalIcon } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { Button } from "@/components/ui/button";
import {
  dataEnginesResource,
  DATA_ENGINES_RECORD_ID,
  DATA_ENGINES_STALE_MS,
  knobValuesEqual,
  type KnobValues,
} from "@/kit/data-engines";
import {
  databasesResource,
  type DatabaseRow,
  type PreviewUpdateDatabasePayload,
  type UpdateDatabaseInput,
  type UpdateDatabasePayload,
} from "@/kit/databases";
import { describeApiError, isConfirmationMismatch, isConfirmationStale } from "@/kit/errors";
import { KnobClassBadge, KnobFields, ResourcesFields } from "./knob-fields";

// Settings tab 的可编辑旋钮/资源面（B3，design §3.5/§6.2；Update 双车道 =
// B2d ADR-0095 决策 5/9）：目录驱动表单（KnobFields 同块，Engine Catalog
// 取值）+「Save changes」走 ConfirmableVerbs 预览/确认流（DeploySheet 先
// 例）：Save = PreviewUpdateDatabase（diff + 车道判定 + 滚动后果 + 铸票）→
// ConfirmDialog（standard 档——settings 变更档位，design §3.5）按滚动后果
// 分级披露：replace（含 rolling 键或资源变更）= 「将触发滚动重启」+ 分次
// 保存即分次重启提示（红队 A12/A14 记档项）；zero_touch（instant 全集）=
// 「即时生效」→ 确认带票发 UpdateDatabase（票绑预览时的输入快照——确认的
// 正是展示的）。STALE/MISMATCH（票过期 / 预览后行被改）→ 自动重预览，确认
// 仍须用户再按。
//
// 载荷只携改动面：settings/resources 各自「改了才在场」——未动的面缺席 =
// 服务端以行当前值织进票面摘要（他方在预览后改动未动面即 MISMATCH，不被
// 本次保存静默覆盖）。

// SettingsDraft 是表单期望态（资源轴空串 = 无限制）。
export interface SettingsDraft {
  settings: KnobValues;
  cpuMillis: string;
  memoryMb: string;
}

// draftOf：行 → 表单期望态（缺席资源轴/0 = 空串 = 无限制）。
export function draftOf(row: DatabaseRow | undefined): SettingsDraft {
  return {
    settings: { ...(row?.settings ?? {}) },
    cpuMillis: row?.resources?.cpu_millis ? String(row.resources.cpu_millis) : "",
    memoryMb: row?.resources?.memory_mb ? String(row.resources.memory_mb) : "",
  };
}

// 资源轴归一（"" 与 "0" 同为无限制）。
function axisKey(value: string): string {
  const trimmed = value.trim();
  return trimmed === "" || Number(trimmed) === 0 ? "" : String(Math.trunc(Number(trimmed)));
}

function resourcesEqual(a: SettingsDraft, b: SettingsDraft): boolean {
  return axisKey(a.cpuMillis) === axisKey(b.cpuMillis) && axisKey(a.memoryMb) === axisKey(b.memoryMb);
}

export function draftsEqual(a: SettingsDraft, b: SettingsDraft): boolean {
  return knobValuesEqual(a.settings, b.settings) && resourcesEqual(a, b);
}

// updateInputFor：基线 vs 草稿 → Update 输入（只携改动面；无改动 = null）。
export function updateInputFor(id: string, base: SettingsDraft, draft: SettingsDraft): UpdateDatabaseInput | null {
  const settingsChanged = !knobValuesEqual(base.settings, draft.settings);
  const resourcesChanged = !resourcesEqual(base, draft);
  if (!settingsChanged && !resourcesChanged) return null;
  return {
    id,
    ...(settingsChanged ? { settings: draft.settings } : {}),
    ...(resourcesChanged ? { resources: { cpuMillis: draft.cpuMillis, memoryMb: draft.memoryMb } } : {}),
  };
}

// rolloutRestarts：滚动后果判定（zero_touch = 不重启；其余一律按重启披露
// ——未知值保守披露，不把可能的重启说成即时生效）。
export function rolloutRestarts(payload: PreviewUpdateDatabasePayload): boolean {
  return payload.rollout !== "zero_touch";
}

// 行期望态指纹（settings 键序归一 + 资源轴）：useMemo 依赖锚——轮询刷新
// 同内容行不换基线，status 推进（滚动中）不扰动表单。
function draftKey(row: DatabaseRow | undefined): string {
  const draft = draftOf(row);
  const settings = Object.fromEntries(Object.entries(draft.settings).sort(([a], [b]) => a.localeCompare(b)));
  return JSON.stringify({ settings, cpuMillis: draft.cpuMillis, memoryMb: draft.memoryMb });
}

function describeResources(resources: { cpu_millis?: number; memory_mb?: number } | undefined): string {
  const cpu = resources?.cpu_millis ? `${resources.cpu_millis} millicores` : "unlimited";
  const memory = resources?.memory_mb ? `${resources.memory_mb} MiB` : "unlimited";
  return `cpu ${cpu}, memory ${memory}`;
}

function updateToast(payload: UpdateDatabasePayload): string {
  const instant = payload.dynamic_applied ?? [];
  const rolling = payload.static_applied ?? [];
  if (rolling.length > 0) return "Changes saved — the database carrier is rolling to pick up the rolling changes";
  if (instant.length > 0) return "Changes saved — instant knobs took effect immediately";
  return "Changes saved";
}

export function DatabaseSettingsEditor({ database, projectId }: { database: DatabaseRow; projectId: string }) {
  const catalog = useRecord(dataEnginesResource, {
    id: DATA_ENGINES_RECORD_ID,
    query: { staleTime: DATA_ENGINES_STALE_MS },
  });
  const engine = catalog.data?.find((entry) => entry.engine === database.engine);
  const id = database.id ?? "";

  const baseKey = draftKey(database);
  const base = useMemo<SettingsDraft>(() => JSON.parse(baseKey) as SettingsDraft, [baseKey]);
  const [draft, setDraft] = useState<SettingsDraft>(base);
  const baseRef = useRef(base);
  useEffect(() => {
    // 行期望态变化（保存落定 / 他方 update）：草稿未改则跟随新行；已改则
    // 保留编辑（预演 diff 以服务端当前行为准）。
    const previous = baseRef.current;
    baseRef.current = base;
    setDraft((current) => (draftsEqual(current, previous) ? base : current));
  }, [base]);

  const pendingInput = updateInputFor(id, base, draft);
  const running = database.status === "running";

  // 预览段状态：payload = 当前所见预演；previewedRef = 票面锚（预览时的输入
  // 快照——确认与重预览同源，避免 onSettled 闭包吃旧值）。
  const [payload, setPayload] = useState<PreviewUpdateDatabasePayload | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [staleNote, setStaleNote] = useState(false);
  const previewedRef = useRef<UpdateDatabaseInput | null>(null);

  const preview = useCommand(databasesResource.commands.previewUpdate, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setPayload(result.payload);
        setConfirmOpen(true);
        return;
      }
      if (failure != null) toast.error(describeApiError(failure));
    },
  });

  const update = useCommand(databasesResource.commands.update, {
    scope: { projectId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        toast(updateToast(result.payload));
        previewedRef.current = null;
        setPayload(null);
        setStaleNote(false);
        return;
      }
      // 票过期 / 预览后行被改：自动重预览（新 diff + 新票），确认仍须用户再按。
      if ((isConfirmationStale(result) || isConfirmationMismatch(result)) && previewedRef.current != null) {
        setStaleNote(true);
        preview.mutate(previewedRef.current);
        return;
      }
      if (failure != null) toast.error(describeApiError(failure));
    },
  });

  const startPreview = () => {
    if (pendingInput == null) return;
    previewedRef.current = pendingInput;
    setStaleNote(false);
    update.reset();
    preview.mutate(pendingInput);
  };

  const discard = () => {
    setDraft(base);
    previewedRef.current = null;
    setPayload(null);
    setStaleNote(false);
    preview.reset();
    update.reset();
  };

  // 结果盒：update 的拒绝/冲突（重预览接手的确认族除外）优先，其次预演拒绝。
  const updateShown =
    update.result != null && !isConfirmationStale(update.result) && !isConfirmationMismatch(update.result)
      ? update.result
      : null;

  return (
    <section className="rounded-xl border bg-panel p-5">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="flex items-center gap-2 text-sm font-semibold">
          <SlidersHorizontalIcon className="size-4 text-muted" />
          Engine settings
        </h3>
        <span className="font-mono text-2xs text-muted">
          {database.engine ?? "—"} {database.version ?? ""}
        </span>
      </div>
      <p className="mb-4 text-xs text-muted">
        Knobs and limits come from the engine catalog. <KnobClassBadge knobClass="dynamic" /> knobs take effect live;{" "}
        <KnobClassBadge knobClass="static" /> knobs and resource limits take effect through a supervised rolling restart.
        Every save is previewed first and confirmed with a ticket.
      </p>
      {catalog.status === "loading" ? (
        <p className="text-xs text-muted">Loading the engine catalog…</p>
      ) : catalog.status === "error" ? (
        <p className="text-xs text-err">
          The engine catalog is unavailable{catalog.error ? ` — ${describeApiError(catalog.error)}` : ""}.
        </p>
      ) : engine == null ? (
        <p className="text-xs text-muted">
          The engine catalog has no entry for {database.engine ?? "this engine"} — settings cannot be edited here.
        </p>
      ) : (
        <div className="flex flex-col gap-4">
          <KnobFields
            knobs={engine.knobs}
            values={draft.settings}
            disabled={preview.pending || update.pending}
            onChange={(settings) => setDraft((current) => ({ ...current, settings }))}
          />
          <div>
            <h4 className="mb-2 text-2xs font-semibold tracking-wide text-muted uppercase">Resources</h4>
            <ResourcesFields
              cpuMillis={draft.cpuMillis}
              memoryMb={draft.memoryMb}
              disabled={preview.pending || update.pending}
              onChange={(next) => setDraft((current) => ({ ...current, ...next }))}
            />
          </div>
          <ResultBox result={updateShown} failure={null} />
          <ResultBox result={preview.result} failure={null} />
          <div className="flex flex-wrap items-center justify-end gap-2">
            {!running ? (
              <span className="mr-auto text-2xs text-muted">
                Saving needs a running database (status: {database.status ?? "unknown"}) — updates during a roll are refused.
              </span>
            ) : null}
            <Button type="button" variant="ghost" size="sm" disabled={pendingInput == null} onClick={discard}>
              Discard changes
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={pendingInput == null || !running || preview.pending || update.pending}
              onClick={startPreview}
            >
              {preview.pending ? "Previewing…" : "Save changes"}
            </Button>
          </div>
        </div>
      )}
      {payload != null ? (
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={`Save changes to ${database.name ?? id}?`}
          description={<UpdatePreviewSummary payload={payload} staleNote={staleNote} />}
          confirmLabel={rolloutRestarts(payload) ? "Save and roll" : "Save"}
          pendingLabel="Saving…"
          destructive={false}
          risk={{ tier: "standard" }}
          confirmDisabled={!payload.ticket}
          onConfirm={() => {
            const input = previewedRef.current;
            if (input == null) return undefined;
            return update.mutateAsync({ ...input, ticket: payload.ticket });
          }}
        />
      ) : null}
    </section>
  );
}

// UpdatePreviewSummary：确认框描述区的预演摘要（AlertDialog 描述元素是
// <p>——结构化行用 block span 承载，保持合法的短语内容嵌套）。分级披露：
// 重启（replace）vs 即时生效（zero_touch）。
export function UpdatePreviewSummary({
  payload,
  staleNote = false,
}: {
  payload: PreviewUpdateDatabasePayload;
  staleNote?: boolean;
}) {
  const changes = payload.settings_changes ?? [];
  const from = describeResources(payload.resources_from);
  const to = describeResources(payload.resources_to);
  const restarts = rolloutRestarts(payload);
  return (
    <>
      {staleNote ? (
        <span className="mb-2 block rounded-md border px-2 py-1 text-2xs">
          The database changed since your last preview (or the ticket expired) — this is a fresh preview with a fresh
          ticket.
        </span>
      ) : null}
      {changes.length === 0 ? (
        <span className="block text-xs">No knob changes.</span>
      ) : (
        changes.map((change) => (
          <span key={change.key} className="flex items-center gap-1.5 py-0.5 font-mono text-xs" data-change={change.key}>
            <span className="font-semibold">{change.key}</span>
            <span>
              {change.from || "(default)"} → {change.to || "(default)"}
            </span>
            <KnobClassBadge knobClass={change.class} />
          </span>
        ))
      )}
      <span className="mt-1 block text-xs">
        {from === to ? `Resources unchanged (${from}).` : `Resources: ${from} → ${to}.`}
      </span>
      {restarts ? (
        <>
          <span className="mt-2 block text-xs font-medium">
            Saving triggers a rolling restart of the database carrier (supervised; expect a brief connection blip).
          </span>
          <span className="mt-1 block text-2xs">
            Batch rolling changes into one save — every save that carries rolling changes restarts the carrier once.
          </span>
        </>
      ) : (
        <span className="mt-2 block text-xs font-medium">
          Takes effect immediately — instant knobs change live and nothing restarts.
        </span>
      )}
    </>
  );
}

// ResultBox：结果通道非 ok 即盒（rejected 带 code 前缀；DeploySheet
// CommandResultBox 同形）。失败通道走 toast（onSettled）。
function ResultBox({ result, failure }: { result: CommandResult<unknown> | null; failure: ApiError | null }) {
  if (result != null && result.status !== "ok") {
    return (
      <div role="alert" className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err">
        {result.status === "rejected" && result.code ? <span className="font-mono">[{result.code}] </span> : null}
        {result.message}
      </div>
    );
  }
  if (failure != null) {
    return (
      <div role="alert" className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err">
        {describeApiError(failure)}
      </div>
    );
  }
  return null;
}

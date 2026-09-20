// 部署字段级 diff 视图（T0-V2.4，R3 借鉴）：「与上一次部署相比改了什么」。
// 数据 = 两版真实归一化快照（GetRevisionSpec 的 canonical JSON），diff 计算在
// 前端（spec-diff.ts）——proto/后端零改动。上一版的取法由页面传入：部署历史
// 中当前行之前（更旧）最近一条带 revision 的部署行。
//
// 诚实呈现纪律：快照不可解析、上一版快照已滑出保留窗（active 5 条之外
// GetRevisionSpec 404）、内容与上一版相同（同 revision 重部署）——均如实
// 说明，不伪造 diff、不渲染空表。

import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";

import { getRevisionSpec } from "@/api/endpoints";
import { ApiError, errorEnvelopeFrom } from "@/api/errors";
import type { DeploymentView } from "@/api/types";
import { EnvelopeAlert } from "@/components/envelope-alert";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { diffSpecJSONs, type FieldDiff } from "@/lib/spec-diff";

const KIND_LABEL: Record<FieldDiff["kind"], string> = {
  changed: "Changed",
  added: "Added",
  removed: "Removed",
};

const KIND_CLASSES: Record<FieldDiff["kind"], string> = {
  changed: "border-sky-500/30 bg-sky-500/5 text-sky-800 dark:text-sky-300",
  added:
    "border-emerald-500/30 bg-emerald-500/5 text-emerald-800 dark:text-emerald-300",
  removed: "border-red-500/30 bg-red-500/5 text-red-800 dark:text-red-300",
};

function MutedNote({ children }: { children: string }) {
  return <p className="text-sm text-muted-foreground">{children}</p>;
}

function DiffTable({ diffs }: { diffs: FieldDiff[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Field</TableHead>
          <TableHead>Change</TableHead>
          <TableHead>Old</TableHead>
          <TableHead>New</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {diffs.map((d) => (
          <TableRow key={d.path}>
            <TableCell className="break-all font-mono text-xs">
              {d.path}
            </TableCell>
            <TableCell>
              <span
                className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-[11px] font-medium ${KIND_CLASSES[d.kind]}`}
              >
                {KIND_LABEL[d.kind]}
              </span>
            </TableCell>
            <TableCell className="max-w-[280px] break-all font-mono text-xs text-muted-foreground">
              {d.old}
            </TableCell>
            <TableCell className="max-w-[280px] break-all font-mono text-xs">
              {d.new}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export function DeploymentDiff({
  app,
  deployment,
  previous,
}: {
  app: string;
  deployment: DeploymentView;
  previous: DeploymentView | undefined;
}) {
  const revisionId = deployment.revision_id ?? "";
  const prevRevisionId = previous?.revision_id ?? "";

  const currentSpec = useQuery({
    queryKey: ["revision-spec", app, revisionId],
    queryFn: () => getRevisionSpec(app, revisionId),
    enabled: revisionId !== "" && prevRevisionId !== "" && revisionId !== prevRevisionId,
    retry: false,
  });
  const previousSpec = useQuery({
    queryKey: ["revision-spec", app, prevRevisionId],
    queryFn: () => getRevisionSpec(app, prevRevisionId),
    enabled: revisionId !== "" && prevRevisionId !== "" && revisionId !== prevRevisionId,
    retry: false,
  });

  // 展开面板头部：与哪一版对比（上一部署的 revision 短 id）。
  const header =
    prevRevisionId !== "" ? (
      <p className="text-sm font-semibold">
        Changes vs previous revision{" "}
        <code className="font-mono text-xs font-normal text-muted-foreground">
          {prevRevisionId.slice(0, 12)}
        </code>
      </p>
    ) : null;

  // 无快照：失败/进行中的部署没有固化 revision（快照仅成功终态写入）。
  if (revisionId === "") {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <MutedNote>
          This deployment has no revision snapshot yet. Changes are recorded
          once a deployment succeeds.
        </MutedNote>
      </div>
    );
  }

  // 历史里再往前没有带快照的部署（首发）。
  if (prevRevisionId === "") {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <MutedNote>No previous deployment to compare.</MutedNote>
      </div>
    );
  }

  // 同 revision 重部署/重放：快照行同一行，内容必然一致。
  if (revisionId === prevRevisionId) {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <MutedNote>No changes vs previous revision.</MutedNote>
      </div>
    );
  }

  if (currentSpec.isError || previousSpec.isError) {
    const err = currentSpec.error ?? previousSpec.error;
    // 404 = 目标快照已滑出保留窗（最近 5 次成功部署；superseded 不可经
    // GetRevisionSpec 消费）——如实说明窗口语义，而非裸报错。
    if (err instanceof ApiError && err.status === 404) {
      return (
        <div data-testid="deployment-diff" className="space-y-2">
          {header}
          <MutedNote>
            The previous revision&apos;s snapshot is outside the retention
            window (last 5 successful deployments), so it cannot be diffed.
          </MutedNote>
        </div>
      );
    }
    const envelope = errorEnvelopeFrom(err);
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <EnvelopeAlert
          code={envelope.code ?? ""}
          message={envelope.message ?? "Failed to load revision snapshots."}
          suggestion={envelope.suggestion ?? ""}
          docs={envelope.docs}
        />
      </div>
    );
  }

  if (!currentSpec.data || !previousSpec.data) {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 aria-hidden className="h-4 w-4 animate-spin" />
          Loading snapshots…
        </p>
      </div>
    );
  }

  const diffs = diffSpecJSONs(
    previousSpec.data.compose ?? "",
    currentSpec.data.compose ?? "",
  );
  if (diffs === null) {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <MutedNote>
          Snapshot could not be parsed; the diff is unavailable.
        </MutedNote>
      </div>
    );
  }
  if (diffs.length === 0) {
    return (
      <div data-testid="deployment-diff" className="space-y-2">
        {header}
        <MutedNote>No changes vs previous revision.</MutedNote>
      </div>
    );
  }

  return (
    <div data-testid="deployment-diff" className="space-y-2">
      {header}
      <DiffTable diffs={diffs} />
    </div>
  );
}

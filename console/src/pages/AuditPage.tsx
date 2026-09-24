// 审计页（/admin/audit，v0.3 W3-S3，rbac-teams 设计 §6/§7「（W3）审计页」
// 平台管理员读面）：过滤栏（actor/action/result/target/since-until）+ 分页
//（limit/offset + total）+ 台账表（时间/actor/action/target/result/
// error_code）+ 行展开 diff_summary。actor=user:<id> 的行经 /users 全量
// 清单批量反查 email 展示（平台管理员本身有权限；未命中如实落原始 actor）。
//
// 诚实口径：导出（CSV）不经 Console——指引 CLI `fleetly audit export
// --csv`（S1 已成），Console 只做浏览。留存天数设置行挂页顶（平台管理员
// PUT /audit/retention，保存即生效——janitor 每拍现读）。页面入口仅对
// is_platform_admin 渲染；服务端 admin scope + 平台管理员双门兜底。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight, Download, Loader2, Save } from "lucide-react";
import { Fragment, useMemo, useState, type FormEvent } from "react";

import {
  getAuditRetention,
  listAudit,
  listUsers,
  setAuditRetention,
} from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import type { AuditView } from "@/api/types";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useIsPlatformAdmin } from "@/lib/context";
import { formatTime } from "@/lib/utils";

/** 过滤集（draft 与 applied 分离——Apply 才生效，applied 变更归零 offset）。 */
interface AuditFilters {
  actor: string;
  action: string;
  result: "ok" | "error" | "";
  target: string;
  since: string; // datetime-local 原文；空 = 不过滤。
  until: string;
}

const EMPTY_FILTERS: AuditFilters = { actor: "", action: "", result: "", target: "", since: "", until: "" };

const PAGE_SIZES = [25, 50, 100, 200];

/** datetime-local 原文 → RFC3339（本地时区解释；空/非法 = undefined）。 */
function toRFC3339(local: string): string | undefined {
  if (!local) return undefined;
  const d = new Date(local);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** user:<id> 形态的 actor 拆出 id；其余形态（human/system）返回 null。 */
function userIdOf(actor: string | undefined): string | null {
  if (actor?.startsWith("user:")) return actor.slice("user:".length);
  return null;
}

/** 留存设置行：当前值（未设置 = 缺省口径）+ 修改确认（PUT 前二次确认）。 */
function RetentionCard({ onError }: { onError: (e: ErrorEnvelope) => void }) {
  const queryClient = useQueryClient();
  const [days, setDays] = useState("");
  const [confirming, setConfirming] = useState(false);

  const retentionQuery = useQuery({
    queryKey: ["audit", "retention"],
    queryFn: getAuditRetention,
  });
  const retention = retentionQuery.data;
  // 未设置时不投影缺省数值——生效值回落链（config > 缺省 90）由文案诚实
  // 说明，不谎报设置存在（state 层语义同源）。
  const currentLabel = retention?.set ? `${retention.days} days` : "not explicitly set — the default 90-day window applies";

  const saveMutation = useMutation({
    mutationFn: () => setAuditRetention(Number(days)),
    onSuccess: () => {
      setConfirming(false);
      setDays("");
      void queryClient.invalidateQueries({ queryKey: ["audit", "retention"] });
    },
    onError: (err) => {
      setConfirming(false);
      onError(errorEnvelopeFrom(err));
    },
  });

  return (
    <Card data-testid="audit-retention">
      <CardHeader className="border-b pb-3">
        <CardTitle className="text-sm font-semibold">Audit retention</CardTitle>
        <CardDescription className="mt-1">
          How long audit rows are kept. Saving takes effect immediately (the janitor re-reads the
          setting on every sweep).
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-end gap-2 pt-4">
        <div className="text-sm" data-testid="audit-retention-value">
          {retentionQuery.isPending ? "Loading…" : currentLabel}
        </div>
        <div className="ml-auto flex items-end gap-2">
          <div className="space-y-1.5">
            <Label htmlFor="audit-retention-input">New value (days)</Label>
            <Input
              id="audit-retention-input"
              data-testid="audit-retention-input"
              className="w-32"
              type="number"
              min={1}
              placeholder="90"
              value={days}
              onChange={(e) => setDays(e.target.value)}
            />
          </div>
          <Button
            size="sm"
            data-testid="audit-retention-save"
            disabled={!days || Number(days) < 1 || saveMutation.isPending}
            onClick={() => setConfirming(true)}
          >
            {saveMutation.isPending ? (
              <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Save aria-hidden className="h-3.5 w-3.5" />
            )}
            Save
          </Button>
        </div>
      </CardContent>

      {/* 修改确认：留存缩水会提前清理台账，两步确认同 admin 动作纪律。 */}
      <Dialog open={confirming} onOpenChange={(open) => !open && setConfirming(false)}>
        <DialogContent data-testid="audit-retention-dialog">
          <DialogHeader>
            <DialogTitle>Set audit retention</DialogTitle>
            <DialogDescription>
              Apply a {days || "?"}-day retention window? Rows older than the window are deleted on
              the next sweep — shrinking the window removes older audit history permanently.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" data-testid="audit-retention-cancel" onClick={() => setConfirming(false)}>
              Cancel
            </Button>
            <Button size="sm" data-testid="audit-retention-confirm" disabled={saveMutation.isPending} onClick={() => saveMutation.mutate()}>
              Confirm
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function AuditBrowser() {
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [draft, setDraft] = useState<AuditFilters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<AuditFilters>(EMPTY_FILTERS);
  const [pageSize, setPageSize] = useState(50);
  const [offset, setOffset] = useState(0);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const auditQuery = useQuery({
    queryKey: ["audit", "list", applied, pageSize, offset],
    queryFn: () =>
      listAudit({
        actor: applied.actor || undefined,
        action: applied.action || undefined,
        result: applied.result || undefined,
        target: applied.target || undefined,
        since: toRFC3339(applied.since),
        until: toRFC3339(applied.until),
        limit: pageSize,
        offset,
      }),
  });

  // actor=user:<id> 的 Console 解析 email 展示（rbac-teams §6）：/users 全量
  // 清单批反查（平台管理员本身有权限）；清单失败不阻塞台账——落原始 actor。
  const usersQuery = useQuery({
    queryKey: ["users"],
    queryFn: listUsers,
    retry: false,
  });
  const userDirectory = useMemo(() => {
    const map = new Map<string, { email: string; displayName: string }>();
    for (const u of usersQuery.data?.users ?? []) {
      if (u.id) map.set(u.id, { email: u.email ?? "", displayName: u.display_name ?? "" });
    }
    return map;
  }, [usersQuery.data]);

  const rows = auditQuery.data?.audits ?? [];
  const total = auditQuery.data?.total ?? 0;

  function onApply(e: FormEvent) {
    e.preventDefault();
    setOffset(0);
    setExpandedId(null);
    setApplied(draft);
  }

  function onReset() {
    setDraft(EMPTY_FILTERS);
    setApplied(EMPTY_FILTERS);
    setOffset(0);
    setExpandedId(null);
  }

  function actorLabel(a: AuditView) {
    const id = userIdOf(a.actor);
    if (!id) return a.actor || "—";
    const u = userDirectory.get(id);
    return u?.email || u?.displayName || a.actor || "—";
  }

  return (
    <div className="space-y-4" data-testid="audit-page">
      <PageHeader
        title="Audit log"
        description="Platform-wide action trail (browse-only). CSV export goes through the CLI: fleetly audit export --csv."
        actions={
          <span
            className="flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-xs text-muted-foreground"
            data-testid="audit-export-hint"
            title="The console does not generate exports; use the CLI against the same audit face."
          >
            <Download aria-hidden className="h-3.5 w-3.5" />
            Export via CLI: fleetly audit export --csv
          </span>
        }
      />

      <RetentionCard onError={(e) => setError(e)} />

      <Card>
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Filters</CardTitle>
        </CardHeader>
        <form className="flex flex-wrap items-end gap-2 p-4" onSubmit={onApply}>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-actor">Actor</Label>
            <Input
              id="audit-filter-actor"
              data-testid="audit-filter-actor"
              className="w-40"
              placeholder="user:… / human / system"
              value={draft.actor}
              onChange={(e) => setDraft({ ...draft, actor: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-action">Action prefix</Label>
            <Input
              id="audit-filter-action"
              data-testid="audit-filter-action"
              className="w-40"
              placeholder="auth. / api. / audit."
              value={draft.action}
              onChange={(e) => setDraft({ ...draft, action: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-result">Result</Label>
            {/* 原生 select——jsdom 可测性取舍（同 admin-registration 口径）。 */}
            <select
              id="audit-filter-result"
              data-testid="audit-filter-result"
              className="h-9 rounded-md border bg-background px-2 text-sm"
              value={draft.result}
              onChange={(e) => setDraft({ ...draft, result: e.target.value as AuditFilters["result"] })}
            >
              <option value="">any</option>
              <option value="ok">ok</option>
              <option value="error">error</option>
            </select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-target">Target</Label>
            <Input
              id="audit-filter-target"
              data-testid="audit-filter-target"
              className="w-44"
              placeholder="substring"
              value={draft.target}
              onChange={(e) => setDraft({ ...draft, target: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-since">Since</Label>
            <Input
              id="audit-filter-since"
              data-testid="audit-filter-since"
              className="w-52"
              type="datetime-local"
              value={draft.since}
              onChange={(e) => setDraft({ ...draft, since: e.target.value })}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="audit-filter-until">Until</Label>
            <Input
              id="audit-filter-until"
              data-testid="audit-filter-until"
              className="w-52"
              type="datetime-local"
              value={draft.until}
              onChange={(e) => setDraft({ ...draft, until: e.target.value })}
            />
          </div>
          <Button type="submit" size="sm" data-testid="audit-filter-apply">
            Apply
          </Button>
          <Button type="button" variant="outline" size="sm" data-testid="audit-filter-reset" onClick={onReset}>
            Reset
          </Button>
          <div className="basis-full">
            {error ? (
              <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} docs={error.docs} />
            ) : null}
          </div>
        </form>
      </Card>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Time</TableHead>
                <TableHead>Actor</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Result</TableHead>
                <TableHead>Error code</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((a) => {
                const id = a.id ?? "";
                const expanded = expandedId === id;
                const uid = userIdOf(a.actor);
                const directory = uid ? userDirectory.get(uid) : undefined;
                return (
                  <Fragment key={id}>
                    <TableRow data-testid="audit-row" data-action={a.action}>
                      <TableCell>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-6 w-6"
                          aria-label={expanded ? "Collapse row" : "Expand row"}
                          data-testid="audit-row-expand"
                          onClick={() => setExpandedId(expanded ? null : id)}
                        >
                          {expanded ? (
                            <ChevronDown aria-hidden className="h-3.5 w-3.5" />
                          ) : (
                            <ChevronRight aria-hidden className="h-3.5 w-3.5" />
                          )}
                        </Button>
                      </TableCell>
                      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                        {formatTime(a.at)}
                      </TableCell>
                      <TableCell>
                        <div data-testid="audit-actor-email">{actorLabel(a)}</div>
                        {directory ? (
                          <div className="font-mono text-[10px] text-muted-foreground">{a.actor}</div>
                        ) : null}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{a.action}</TableCell>
                      <TableCell className="max-w-[220px] truncate font-mono text-xs" title={a.target}>
                        {a.target || "—"}
                      </TableCell>
                      <TableCell>
                        {a.result === "error" ? (
                          <Badge
                            variant="secondary"
                            className="bg-destructive/10 text-[10px] font-semibold uppercase tracking-wide text-destructive"
                            data-testid="audit-result-badge"
                          >
                            error
                          </Badge>
                        ) : (
                          <Badge variant="secondary" className="text-[10px] font-semibold uppercase tracking-wide" data-testid="audit-result-badge">
                            {a.result || "ok"}
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs">{a.error_code || "—"}</TableCell>
                    </TableRow>
                    {/* 行展开：脱敏 diff 摘要 + request_id（'' = 无的诚实占位）。 */}
                    {expanded ? (
                      <TableRow data-testid="audit-diff-row">
                        <TableCell />
                        <TableCell colSpan={6}>
                          <div className="space-y-1 rounded-md border bg-muted/40 p-3" data-testid="audit-diff">
                            <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                              Diff summary
                            </div>
                            <div className="break-all font-mono text-xs">{a.diff_summary || "—"}</div>
                            {a.request_id ? (
                              <div className="font-mono text-[10px] text-muted-foreground">request_id: {a.request_id}</div>
                            ) : null}
                          </div>
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })}
              {rows.length === 0 && !auditQuery.isPending ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-6 text-center text-sm text-muted-foreground">
                    No audit rows match the current filters.
                  </TableCell>
                </TableRow>
              ) : null}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <div className="flex items-center gap-3 text-sm" data-testid="audit-pagination">
        <span className="text-xs text-muted-foreground">
          {total > 0 ? `${offset + 1}–${Math.min(offset + pageSize, total)} of ` : ""}
          <span data-testid="audit-total">{total}</span> rows
        </span>
        <select
          aria-label="Page size"
          data-testid="audit-page-size"
          className="h-8 rounded-md border bg-background px-2 text-xs"
          value={pageSize}
          onChange={(e) => {
            setPageSize(Number(e.target.value));
            setOffset(0);
          }}
        >
          {PAGE_SIZES.map((n) => (
            <option key={n} value={n}>
              {n} / page
            </option>
          ))}
        </select>
        <Button
          variant="outline"
          size="sm"
          data-testid="audit-page-prev"
          disabled={offset === 0}
          onClick={() => setOffset(Math.max(0, offset - pageSize))}
        >
          Prev
        </Button>
        <Button
          variant="outline"
          size="sm"
          data-testid="audit-page-next"
          disabled={offset + pageSize >= total}
          onClick={() => setOffset(offset + pageSize)}
        >
          Next
        </Button>
      </div>
    </div>
  );
}

export function AuditPage() {
  const isPlatformAdmin = useIsPlatformAdmin();
  // 非管理员进入（深链）：诚实提示——不渲染管理面（服务端 admin scope +
  // 平台管理员双门仍硬拦，此处只是不展示不可用的面）。
  if (!isPlatformAdmin) {
    return (
      <div className="space-y-4" data-testid="audit-page-denied">
        <PageHeader title="Audit log" />
        <EnvelopeAlert
          code="E_PERMISSION_DENIED"
          message="Platform administrator access is required for the audit log."
          suggestion="Ask an existing platform administrator to grant you the flag (see team design §3.2)."
        />
      </div>
    );
  }
  return <AuditBrowser />;
}

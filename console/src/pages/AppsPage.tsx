// 应用列表（dokploy Services 表式）：卡片内表格 + 搜索/状态/生命周期筛选
// + 排序 + 行点击进详情。derived_state 徽章一等展示（degraded/blocked 语义
// 色与 StateBadge 同源）。筛选与排序均为客户端投影——列表读面无服务端
// 分页参数，全量数据量级（单操作员平台）客户端处理即可。

import { useQuery } from "@tanstack/react-query";
import { Boxes, ChevronRight, Search } from "lucide-react";
import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { listApps } from "@/api/endpoints";
import { errorEnvelopeFrom } from "@/api/errors";
import type { AppView } from "@/api/types";
import { DegradedExplanationCard } from "@/components/degraded-explanation-card";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { EmptyState } from "@/components/empty-state";
import { PageHeader } from "@/components/page-header";
import { StateBadge } from "@/components/state-badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardFooter,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Input } from "@/components/ui/input";
import { timeAgo } from "@/lib/utils";

type StateFilter = "all" | "healthy" | "degraded" | "unavailable";
type LifecycleFilter = "all" | "active" | "deleting";
type SortKey = "updated" | "created" | "name";

const STATE_FILTERS: { key: StateFilter; label: string }[] = [
  { key: "all", label: "All states" },
  { key: "healthy", label: "Running" },
  { key: "degraded", label: "Degraded" },
  { key: "unavailable", label: "Unavailable" },
];

function matchStateFilter(state: string | undefined, filter: StateFilter): boolean {
  switch (filter) {
    case "healthy":
      return state === "running";
    case "degraded":
      return state === "degraded";
    case "unavailable":
      return ["blocked", "down", "failed"].includes(state ?? "");
    default:
      return true;
  }
}

function AppRow({ app }: { app: AppView }) {
  const navigate = useNavigate();
  const name = app.name ?? "";
  return (
    <TableRow
      data-testid="app-row"
      data-state={app.derived_state}
      className="cursor-pointer"
      onClick={() => navigate(`/apps/${encodeURIComponent(name)}`)}
    >
      <TableCell>
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted/40">
            <Boxes aria-hidden className="h-4 w-4 text-muted-foreground" />
          </span>
          <span className="min-w-0">
            <Link
              to={`/apps/${encodeURIComponent(name)}`}
              className="block truncate font-medium hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {name}
            </Link>
            <span className="block truncate font-mono text-xs text-muted-foreground">
              {app.id}
            </span>
            {/* degraded 一等 UI（W5-S2）：行内常驻解释（compact 卡）——
                不再是只有 badge 的二等态；点击链接进事件流（行点击语义
                不受影响——链接 stopPropagation）。 */}
            {app.derived_state === "degraded" ? (
              <div onClick={(e) => e.stopPropagation()}>
                <DegradedExplanationCard app={name} compact />
              </div>
            ) : null}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <StateBadge state={app.derived_state ?? ""} />
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {app.lifecycle}
      </TableCell>
      <TableCell
        className="whitespace-nowrap text-xs text-muted-foreground"
        title={app.updated_at}
      >
        {timeAgo(app.updated_at)}
      </TableCell>
      <TableCell
        className="whitespace-nowrap text-xs text-muted-foreground"
        title={app.created_at}
      >
        {timeAgo(app.created_at)}
      </TableCell>
      <TableCell className="w-10 text-right">
        <ChevronRight aria-hidden className="ml-auto h-4 w-4 text-muted-foreground/60" />
      </TableCell>
    </TableRow>
  );
}

export function AppsPage() {
  const query = useQuery({ queryKey: ["apps"], queryFn: listApps, refetchInterval: 5000 });

  const [search, setSearch] = useState("");
  const [stateFilter, setStateFilter] = useState<StateFilter>("all");
  const [lifecycleFilter, setLifecycleFilter] = useState<LifecycleFilter>("all");
  const [sortKey, setSortKey] = useState<SortKey>("updated");

  // 生成类型口径：空 repeated 字段不出现在 JSON（EmitUnpopulated=false）。
  // useMemo 包一层：引用稳定，下游筛选 memo 的依赖才不会每渲染刷新。
  const apps = useMemo(() => query.data?.apps ?? [], [query.data]);

  const visible = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const filtered = apps.filter((a) => {
      if (needle && !(a.name ?? "").toLowerCase().includes(needle) && !(a.id ?? "").toLowerCase().includes(needle)) {
        return false;
      }
      if (!matchStateFilter(a.derived_state, stateFilter)) return false;
      if (lifecycleFilter !== "all" && (a.lifecycle ?? "active") !== lifecycleFilter) {
        return false;
      }
      return true;
    });
    return filtered.sort((a, b) => {
      switch (sortKey) {
        case "name":
          return (a.name ?? "").localeCompare(b.name ?? "");
        case "created":
          return (b.created_at ?? "").localeCompare(a.created_at ?? "");
        default:
          return (b.updated_at ?? "").localeCompare(a.updated_at ?? "");
      }
    });
  }, [apps, search, stateFilter, lifecycleFilter, sortKey]);

  if (query.isPending) {
    return (
      <div className="space-y-4">
        <PageHeader title="Applications" />
        <p className="text-sm text-muted-foreground">Loading apps…</p>
      </div>
    );
  }
  if (query.isError) {
    const envelope = errorEnvelopeFrom(query.error);
    return (
      <div className="space-y-4">
        <PageHeader title="Applications" />
        <EnvelopeAlert
          code={envelope.code}
          message={envelope.message}
          suggestion={envelope.suggestion}
          docs={envelope.docs}
        />
      </div>
    );
  }

  const filtered = visible.length !== apps.length;

  return (
    <div className="space-y-4">
      <PageHeader
        title="Applications"
        description="Compose-deployed applications and their derived health."
        actions={
          <Button
            variant="outline"
            size="sm"
            onClick={() => void query.refetch()}
            aria-label="Refresh apps"
          >
            Refresh
          </Button>
        }
      />

      <Card>
        <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
          <div className="relative">
            <Search
              aria-hidden
              className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
            />
            <Input
              aria-label="Filter applications"
              placeholder="Filter applications…"
              className="w-56 pl-8"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <Select value={stateFilter} onValueChange={(v) => setStateFilter(v as StateFilter)}>
            <SelectTrigger className="w-36" aria-label="Filter by state">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STATE_FILTERS.map((f) => (
                <SelectItem key={f.key} value={f.key}>
                  {f.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={lifecycleFilter}
            onValueChange={(v) => setLifecycleFilter(v as LifecycleFilter)}
          >
            <SelectTrigger className="w-32" aria-label="Filter by lifecycle">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All lifecycles</SelectItem>
              <SelectItem value="active">active</SelectItem>
              <SelectItem value="deleting">deleting</SelectItem>
            </SelectContent>
          </Select>
          <Select value={sortKey} onValueChange={(v) => setSortKey(v as SortKey)}>
            <SelectTrigger className="ml-auto w-44" aria-label="Sort applications">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="updated">Recently updated</SelectItem>
              <SelectItem value="created">Newest first</SelectItem>
              <SelectItem value="name">Name</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <CardContent className="p-0">
          {apps.length === 0 ? (
            <EmptyState
              icon={Search}
              title="No applications yet."
              hint="Deploy a compose file to create the first app."
            />
          ) : visible.length === 0 ? (
            <EmptyState
              icon={Search}
              title="No applications match the current filters."
              hint="Adjust the search or filter selections to widen the view."
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Application</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Lifecycle</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead />
                </TableRow>
              </TableHeader>
              <TableBody>
                {visible.map((app) => (
                  <AppRow key={app.id} app={app} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
        <CardFooter className="justify-between border-t py-3 text-xs text-muted-foreground">
          <span>
            {filtered
              ? `${visible.length} of ${apps.length} applications`
              : `${apps.length} ${apps.length === 1 ? "application" : "applications"} total`}
          </span>
          <span className="hidden sm:inline">click a row to open</span>
        </CardFooter>
      </Card>
    </div>
  );
}

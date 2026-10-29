import { useState } from "react";
import type { LegacyColumnDef } from "@tanstack/react-table/legacy";
import { useCollection, useCommand } from "@lynx-go/uikit-core";
import { routesResource, routesLookup } from "@/kit/routes";
import { appTabCommands } from "@/kit/app-tabs";
import { toastOnSettled } from "@/kit/toast";
import { describeApiError } from "@/kit/errors";
import { CopyButton } from "@/components/domain/copy-button";
import { DataTable } from "@/components/domain/data-table";
import { EmptyState } from "@/components/domain/empty-state";
import { ListToolbar, useListFilter } from "@/components/domain/list-toolbar";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GlobeIcon } from "lucide-react";
import { toast } from "sonner";

type RouteEntry = { id?: string; host?: string; path?: string; app_id?: string; process?: string; port?: number; protocol?: string; tls_mode?: string };

// App Routes scoped tab（IA v3 T2）：项目路由表按 app 过滤的暴露面 + 创建
//（app 固定本 app）/删除。列表走 house 模板（ListToolbar + DataTable，
// 2026-10-12 走查：手拼 table 与其他列表形态脱队）；创建交互统一契约：
// 入口钉工具栏主动作槽 + 模态对话框。写面批2 命令化（kit/app-tabs 的
// createRoute/deleteRoute，失效声明在命令内）。
// 批4（2026-10-27）：路由表读面迁 kit useCollection（routeId 由宿主路由
// 注入；大页 lookup 不截断行集；15s 轮询同旧拍；错误面经 describeApiError
// 收敛 kernel ApiError——与 ErrorInline 的 fetch-Error 口径并轨）。
export function AppRoutesTab({ projectId, appId, routeId }: { projectId: string; appId: string; routeId: string }) {
  const routes = useCollection(routesResource, {
    from: routeId,
    search: routesLookup,
    scope: { projectId },
    query: { refetchInterval: 15_000 },
  });
  const rows = routes.rows.filter((route) => route.app_id === appId);
  const [createOpen, setCreateOpen] = useState(false);
  const [query, setQuery] = useState("");
  const filtered = useListFilter(rows, query, (route: RouteEntry) => [
    route.host ?? "",
    route.path ?? "",
    `${route.process ?? ""}:${route.port ?? ""}`,
    route.protocol ?? "",
    route.tls_mode ?? "",
  ]);

  const columns: LegacyColumnDef<RouteEntry, any>[] = [
    {
      accessorKey: "host",
      header: "Host",
      cell: ({ row }) => (
        <span className="flex items-center gap-1 font-mono text-xs font-semibold">
          {row.original.host ?? "—"}
          <CopyButton value={row.original.host ?? ""} />
        </span>
      ),
    },
    {
      accessorKey: "path",
      header: "Path",
      cell: ({ row }) => <span className="font-mono text-xs text-muted">{row.original.path || "/"}</span>,
    },
    {
      id: "target",
      header: "Target",
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.process}:{row.original.port}
        </span>
      ),
    },
    {
      accessorKey: "protocol",
      header: "Protocol",
      cell: ({ row }) => <span className="text-xs">{row.original.protocol}</span>,
    },
    {
      accessorKey: "tls_mode",
      header: "TLS",
      cell: ({ row }) => (
        <span className="text-xs text-muted">
          {row.original.tls_mode === "auto" ? "auto (ACME)" : (row.original.tls_mode ?? "—")}
        </span>
      ),
    },
    {
      id: "actions",
      header: () => <span className="sr-only">Actions</span>,
      cell: ({ row }) => <RouteDeleteButton projectId={projectId} route={row.original} />,
    },
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-xl border bg-panel">
        <div className="px-3 pt-3">
          <ListToolbar
            label="routes"
            value={query}
            onChange={setQuery}
            placeholder="Filter routes..."
            total={rows.length}
            shown={filtered.length}
            actions={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <GlobeIcon data-icon-start-inline />
                Add route
              </Button>
            }
          />
        </div>
        <DataTable
          data={filtered}
          columns={columns}
          loading={routes.status === "loading"}
          error={routes.error != null ? new Error(describeApiError(routes.error)) : null}
          onRetry={routes.refetch}
          empty={
            <EmptyState
              icon={GlobeIcon}
              title="No routes for this app"
              description="Expose the app by creating a host route on the managed proxy."
              actionLabel="Add route"
              onAction={() => setCreateOpen(true)}
            />
          }
        />
      </div>
      <CreateRouteDialog projectId={projectId} appId={appId} open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  );
}

function RouteDeleteButton({ projectId, route }: { projectId: string; route: RouteEntry }) {
  // M2：deleteRoute 的失效身份经工厂 scopeOf 派生，零手传。
  const remove = useCommand(appTabCommands(projectId, route.app_id ?? "").deleteRoute, {
    onSettled: toastOnSettled(() => toast("Route deleted")),
  });
  return (
    <div className="text-right">
      <Button
        variant="outline"
        size="sm"
        disabled={remove.pending}
        onClick={() => remove.mutate({ id: route.id ?? "" })}
      >
        Delete
      </Button>
    </div>
  );
}

function CreateRouteDialog({
  projectId,
  appId,
  open,
  onOpenChange,
}: {
  projectId: string;
  appId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [form, setForm] = useState({ host: "", path: "", process: "web", port: "8080", protocol: "http", tls: "auto" });
  const create = useCommand(appTabCommands(projectId, appId).createRoute, {
    scope: { projectId },
    onSettled: toastOnSettled(() => {
      toast("Route created");
      onOpenChange(false);
      setForm({ host: "", path: "", process: "web", port: "8080", protocol: "http", tls: "auto" });
    }),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!create.pending) onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add route</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault();
            create.mutate({
              host: form.host,
              path: form.path,
              process: form.process,
              port: Number(form.port),
              protocol: form.protocol,
              tlsMode: form.tls,
            });
          }}
        >
          <div className="flex flex-col gap-2">
            <Label htmlFor="route-host">Host</Label>
            <Input
              id="route-host"
              className="font-mono"
              value={form.host}
              onChange={(event) => setForm({ ...form, host: event.target.value })}
              placeholder="app.dev.fleetly.run"
              autoFocus
            />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="route-path">Path (optional prefix)</Label>
              <Input id="route-path" className="font-mono" value={form.path} onChange={(event) => setForm({ ...form, path: event.target.value })} placeholder="/" />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="route-tls">TLS</Label>
              <select
                id="route-tls"
                className="h-9 rounded-md border bg-transparent px-3 text-sm shadow-xs"
                value={form.tls}
                onChange={(event) => setForm({ ...form, tls: event.target.value })}
              >
                <option value="auto">auto (ACME)</option>
                <option value="none">none</option>
              </select>
            </div>
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div className="flex flex-col gap-2">
              <Label htmlFor="route-process">Process</Label>
              <Input id="route-process" className="font-mono" value={form.process} onChange={(event) => setForm({ ...form, process: event.target.value })} />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="route-port">Port</Label>
              <Input id="route-port" className="font-mono" inputMode="numeric" value={form.port} onChange={(event) => setForm({ ...form, port: event.target.value.replace(/\D/g, "") })} />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="route-protocol">Protocol</Label>
              <select
                id="route-protocol"
                className="h-9 rounded-md border bg-transparent px-3 text-sm shadow-xs"
                value={form.protocol}
                onChange={(event) => setForm({ ...form, protocol: event.target.value })}
              >
                <option value="http">http</option>
                <option value="h2c">h2c</option>
                <option value="tcp">tcp</option>
              </select>
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={create.pending || form.host === ""}>
              {create.pending ? "Creating…" : "Create route"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

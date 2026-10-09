import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { apiSend } from "@/api/client";
import { useRoutes } from "@/lib/catalog";
import { CopyButton } from "@/components/domain/copy-button";
import { EmptyState } from "@/components/domain/empty-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { GlobeIcon } from "lucide-react";
import { toast } from "sonner";

type RouteEntry = { id?: string; host?: string; path?: string; app_id?: string; process?: string; port?: number; protocol?: string; tls_mode?: string };

function fieldError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

// App Routes scoped tab（IA v3 T2）：项目路由表按 app 过滤的暴露面 + 创建
// （app 固定本 app）/删除。创建交互统一契约：入口工具栏右上 + 模态对话框。
export function AppRoutesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const routes = useRoutes(projectId);
  const queryClient = useQueryClient();
  const rows = (routes.data ?? []).filter((route) => route.app_id === appId);
  const [createOpen, setCreateOpen] = useState(false);
  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ["resources", "routes", projectId] });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-end">
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          Add route
        </Button>
      </div>
      {routes.isPending ? (
        <p className="py-10 text-center text-xs text-muted-foreground">Loading routes…</p>
      ) : routes.isError ? (
        <p className="py-10 text-center text-xs text-destructive">{fieldError(routes.error)}</p>
      ) : rows.length === 0 ? (
        <EmptyState
          icon={GlobeIcon}
          title="No routes for this app"
          description="Expose the app by creating a host route on the managed proxy."
          actionLabel="Add route"
          onAction={() => setCreateOpen(true)}
        />
      ) : (
        <div className="overflow-hidden rounded-xl border">
          <table className="w-full text-sm">
            <thead className="border-b bg-muted/40 text-left text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
              <tr>
                <th className="px-3 py-2">Host</th>
                <th className="px-3 py-2">Path</th>
                <th className="px-3 py-2">Target</th>
                <th className="px-3 py-2">Protocol</th>
                <th className="px-3 py-2">TLS</th>
                <th className="px-3 py-2 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((route) => (
                <RouteRow key={route.id} route={route} onDeleted={invalidate} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <CreateRouteDialog projectId={projectId} appId={appId} open={createOpen} onOpenChange={setCreateOpen} onCreated={invalidate} />
    </div>
  );
}

function RouteRow({ route, onDeleted }: { route: RouteEntry; onDeleted: () => void }) {
  const remove = useMutation({
    mutationFn: async () => apiSend(`/v1/routes/${encodeURIComponent(route.id ?? "")}`, "DELETE"),
    onSuccess: () => {
      toast("Route deleted");
      onDeleted();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <tr className="border-b last:border-b-0">
      <td className="px-3 py-2">
        <span className="flex items-center gap-1 font-mono text-xs font-semibold">
          {route.host ?? "—"}
          <CopyButton value={route.host ?? ""} />
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{route.path || "/"}</td>
      <td className="px-3 py-2 font-mono text-xs">
        {route.process}:{route.port}
      </td>
      <td className="px-3 py-2 text-xs">{route.protocol}</td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{route.tls_mode === "auto" ? "auto (ACME)" : (route.tls_mode ?? "—")}</td>
      <td className="px-3 py-2 text-right">
        <Button variant="outline" size="sm" disabled={remove.isPending} onClick={() => remove.mutate()}>
          Delete
        </Button>
      </td>
    </tr>
  );
}

function CreateRouteDialog({
  projectId,
  appId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  appId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [form, setForm] = useState({ host: "", path: "", process: "web", port: "8080", protocol: "http", tls: "auto" });
  const create = useMutation({
    mutationFn: async () =>
      apiSend("/v1/routes", "POST", {
        project_id: projectId,
        host: form.host,
        path: form.path || undefined,
        app_id: appId,
        process: form.process,
        port: Number(form.port),
        protocol: form.protocol,
        tls_mode: form.tls,
      }),
    onSuccess: () => {
      toast("Route created");
      onOpenChange(false);
      setForm({ host: "", path: "", process: "web", port: "8080", protocol: "http", tls: "auto" });
      onCreated();
    },
    onError: (cause) => toast.error(fieldError(cause)),
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!create.isPending) onOpenChange(next);
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
            create.mutate();
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
            <Button type="submit" disabled={create.isPending || form.host === ""}>
              {create.isPending ? "Creating…" : "Create route"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

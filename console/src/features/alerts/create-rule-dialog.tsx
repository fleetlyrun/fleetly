import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useProjects, useApps } from "@/lib/catalog";
import { apiSend } from "@/api/client";
import { Button } from "@/components/ui/button";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

// 告警规则创建（List 原型主操作，UI v2 批 3）：旧 CreateRuleModal 的
// Dialog 化迁移——值域冻结（ADR-0041 cpu_percent/memory_working_set_bytes）
// 与聚合口径文案保真；写后失效 alerts 键。

const ALERT_METRICS = [
  { key: "cpu_percent", label: "CPU (% of node)" },
  { key: "memory_working_set_bytes", label: "Memory working set (bytes)" },
] as const;

export function CreateRuleDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const queryClient = useQueryClient();
  const projects = useProjects();
  const [projectId, setProjectId] = useState("");
  const apps = useApps(projectId);
  const [appId, setAppId] = useState("");
  const [metric, setMetric] = useState<string>(ALERT_METRICS[0].key);
  const [threshold, setThreshold] = useState("");
  const [forSeconds, setForSeconds] = useState("0");

  const create = useMutation({
    mutationFn: () =>
      apiSend("/v1/alerts/rules", "POST", {
        app_id: appId,
        metric,
        threshold: Number(threshold),
        for_seconds: forSeconds === "" ? "0" : forSeconds,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["alerts"] });
      onOpenChange(false);
      setThreshold("");
    },
  });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New alert rule</DialogTitle>
          <DialogDescription>Evaluated every 30s — aggregation is max across the app's containers (hottest replica).</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Project</Label>
            <Select
              value={projectId}
              onValueChange={(value) => {
                setProjectId(value);
                setAppId("");
              }}
            >
              <SelectTrigger>
                <SelectValue placeholder="— pick a project —" />
              </SelectTrigger>
              <SelectContent>
                {(projects.data ?? []).map((project) => (
                  <SelectItem key={project.id} value={project.id}>
                    {project.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">App</Label>
            <Select value={appId} onValueChange={setAppId} disabled={projectId === ""}>
              <SelectTrigger>
                <SelectValue placeholder="— pick an app —" />
              </SelectTrigger>
              <SelectContent>
                {(apps.data ?? []).map((app) => (
                  <SelectItem key={app.id} value={app.id}>
                    {app.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs">Metric</Label>
            <Select value={metric} onValueChange={setMetric}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {ALERT_METRICS.map((entry) => (
                  <SelectItem key={entry.key} value={entry.key}>
                    {entry.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">Threshold</Label>
              <Input
                type="number"
                step="any"
                min="0"
                value={threshold}
                onChange={(event) => setThreshold(event.target.value)}
                placeholder={metric === "cpu_percent" ? "90" : "1073741824"}
              />
              <p className="text-[11px] text-muted-foreground">cpu rules are percent of node; memory rules are bytes</p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">For (seconds)</Label>
              <Input type="number" min="0" value={forSeconds} onChange={(event) => setForSeconds(event.target.value)} />
              <p className="text-[11px] text-muted-foreground">consecutive breach window (0 = immediately)</p>
            </div>
          </div>
          {create.isError ? (
            <div className="rounded-lg border border-[color-mix(in_oklch,var(--status-danger)_35%,transparent)] bg-[var(--status-danger-bg)] px-3 py-2 font-mono text-[11px] break-all text-muted-foreground">
              {String(create.error)}
            </div>
          ) : null}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={create.isPending || appId === "" || threshold === ""} onClick={() => void create.mutate()}>
            {create.isPending ? "Creating…" : "Create rule"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

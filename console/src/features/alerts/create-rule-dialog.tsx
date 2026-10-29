import { useEffect, useState } from "react";
import { useCollectionData, useCommand } from "@lynx-go/uikit-core";
import type { ApiError, CommandResult } from "@lynx-go/uikit-core/kernel";
import { projectsResource } from "@/kit/projects";
import { appsResource } from "@/kit/apps";
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
import { alertRulesResource } from "@/kit/alerts";
import { describeApiError } from "@/kit/errors";

// 告警规则创建（List 原型主操作，UI v2 批 3）：旧 CreateRuleModal 的
// Dialog 化迁移——值域冻结（ADR-0041 cpu_percent/memory_working_set_bytes）
// 与聚合口径文案保真；写后失效 alerts 键。
// 批2（2026-10-26）写位命令化：挂 alertRules resource commands（legacyKeys
// 桥 ["alerts"] 前缀）；壳保留（project→app 级联 Radix Select 动态值域不
// 迁 form 块），rejected/conflict 结果通道改 message-first 结果盒（旧
// String(error) 盒退役）。

const ALERT_METRICS = [
  { key: "cpu_percent", label: "CPU (% of node)" },
  { key: "memory_working_set_bytes", label: "Memory working set (bytes)" },
] as const;

export function CreateRuleDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const projects = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 });
  const [projectId, setProjectId] = useState("");
  const apps = useCollectionData(appsResource, {
    scope: { projectId },
    window: { size: 200 },
    refetchInterval: 60_000,
    enabled: projectId !== "",
  });
  const [appId, setAppId] = useState("");
  const [metric, setMetric] = useState<string>(ALERT_METRICS[0].key);
  const [threshold, setThreshold] = useState("");
  const [forSeconds, setForSeconds] = useState("0");

  const create = useCommand(alertRulesResource.commands.create, {
    // rejected/conflict/failure → 下方结果盒（盒子优先，toast 不双响）
    onSettled: (result) => {
      if (result?.status === "ok") {
        onOpenChange(false);
        setThreshold("");
      }
    },
  });

  // 重开清上一轮结果/失败面（结果盒不跨会话残留）
  const resetCreate = create.reset;
  useEffect(() => {
    if (open) resetCreate();
  }, [open, resetCreate]);

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
                {projects.rows.map((project) => (
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
                {apps.rows.map((app) => (
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
              <p className="text-2xs text-muted">cpu rules are percent of node; memory rules are bytes</p>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs">For (seconds)</Label>
              <Input type="number" min="0" value={forSeconds} onChange={(event) => setForSeconds(event.target.value)} />
              <p className="text-2xs text-muted">consecutive breach window (0 = immediately)</p>
            </div>
          </div>
          <CommandResultBox result={create.result} failure={create.failure} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            disabled={create.pending || appId === "" || threshold === ""}
            onClick={() =>
              create.mutate({
                appId,
                metric,
                threshold: Number(threshold),
                forSeconds: forSeconds === "" ? "0" : forSeconds,
              })
            }
          >
            {create.pending ? "Creating…" : "Create rule"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// 结果盒（form 块结果分支同形）：rejected 带 code 前缀 message-first；
// conflict 如实呈现；失败通道走 describeApiError 文案。
function CommandResultBox({
  result,
  failure,
}: {
  result: CommandResult<unknown> | null;
  failure: ApiError | null;
}) {
  if (result != null && result.status !== "ok") {
    return (
      <div role="alert" className="rounded-md border border-err/40 bg-err/10 px-3 py-2 text-xs text-err">
        {result.status === "rejected" && result.code ? (
          <span className="font-mono">[{result.code}] </span>
        ) : null}
        {result.message}
      </div>
    );
  }
  if (failure != null) {
    return (
      <div role="alert" className="rounded-md border border-err/40 bg-err/10 px-3 py-2 font-mono text-2xs break-all text-err">
        {describeApiError(failure)}
      </div>
    );
  }
  return null;
}

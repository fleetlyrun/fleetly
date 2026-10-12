import { useProcessInstances } from "@/lib/catalog";

// 在跑实例观测件（App→Process→Instance 钻取面，2026-10-12 层级定案）：
// 数据单源 useProcessInstances（ADR-0067 快照语义——直读编排器任务/pod
// 列表，空集 = 观测行非错误），Processes 页 Running 列/行展开与 Overview
// per-process 摘要共用。用词分工是层级语义的一部分：声明侧 replicas
//（spec 数量）、运行侧 instances（live 个体）。

// RunningCount 是进程的 live 在跑实例数（声明 replicas 的对照面）。
export function RunningCount({ appId, processName }: { appId: string; processName: string }) {
  const instances = useProcessInstances(appId, processName);
  if (instances.isPending) return <span className="text-muted-foreground">…</span>;
  if (instances.isError) {
    const message = instances.error instanceof Error ? instances.error.message : String(instances.error);
    return (
      <span className="text-muted-foreground" title={message}>
        n/a
      </span>
    );
  }
  const count = instances.data?.instances?.length ?? 0;
  return <span className={count > 0 ? "" : "text-muted-foreground"}>{count}</span>;
}

// InstanceList 是进程行展开的实例明细（编排器载体标识全值 + 平台节点——
// 短形式只用于下拉展示面，明细面回显全值）。
export function InstanceList({ appId, processName }: { appId: string; processName: string }) {
  const instances = useProcessInstances(appId, processName);
  if (instances.isPending) return <p className="py-1.5 text-xs text-muted-foreground">Loading instances…</p>;
  if (instances.isError) {
    const message = instances.error instanceof Error ? instances.error.message : String(instances.error);
    return <p className="py-1.5 text-xs text-destructive">{message}</p>;
  }
  const rows = instances.data?.instances ?? [];
  if (rows.length === 0) {
    return <p className="py-1.5 text-xs text-muted-foreground">No running instances — replicas may be stopped or between deploys.</p>;
  }
  return (
    <ul className="flex flex-col gap-1 py-1.5 font-mono text-xs">
      {rows.map((row) => (
        <li key={row.instance ?? ""} className="flex flex-wrap items-baseline gap-x-3">
          <span className="text-muted-foreground">instance</span>
          <span className="min-w-0 break-all">{row.instance}</span>
          {row.node_id ? (
            <>
              <span className="text-muted-foreground">node</span>
              <span>{row.node_id}</span>
            </>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

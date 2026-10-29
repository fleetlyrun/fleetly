import { useCollectionData } from "@lynx-go/uikit-core";
import { execInstancesResource } from "@/kit/exec";
import { describeApiError } from "@/kit/errors";

// 在跑实例观测件（App→Process→Instance 钻取面，2026-10-12 层级定案）：
// 数据单源 exec-instances 资源（ADR-0067 快照语义——直读编排器任务/pod
// 列表，空集 = 观测行非错误），Processes 页 Running 列/行展开与 Overview
// per-process 摘要共用。用词分工是层级语义的一部分：声明侧 replicas
//（spec 数量）、运行侧 instances（live 个体）。

// 伴读窗（M1 useCollectionData）：整集窗 size 200（实例个位数~两位数），
// 10s 轮询 + 空 process 禁查（旧 useProcessInstances 同拍）。
function useExecInstances(appId: string, process: string) {
  return useCollectionData(execInstancesResource, {
    scope: { appId, process },
    window: { size: 200 },
    refetchInterval: 10_000,
    enabled: appId !== "" && process !== "",
  });
}

// RunningCount 是进程的 live 在跑实例数（声明 replicas 的对照面）。
export function RunningCount({ appId, processName }: { appId: string; processName: string }) {
  const instances = useExecInstances(appId, processName);
  if (instances.status === "loading") return <span className="text-muted">…</span>;
  if (instances.status === "error") {
    const message = instances.error ? describeApiError(instances.error) : "";
    return (
      <span className="text-muted" title={message}>
        n/a
      </span>
    );
  }
  const count = instances.rows.length;
  return <span className={count > 0 ? "" : "text-muted"}>{count}</span>;
}

// InstanceList 是进程行展开的实例明细（编排器载体标识全值 + 平台节点——
// 短形式只用于下拉展示面，明细面回显全值）。
export function InstanceList({ appId, processName }: { appId: string; processName: string }) {
  const instances = useExecInstances(appId, processName);
  if (instances.status === "loading") return <p className="py-1.5 text-xs text-muted">Loading instances…</p>;
  if (instances.status === "error") {
    const message = instances.error ? describeApiError(instances.error) : "";
    return <p className="py-1.5 text-xs text-err">{message}</p>;
  }
  const rows = instances.rows;
  if (rows.length === 0) {
    return <p className="py-1.5 text-xs text-muted">No running instances — replicas may be stopped or between deploys.</p>;
  }
  return (
    <ul className="flex flex-col gap-1 py-1.5 font-mono text-xs">
      {rows.map((row) => (
        <li key={row.instance ?? ""} className="flex flex-wrap items-baseline gap-x-3">
          <span className="text-muted">instance</span>
          <span className="min-w-0 break-all">{row.instance}</span>
          {row.node_id ? (
            <>
              <span className="text-muted">node</span>
              <span>{row.node_id}</span>
            </>
          ) : null}
        </li>
      ))}
    </ul>
  );
}

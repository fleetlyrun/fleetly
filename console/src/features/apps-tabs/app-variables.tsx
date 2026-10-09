import { useQuery } from "@tanstack/react-query";
import { ApiError, apiFetch } from "@/api/client";
import type { components } from "@/api/structure";
import { Link } from "@tanstack/react-router";
import { FileJsonIcon } from "lucide-react";
import { EmptyState } from "@/components/domain/empty-state";
import { CopyButton } from "@/components/domain/copy-button";

type AppSpec = components["schemas"]["v1AppSpec"];

// App Variables tab（IA v3 二期②点亮）：冻结 Spec 的只读读面——per-process
// env / secret_refs / 端口 / 卷附件 + build/source 摘要。写路径仅 Deploy
//（DeploySheet image 模式 env / spec_file）；暂存式编辑流为后续批。
// secret 值不进 Spec（引用即锚），Reveal once 语义在 Project Configuration。
export function AppVariablesTab({ projectId, appId }: { projectId: string; appId: string }) {
  const spec = useQuery({
    queryKey: ["resources", "app-spec", projectId, appId],
    queryFn: async (): Promise<AppSpec | undefined> => {
      try {
        const res = await apiFetch<{ spec?: AppSpec }>(`/v1/apps/${encodeURIComponent(appId)}/spec`);
        return res?.spec;
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return undefined;
        throw error;
      }
    },
    refetchInterval: 60_000,
  });

  if (spec.isPending) {
    return <p className="py-10 text-center text-xs text-muted-foreground">Loading spec…</p>;
  }
  if (spec.isError) {
    return <p className="py-10 text-center text-xs text-destructive">{spec.error instanceof Error ? spec.error.message : String(spec.error)}</p>;
  }
  if (spec.data == null) {
    return (
      <EmptyState
        icon={FileJsonIcon}
        title="No frozen spec yet"
        description="The app has no deployment — its spec freezes on the first deploy. Environment can be provided at deploy time."
        actionLabel="Deploy now"
        onAction={() => window.location.assign(`/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}?deploy=1`)}
      />
    );
  }
  const s = spec.data;
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        {(s.processes ?? []).map((process) => (
          <section key={process.name} className="rounded-xl border bg-card p-4">
            <div className="mb-3 flex items-baseline justify-between">
              <h3 className="text-[13px] font-semibold">
                process <span className="font-mono">{process.name}</span>
              </h3>
              <span className="text-[11.5px] text-muted-foreground">×{process.replicas ?? 1}</span>
            </div>
            <EnvTable env={process.env ?? {}} />
            <div className="mt-3 flex flex-col gap-1.5 text-xs">
              <span className="text-muted-foreground">secret refs</span>
              {(process.secret_refs ?? []).length === 0 ? (
                <span className="text-muted-foreground">—</span>
              ) : (
                <span className="flex flex-wrap gap-1.5">
                  {(process.secret_refs ?? []).map((ref) => (
                    <Link
                      key={ref}
                      to="/p/$projectId/configuration"
                      params={{ projectId }}
                      className="rounded-full border border-info/30 bg-info/10 px-2 py-0.5 font-mono text-[11px] text-info"
                    >
                      {ref}
                    </Link>
                  ))}
                </span>
              )}
            </div>
            {(process.volumes ?? []).length > 0 ? (
              <div className="mt-3 flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">volumes</span>
                {(process.volumes ?? []).map((volume) => (
                  <span key={`${volume.volume_id}-${volume.target}`} className="font-mono text-[11px] text-muted-foreground">
                    {volume.volume_id} → {volume.target}
                    {volume.read_only ? " (ro)" : ""}
                  </span>
                ))}
              </div>
            ) : null}
            {(process.ports ?? []).length > 0 ? (
              <div className="mt-3 text-xs text-muted-foreground">
                ports: {(process.ports ?? []).map((port) => `${port.port ?? "?"}/${port.protocol ?? "http"}`).join(", ")}
              </div>
            ) : null}
          </section>
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <SourceCard spec={s} />
        <BuildCard spec={s} />
      </div>

      <p className="text-[11.5px] text-muted-foreground">
        Read-only view of the frozen spec (latest revision). Changes go live by creating a new deployment — the deploy sheet
        carries environment for image deploys and full spec files for the spec mode. Values of secrets never enter the spec; only
        named references.
      </p>
    </div>
  );
}

function EnvTable({ env }: { env: Record<string, string> }) {
  const names = Object.keys(env).sort();
  return (
    <div className="overflow-hidden rounded-lg border">
      <table className="w-full text-xs">
        <thead className="border-b bg-muted/40 text-left text-[10.5px] font-semibold tracking-wide text-muted-foreground uppercase">
          <tr>
            <th className="px-2.5 py-1.5">Variable</th>
            <th className="px-2.5 py-1.5">Value</th>
            <th className="px-2.5 py-1.5" />
          </tr>
        </thead>
        <tbody>
          {names.length === 0 ? (
            <tr>
              <td colSpan={3} className="px-2.5 py-3 text-center text-muted-foreground">
                No plain env vars.
              </td>
            </tr>
          ) : (
            names.map((name) => (
              <tr key={name} className="border-b last:border-b-0">
                <td className="px-2.5 py-1.5 font-mono font-semibold">{name}</td>
                <td className="px-2.5 py-1.5 font-mono text-muted-foreground">{env[name]}</td>
                <td className="px-2.5 py-1.5 text-right">
                  <CopyButton value={env[name]} />
                </td>
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

function SourceCard({ spec }: { spec: AppSpec }) {
  const source = spec.source;
  const kind = source?.git ? "git" : source?.image ? "image" : source?.upload ? "upload" : "—";
  const detail = source?.git ? `${source.git.repo} @ ${source.git.ref ?? "-"}` : source?.image?.ref ?? (source?.upload?.id ? `upload ${source.upload.id}` : "—");
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-[13px] font-semibold">Source</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
        <dt className="text-muted-foreground">kind</dt>
        <dd className="font-mono">{kind}</dd>
        <dt className="text-muted-foreground">origin</dt>
        <dd className="break-all font-mono">{detail}</dd>
      </dl>
    </section>
  );
}

function BuildCard({ spec }: { spec: AppSpec }) {
  const build = spec.build;
  return (
    <section className="rounded-xl border bg-card p-4">
      <h3 className="mb-3 text-[13px] font-semibold">Build</h3>
      {build == null ? (
        <p className="text-xs text-muted-foreground">No build — image-deployed app.</p>
      ) : (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-xs">
          <dt className="text-muted-foreground">builder</dt>
          <dd className="font-mono">{build.builder ?? "—"}</dd>
          <dt className="text-muted-foreground">dockerfile</dt>
          <dd className="font-mono">{build.dockerfile || "—"}</dd>
          <dt className="text-muted-foreground">cache from</dt>
          <dd className="font-mono">{build.cache_from?.length ? build.cache_from.join(", ") : "—"}</dd>
        </dl>
      )}
    </section>
  );
}

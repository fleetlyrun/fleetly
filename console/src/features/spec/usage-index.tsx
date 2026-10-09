import { LinkIcon } from "lucide-react";
import { useApps, useUploads, useVolumes } from "@/lib/catalog";
import { specIndex, useAppSpecs } from "@/features/spec/use-app-specs";

// UsageIndex（IA v3 二期②）：Volume 挂载反查 + Upload 引用反查——扫描项目
// 内 App 冻结 Spec（processes[].volumes[].volume_id / source.upload.id）。
// 客户端 fan-out（ADR-0057 惯例）；一屏回答"这个卷/上传谁在用"。
export function UsageIndex({ projectId }: { projectId: string }) {
  const apps = useApps(projectId);
  const volumes = useVolumes(projectId);
  const uploads = useUploads(projectId);
  const { specs, pending } = useAppSpecs(projectId, apps.data ?? []);

  const appName = (id: string) => (apps.data ?? []).find((app) => app.id === id)?.name ?? id;
  const volumeIndex = specIndex(specs, (spec) =>
    (spec.processes ?? []).flatMap((process) => (process.volumes ?? []).map((volume) => volume.volume_id ?? "")),
  );
  const uploadIndex = specIndex(specs, (spec) => (spec.source?.upload?.id ? [spec.source.upload.id] : []));

  return (
    <section className="mb-6 rounded-xl border bg-card p-4">
      <h2 className="mb-2 text-[13px] font-semibold">Usage index</h2>
      <p className="mb-3 text-[11.5px] text-muted-foreground">
        Resolved from frozen app specs — mounts and upload references go live on the app's next deploy.
      </p>
      {pending ? (
        <p className="text-xs text-muted-foreground">Scanning app specs…</p>
      ) : (
        <div className="grid gap-5 lg:grid-cols-2">
          <div>
            <h3 className="mb-1.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">Volume → apps</h3>
            {(volumes.data ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">No volumes.</p>
            ) : (
              <ul className="flex flex-col gap-1 text-xs">
                {(volumes.data ?? []).map((volume) => {
                  const users = volumeIndex.get(volume.id ?? "") ?? [];
                  return (
                    <li key={volume.id} className="flex items-center gap-2">
                      <span className="font-mono">{volume.name || volume.id}</span>
                      {users.length === 0 ? (
                        <span className="text-muted-foreground">unmounted</span>
                      ) : (
                        <span className="flex items-center gap-1 text-muted-foreground">
                          <LinkIcon className="size-3" />
                          {users.map(appName).join(", ")}
                        </span>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
          <div>
            <h3 className="mb-1.5 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">Upload → apps</h3>
            {(uploads.data ?? []).length === 0 ? (
              <p className="text-xs text-muted-foreground">No uploads.</p>
            ) : (
              <ul className="flex flex-col gap-1 text-xs">
                {(uploads.data ?? []).slice(0, 20).map((upload) => {
                  const users = uploadIndex.get(upload.id ?? "") ?? [];
                  return (
                    <li key={upload.id} className="flex items-center gap-2">
                      <span className="font-mono" title={upload.id}>
                        {(upload.id ?? "").slice(0, 12)}…
                      </span>
                      {users.length === 0 ? (
                        <span className="text-muted-foreground">unreferenced</span>
                      ) : (
                        <span className="flex items-center gap-1 text-muted-foreground">
                          <LinkIcon className="size-3" />
                          {users.map(appName).join(", ")}
                        </span>
                      )}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

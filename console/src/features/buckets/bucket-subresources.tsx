import { useState } from "react";
import { useCollection, useCommand } from "@lynxland/uikit-core";
import { toast } from "sonner";
import { PlusIcon, Trash2Icon } from "lucide-react";
import { ConfirmDialog } from "@/components/domain/confirm-dialog";
import { RelativeTime } from "@/components/domain/relative-time";
import { StatusBadge } from "@/components/domain/status-badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { subStatusTone } from "@/features/pubsubs/pubsub-subresources";
import { describeApiError } from "@/kit/errors";
import { toastOnSettled } from "@/kit/toast";
import { subBucketsLookup, subBucketsResource, type SubBucketRow } from "@/kit/subresources";

// Bucket Store 子资源 tab（B8，ADR-0096 决策 10 / design §4.7）：Bucket 声明行
// + admin 工具容器（silo 自带 mcli）收敛——可选 hard quota（live 定案在册）。
// 删除 = wide 档（PreviewDelete 删除披露行列出桶内对象量与字节量——mcli du
// 实测可得；删除连带对象，不可恢复）。Update 面在 CLI/API（`fleetly buckets
// bucket update`），Console v1 承载建/列/删。

// formatBytes：quota/数据量的人类形态（0 = none）。
export function formatBytes(value: string | number | undefined): string {
  const n = Number(value ?? 0);
  if (!n) return "none";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

export function BucketSubResourcesTab({
  projectId,
  bucketStoreId,
  routeId,
}: {
  projectId: string;
  bucketStoreId: string;
  routeId: string;
}) {
  const buckets = useCollection(subBucketsResource, {
    from: routeId,
    search: subBucketsLookup,
    scope: { projectId, bucketStoreId },
    query: { refetchInterval: 10_000 },
  });
  const [createOpen, setCreateOpen] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted">
          Declared buckets converge through the admin tool container (mcli). Hard quotas are enforced by the object
          scanner — writes are refused once a bucket reaches its quota.
        </p>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <PlusIcon data-icon-start-inline />
          New bucket
        </Button>
      </div>
      <div className="overflow-hidden rounded-xl border">
        <table className="w-full text-sm">
          <thead className="border-b bg-panel-2/40 text-left text-2xs font-semibold tracking-wide text-muted uppercase">
            <tr>
              <th className="px-3 py-2">Bucket</th>
              <th className="px-3 py-2">Quota</th>
              <th className="px-3 py-2">Status</th>
              <th className="px-3 py-2">Created</th>
              <th className="px-3 py-2 text-right">Actions</th>
            </tr>
          </thead>
          <tbody>
            {buckets.status === "loading" ? (
              <tr>
                <td colSpan={5} className="px-3 py-6 text-center text-xs text-muted">
                  Loading buckets…
                </td>
              </tr>
            ) : buckets.rows.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-3 py-6 text-center text-xs text-muted">
                  No buckets declared yet.
                </td>
              </tr>
            ) : (
              buckets.rows.map((bucket) => (
                <tr key={bucket.id} className="border-b last:border-b-0">
                  <td className="px-3 py-2 font-mono text-xs">{bucket.name}</td>
                  <td className="px-3 py-2 text-xs">{formatBytes(bucket.quota_bytes)}</td>
                  <td className="px-3 py-2">
                    <StatusBadge tone={subStatusTone(bucket.status)}>{bucket.status ?? "unknown"}</StatusBadge>
                    {bucket.status_error ? <div className="mt-1 text-2xs text-warning">{bucket.status_error}</div> : null}
                  </td>
                  <td className="px-3 py-2 text-xs text-muted">
                    <RelativeTime value={bucket.created_at} />
                  </td>
                  <td className="px-3 py-2 text-right">
                    <BucketDeleteButton projectId={projectId} bucketStoreId={bucketStoreId} bucket={bucket} />
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      <CreateBucketDialog open={createOpen} onOpenChange={setCreateOpen} projectId={projectId} bucketStoreId={bucketStoreId} />
    </div>
  );
}

function CreateBucketDialog({
  open,
  onOpenChange,
  projectId,
  bucketStoreId,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  bucketStoreId: string;
}) {
  const [name, setName] = useState("");
  const [quota, setQuota] = useState("");
  const create = useCommand(subBucketsResource.commands.create, {
    scope: { projectId, bucketStoreId },
    onSettled: toastOnSettled(() => toast("Bucket declared")),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>New bucket</DialogTitle>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void create
              .mutateAsync({ bucketStoreId, name, quotaBytes: quota ? Number(quota) : undefined })
              .then(() => onOpenChange(false))
              .catch(() => undefined);
          }}
        >
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="bucket-name">Name (3-63 chars: a-z, 0-9, dot, hyphen)</Label>
            <Input id="bucket-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="media" />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="bucket-quota">Hard quota in bytes (empty = no quota)</Label>
            <Input id="bucket-quota" inputMode="numeric" value={quota} onChange={(e) => setQuota(e.target.value)} />
          </div>
          {create.failure ? <p className="text-xs text-destructive">{describeApiError(create.failure)}</p> : null}
          <div className="flex justify-end">
            <Button type="submit" size="sm" disabled={create.pending || name.trim() === ""}>
              {create.pending ? "Declaring…" : "Declare bucket"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function BucketDeleteButton({
  projectId,
  bucketStoreId,
  bucket,
}: {
  projectId: string;
  bucketStoreId: string;
  bucket: SubBucketRow;
}) {
  const [open, setOpen] = useState(false);
  const [disclosure, setDisclosure] = useState("");
  const [ticket, setTicket] = useState("");
  const preview = useCommand(subBucketsResource.commands.previewDelete, {
    scope: { projectId, bucketStoreId },
    onSettled: (result, failure) => {
      if (result?.status === "ok") {
        setDisclosure(result.payload.disclosure ?? "");
        setTicket(result.payload.ticket ?? "");
        setOpen(true);
        return;
      }
      if (failure != null) toast.error(describeApiError(failure));
    },
  });
  const remove = useCommand(subBucketsResource.commands.delete, {
    scope: { projectId, bucketStoreId },
    onSettled: toastOnSettled(() => toast("Bucket deleted — the convergence segment force-removes the live bucket")),
  });
  const id = bucket.id ?? "";
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        aria-label={`Delete bucket ${bucket.name ?? ""}`}
        disabled={preview.pending}
        onClick={() => preview.mutate({ bucketStoreId, id })}
      >
        <Trash2Icon />
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={`Delete bucket ${bucket.name ?? ""}?`}
        description="Deleting a bucket force-removes every object in it."
        confirmLabel="Delete bucket"
        pendingLabel="Deleting…"
        risk={{ tier: "wide", consequences: <p className="text-xs">{disclosure}</p> }}
        onConfirm={() => remove.mutateAsync({ bucketStoreId, id, ticket })}
      />
    </>
  );
}

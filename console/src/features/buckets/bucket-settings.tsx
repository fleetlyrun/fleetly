import { EngineSettingsEditor } from "@/features/databases/settings-editor";
import { bucketStoresResource, type BucketStoreRow } from "@/kit/buckets";

// Bucket Store Settings tab 的旋钮/资源编辑体（B5 姐妹页）：EngineSettingsEditor
// 的 bucketstore 族壳（PubSubSettingsEditor 同构——预览/确认命令接
// bucketStoresResource；silo v1 零旋钮：表单只剩 resources 面，任何保存的
// 预演恒 replace——确认框按 rollout 值如实披露「将触发滚动重启」）。
export function BucketStoreSettingsEditor({ bucketStore, projectId }: { bucketStore: BucketStoreRow; projectId: string }) {
  return (
    <EngineSettingsEditor
      row={bucketStore}
      projectId={projectId}
      previewUpdate={bucketStoresResource.commands.previewUpdate}
      update={bucketStoresResource.commands.update}
      noun="bucket store"
    />
  );
}

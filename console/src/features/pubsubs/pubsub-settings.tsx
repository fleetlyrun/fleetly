import { EngineSettingsEditor } from "@/features/databases/settings-editor";
import { pubsubsResource, type PubSubRow } from "@/kit/pubsubs";

// PubSub Settings tab 的旋钮/资源编辑体（B4c 姐妹页）：EngineSettingsEditor
// 的 pubsub 族壳（DatabaseSettingsEditor 同构——预览/确认命令接
// pubsubsResource；本族首批旋钮全 static：任何保存的预演恒 replace——确认
// 框按 rollout 值如实披露「将触发滚动重启」）。
export function PubSubSettingsEditor({ pubsub, projectId }: { pubsub: PubSubRow; projectId: string }) {
  return (
    <EngineSettingsEditor
      row={pubsub}
      projectId={projectId}
      previewUpdate={pubsubsResource.commands.previewUpdate}
      update={pubsubsResource.commands.update}
      noun="pubsub"
    />
  );
}

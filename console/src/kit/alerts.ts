import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows } from "./http";

// 告警域资源适配（2026-10-26 批1 Wave2-A useCollection 化，平台域——无
// scope）：rules / channels 双资源供双 tab 双列表。alert states（GET
// /v1/alerts 的现行评估态）**不在本域**——它是 lead 槽 firing 摘要 + 行内
// state 覆盖列的前端 join 数据源而非列表，保持 useQuery（lib/catalog 的
// useAlertStates）。REST 端点与收敛口径照抄 lib/catalog 的
// useAlertRules/useAlertingChannels（rowsOf 同款 undefined 行过滤）。

export type AlertRuleRow = telemetrySchemas["schemas"]["v1AlertRule"];
export type ChannelRow = telemetrySchemas["schemas"]["v1NotificationChannel"];

export async function fetchAlertRules(_ctx: ResourceScope, signal?: AbortSignal): Promise<AlertRuleRow[]> {
  try {
    return await fetchRows<AlertRuleRow>(
      "/v1/alerts/rules",
      (res) => (res as { rules?: Array<AlertRuleRow | undefined> }).rules,
      signal,
    );
  } catch (err) {
    // 资源适配器失败必须收敛为 kernel ApiError（states 块错误分诊契约）
    throw toApiError(err);
  }
}

export async function fetchAlertingChannels(_ctx: ResourceScope, signal?: AbortSignal): Promise<ChannelRow[]> {
  try {
    return await fetchRows<ChannelRow>(
      "/v1/channels",
      (res) => (res as { channels?: Array<ChannelRow | undefined> }).channels,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

// ---- 写路径命令（批2） ----

// create = 告警规则创建（ADR-0041 值域冻结在调用面，命令只承载载荷形）：
// threshold Number 化、for_seconds 空串归 "0" 与旧对话框载荷逐字段同形
//（int64 protojson 字符串侧）。
export interface CreateAlertRuleInput {
  appId: string;
  metric: string;
  threshold: number;
  forSeconds: string;
}

const createAlertRuleCommand: CommandDescriptor<CreateAlertRuleInput, void> = {
  run: (input) =>
    runCommandMapped(() =>
      apiSend<void>("/v1/alerts/rules", "POST", {
        app_id: input.appId,
        metric: input.metric,
        threshold: input.threshold,
        for_seconds: input.forSeconds,
      }),
    ),
};

export const alertRulesResource = defineResource({
  name: "alert-rules",
  getId: (row: AlertRuleRow) => row.id ?? "",
  collection: clientCollection(fetchAlertRules),
  // 存量键失效桥：对齐旧对话框的 invalidate 键（["alerts"] 前缀——覆盖
  // catalog 的 ["alerts","rules"] 与 ["alerts","states"] 双键）。
  legacyKeys: () => [["alerts"]],
  commands: {
    create: createAlertRuleCommand,
  },
});

export const channelsResource = defineResource({
  name: "notification-channels",
  getId: (row: ChannelRow) => row.id ?? "",
  collection: clientCollection(fetchAlertingChannels),
});

// 双 tab 共用实例：q/分页跨 tab 持久是接受语义（tab 切换函数式 search 保
// 留集合键）。字段集 = Rules（app_id/metric）∪ Channels（name/kind）。
export const alertsSearch = collectionQuerySchema({
  text: ["app_id", "metric", "name", "kind"],
  defaultSize: 20,
});

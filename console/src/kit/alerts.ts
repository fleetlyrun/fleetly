import { clientCollection } from "@lynx-go/uikit-core/adapters";
import { defineResource, type CommandDescriptor, type ResourceScope } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as telemetrySchemas } from "@/api/telemetry";
import { apiSend } from "@/api/client";
import { runCommandMapped, toApiError } from "./errors";
import { fetchRows } from "./http";

// 告警域资源适配（2026-10-26 批1 Wave2-A useCollection 化，平台域——无
// scope）：rules / channels 双资源供双 tab 双列表。alert states（GET
// /v1/alerts 的现行评估态）批4 起也收进 kit（alertStatesResource）——它是
// lead 槽 firing 摘要 + 行内 state 覆盖列的前端 join 数据源而非列表，读面
// 用大页 lookup schema 不写 URL。REST 端点与收敛口径照抄 lib/catalog 的
// useAlertRules/useAlertingChannels/useAlertStates（rowsOf 同款 undefined
// 行过滤）。

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

// 双列表各持 namespace 实例（F1 回流批 M1：rules.q 与 channels.q 并存互
// 不携带，「q 跨 tab 持久」妥协退役；字段集保持旧并集——Rules（app_id/
// metric）∪ Channels（name/kind））。
export const rulesSearch = collectionQuerySchema({
  namespace: "rules",
  text: ["app_id", "metric", "name", "kind"],
  defaultSize: 20,
});
export const channelsSearch = collectionQuerySchema({
  namespace: "channels",
  text: ["app_id", "metric", "name", "kind"],
  defaultSize: 20,
});

// ---- alert states（2026-10-27 批4 读面收编：catalog useAlertStates 退役；
// 端点与收敛照抄，轮询由消费方 query.refetchInterval 注入 15s 同拍） ----

export type AlertStateRow = telemetrySchemas["schemas"]["v1AlertState"];

export async function fetchAlertStates(_ctx: ResourceScope, signal?: AbortSignal): Promise<AlertStateRow[]> {
  try {
    return await fetchRows<AlertStateRow>(
      "/v1/alerts",
      (res) => (res as { states?: Array<AlertStateRow | undefined> }).states,
      signal,
    );
  } catch (err) {
    throw toApiError(err);
  }
}

// 平台域无 scope（join 数据源全局一张表）；行 id = rule×app 复合（评估态
// 无独立 id，规则行 join 键是 rule_id）。
export const alertStatesResource = defineResource({
  name: "alert-states",
  getId: (row: AlertStateRow) => `${row.rule_id ?? ""}:${row.app_id ?? ""}`,
  collection: clientCollection(fetchAlertStates),
  // 存量键失效桥：catalog 的 ["alerts","states"] 键
  legacyKeys: () => [["alerts", "states"]],
});

// join 读面用大页 schema（firing 总览要全集——窗口不截断行集）。
export const alertStatesLookup = collectionQuerySchema({ defaultSize: 200 });

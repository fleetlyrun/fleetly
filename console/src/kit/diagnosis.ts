import { restCollection, restRecord } from "@lynx-go/uikit-core/adapters";
import { defineResource } from "@lynx-go/uikit-core/kernel";
import { collectionQuerySchema } from "@lynx-go/uikit-core/schema";
import type { components as Diagnosis } from "@/api/diagnosis";
import { authFetch } from "./http";

// 诊断读面资源族（B2a W4，zeabur 借鉴设计 §2.5；ADR-0085 词汇——Finding
// 读时计算不落行，诊断面只读不写：本文件零 commands，消费面不触碰
// useCommand）。两个读面：
// ① appDiagnosisResource（DiagnoseApp）：App 详情「诊断」段的 record 面，
//    id 约定 "current"（伴读同 appSpecSheetResource/appHookResource 族）；
// ② projectFindingsResource（ListProjectFindings）：「需要你」页的跨 App
//    聚合面——服务端 keyset 方言（after_app_seq 游标轴 = App 创建序，先行
//    筛命中的 App 才占响应——行数 ≠ 扫描窗，hasMore 只能由「整窗占满」
//    推导，见 nextCursor 注释）。
// rest 工厂族（F1 回流批 D 组同款）：组 URL/信封投影/错误收敛归工厂
// 内置，fetch 注入 authFetch（Bearer + 401 闭环单源）。

export type FindingView = Diagnosis["schemas"]["v1Finding"];
export type AppFindingsView = Diagnosis["schemas"]["v1AppFindings"];

// 单 App 诊断结果（record 面；findings 空 = 无失败态可解释——健康也是
// 答案；unreadableFaces 是读不了的事实面名，消费面诚实展示）。
export interface AppDiagnosis {
  findings: FindingView[];
  unreadableFaces: string[];
}

// ① App 诊断 record：15s 拍子（alert states 同档——同为「现在坏没坏」的
// 观测面；深诊断含一次 runtime 观测往返，不再加频）。
export const appDiagnosisResource = defineResource({
  name: "app-diagnosis",
  scope: ["projectId", "appId"],
  record: restRecord<AppDiagnosis>({
    path: (_id, ctx) => `/v1/apps/${encodeURIComponent(ctx.appId ?? "")}/diagnosis`,
    envelope: (body): AppDiagnosis => {
      const res = body as { findings?: FindingView[]; unreadable_faces?: string[] };
      return { findings: res.findings ?? [], unreadableFaces: res.unreadable_faces ?? [] };
    },
    fetch: authFetch,
  }),
});

// ② 「需要你」聚合 collection：页大小钉 50（服务端缺省同值——ListApps
// 分页惯例的扇出预算上限；「需要你」页不提供页大小切换，窗口 size 与
// 请求 limit 恒等，nextCursor 的短页判定才封闭）。
export const FINDINGS_PAGE_SIZE = 50;

// keyset 方言（服务端游标）：window.cursor（URL 进出）→ after_app_seq，
// limit 恒 50。健康 App 推进游标但不占响应（proto 注释），故「短页 =
// 到底」只对整窗成立——nextCursor 仅在响应行数 = 页大小时下发（末条
// app_seq 回显），短页即窗尽。
export const projectFindingsResource = defineResource({
  name: "project-findings",
  getId: (row: AppFindingsView) => row.app_id ?? "",
  scope: ["projectId"],
  collection: restCollection<AppFindingsView>({
    path: (ctx) => `/v1/projects/${encodeURIComponent(ctx.projectId ?? "")}/findings`,
    envelope: (body) => (body as { apps?: AppFindingsView[] }).apps ?? [],
    params: (query) => ({
      after_app_seq: query.window.cursor === undefined ? undefined : query.window.cursor,
      limit: FINDINGS_PAGE_SIZE,
    }),
    nextCursor: (body) => {
      const apps = (body as { apps?: AppFindingsView[] }).apps ?? [];
      if (apps.length < FINDINGS_PAGE_SIZE) return undefined; // 短页 = 扫描窗尽（ListApps 惯例）
      const last = apps[apps.length - 1]?.app_seq;
      return typeof last === "string" && last !== "" ? last : undefined;
    },
    fetch: authFetch,
  }),
});

// 「需要你」页 URL schema（游标/窗口进 URL——kit keyset 分页件消费）。
export const projectFindingsSearch = collectionQuerySchema({ defaultSize: FINDINGS_PAGE_SIZE });

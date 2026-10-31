import { useCollection } from "@lynx-go/uikit-core";
import { CircleCheckIcon } from "lucide-react";
import { FAULT_CLASSES, FindingCard, UnreadableFacesChip, faultClassOf, type FaultClass } from "@/features/findings/finding-card";
import { KeysetPagination } from "@/components/kit/query/keyset-pagination";
import { PageFrame } from "@/components/domain/page-frame";
import { ProjectAvatar } from "@/components/domain/project-avatar";
import { projectFindingsResource, projectFindingsSearch, type AppFindingsView, type FindingView } from "@/kit/diagnosis";
import { describeApiError } from "@/kit/errors";

// 「需要你」页（B2a W4，设计 §2.5；CONTEXT.md Finding 词条：按 project 域
// 聚合，platform 组单列「平台造成（等管理员修）」形态）：ListProjectFindings
// 读面（kit collection，服务端 keyset——after_app_seq 游标进 URL，kit
// KeysetPagination 承载翻页）。纯读页：无命令、无写面。分组 = fault_class
// 四档定序（app → declaration → platform → external）；platform 是特殊形
// 态——组头钉「等管理员修」文案，条目无 fix 钮（platform 的错永远不读成
// 用户的错）。
// 页面宿主在 features/findings，路由只挂参（对照 tasks/deployments 的
// project 子页挂法）。
export function ProjectFindingsPage({ projectId, routeId }: { projectId: string; routeId: string }) {
  const list = useCollection(projectFindingsResource, {
    from: routeId,
    search: projectFindingsSearch,
    scope: { projectId },
    // 深诊断扇出有预算（每 App 一次观测/账本往返）——拍子取目录档 60s，
    // 不追观测档。
    query: { refetchInterval: 60_000 },
  });
  const rows = list.rows;

  // 分组视图：fault_class 四档定序 → App 块（App 创建序保持服务端序）→
  // 条目（规则序保持确定性产出序）。
  const groups = new Map<FaultClass, AppFindingsView[]>();
  for (const cls of FAULT_CLASSES) groups.set(cls, []);
  for (const app of rows) {
    for (const finding of app.findings ?? []) {
      const bucket = groups.get(faultClassOf(finding));
      if (bucket != null && !bucket.includes(app)) bucket.push(app);
    }
  }

  if (list.status === "loading") {
    return <p className="py-10 text-center text-xs text-muted">Collecting findings across apps…</p>;
  }
  if (list.status === "error") {
    return <p className="py-10 text-center text-xs text-err">{describeApiError(list.error ?? { kind: "network" })}</p>;
  }

  return (
    <PageFrame
      title="Needs you"
      description="Diagnosis findings across this project's apps, grouped by where the fix lives — refreshed every 60s"
    >
      {/* 游标页空 ≠ 项目全清（只是本页无条目）——绿态只给首页空，游标页
          保留分页栏可退（Prev 走浏览器历史，kit keyset 惯例）。 */}
      {rows.length === 0 && list.query.window.cursor === undefined ? (
        // 空态 = 一切正常（绿）。
        <div className="rounded-xl border border-ok/30 bg-ok/10 p-8 text-center">
          <CircleCheckIcon className="mx-auto size-5 text-ok" />
          <h2 className="mt-2 text-sm font-semibold text-ok">Nothing needs you</h2>
          <p className="mt-1 text-xs text-muted">
            No diagnosis findings in this project. Apps that fail their cheap pre-check are diagnosed here; healthy apps stay
            quiet.
          </p>
        </div>
      ) : (
        <div className="flex flex-col gap-5">
          {rows.length === 0 ? (
            <p className="py-6 text-center text-xs text-muted">No apps with findings on this page — go back with Prev.</p>
          ) : (
            FAULT_CLASSES.map((cls) => {
              const apps = groups.get(cls) ?? [];
              if (apps.length === 0) return null;
              return <FaultClassGroup key={cls} cls={cls} apps={apps} projectId={projectId} />;
            })
          )}
          <KeysetPagination
            list={list}
            labels={{
              describe: ({ rows: pageRows, hasMore }) =>
                pageRows === 0
                  ? "No apps with findings this page"
                  : `${pageRows} app${pageRows === 1 ? "" : "s"} with findings this page${hasMore ? "" : " (end)"}`,
            }}
          />
        </div>
      )}
    </PageFrame>
  );
}

// GROUP_META 是四档组头文案（platform 特殊形态钉在设计 §2.3/§2.5：「不是
// 我的问题，等管理员修」+ Actions 不可用）。
const GROUP_META: Record<FaultClass, { title: string; hint: string }> = {
  app: {
    title: "App failures",
    hint: "Caused by your code, image, or dependencies — look at the app itself.",
  },
  declaration: {
    title: "Declaration changes needed",
    hint: "Caused by the declared spec — change the declaration and deploy.",
  },
  platform: {
    title: "Platform issues — waiting for an administrator",
    hint: "Not caused by your apps. Actions are unavailable here; an administrator fixes the platform side.",
  },
  external: {
    title: "External dependencies",
    hint: "Upstream of the platform — image registries, ACME/DNS, provider APIs.",
  },
};

// FaultClassGroup 是单档分组：组头（标题 + 条目数 + hint）+ App 块列。
// platform 组的条目无 fix 钮（FindingCard platform 分支：描述与定位即全
// 部——「描述与组件名」）。
function FaultClassGroup({ cls, apps, projectId }: { cls: FaultClass; apps: AppFindingsView[]; projectId: string }) {
  const meta = GROUP_META[cls];
  let count = 0;
  for (const app of apps) count += (app.findings ?? []).length;
  return (
    <section className="rounded-xl border bg-panel">
      <div className="border-b px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-2">
          <h2 className="text-sm font-semibold">{meta.title}</h2>
          <span className="text-2xs text-muted">
            {count} finding{count === 1 ? "" : "s"} · {apps.length} app{apps.length === 1 ? "" : "s"}
          </span>
        </div>
        <p className="mt-0.5 text-xs text-muted">{meta.hint}</p>
      </div>
      <div className="flex flex-col gap-4 px-4 py-4">
        {apps.map((app) => (
          <AppBlock key={app.app_id ?? ""} app={app} cls={cls} projectId={projectId} />
        ))}
      </div>
    </section>
  );
}

// AppBlock 是组内单 App 块：App 名（深链 App 详情）+ unreadable 诚实片 +
// 该 App 落在本档的条目卡。
function AppBlock({ app, cls, projectId }: { app: AppFindingsView; cls: FaultClass; projectId: string }) {
  const appId = app.app_id ?? "";
  const name = app.app_name || appId;
  const inClass = (app.findings ?? []).filter((finding: FindingView) => faultClassOf(finding) === cls);
  // 跨 App 深链基路径（条目卡 fix 深链的宿主面——App 详情）。
  const appBase = `/p/${encodeURIComponent(projectId)}/apps/${encodeURIComponent(appId)}`;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <ProjectAvatar seed={appId} label={name} className="size-5 rounded-md text-2xs" />
        <a href={appBase} className="text-sm font-medium text-info underline-offset-2 hover:underline">
          {name}
        </a>
        <UnreadableFacesChip faces={app.unreadable_faces ?? []} />
      </div>
      <div className="flex flex-col gap-3">
        {inClass.map((finding: FindingView) => (
          <FindingCard key={`${finding.finding_id}-${finding.workload_id}`} finding={finding} appBase={appBase} />
        ))}
      </div>
    </div>
  );
}

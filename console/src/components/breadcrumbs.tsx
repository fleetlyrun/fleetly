// 面包屑：从 location 派生（Home / Applications / <app> / <tab>）。
// 已知路由段映射为业务名；未知段按业务解析：
//   - 团队 id 段 → 团队名（Me 投影）；
//   - 应用 id 段（详情导航 2026-09-25 起 id 寻址）→ 应用名：响应式观察
//     ["apps", ref] 列表缓存与 ["app", id] 详情缓存（后者与详情壳共用同
//     键查询——深链直入也随后到数据补出真名，getQueryData 非响应式读不到
//     异步填充，2026-09-25 复验实爆）。缓存缺席回退通用名，绝不裸显 ID。
// 末段为当前页（纯文本），前段可点。

import { useQuery } from "@tanstack/react-query";
import { Fragment } from "react";
import { Link, useLocation } from "react-router-dom";
import { ChevronRight } from "lucide-react";

import { getApp, listApps } from "@/api/endpoints";
import { useProjectContext } from "@/lib/context";

const SEGMENT_LABELS: Record<string, string> = {
  apps: "Applications",
  databases: "Databases",
  system: "System",
  events: "Events",
  audit: "Audit",
  teams: "Teams",
  projects: "Projects",
  deployments: "Deployments",
  logs: "Logs",
  env: "Env",
  secrets: "Secrets",
  domains: "Domains",
  terminal: "Terminal",
};

/** 26 字符规范 ULID（平台 ID 段的形态判据）。 */
const ULID_RE = /^[0-9A-HJKMNP-TV-Z]{26}$/;

export function Breadcrumbs() {
  const location = useLocation();
  const { teams, projectRef } = useProjectContext();
  const segments = location.pathname.split("/").filter(Boolean);

  // 应用 id 段至多一个（路径末段）；useQuery 数量恒定，enabled 门控是否
  // 挂观察者（enabled=false 只读缓存不发请求——列表先行场景零开销）。
  const lastSeg = segments[segments.length - 1] ?? "";
  const prevOfLast = segments[segments.length - 2];
  const lastIsAppId = ULID_RE.test(lastSeg) && prevOfLast === "apps";
  const appsList = useQuery({
    queryKey: ["apps", projectRef],
    queryFn: () => listApps(projectRef ? { project: projectRef } : {}),
    enabled: false,
    staleTime: 5 * 60 * 1000,
  });
  const appDetail = useQuery({
    queryKey: ["app", lastSeg],
    queryFn: () => getApp(lastSeg),
    enabled: lastIsAppId,
    staleTime: 30 * 1000,
    retry: false,
  });

  const labelFor = (seg: string, prev?: string): string => {
    if (SEGMENT_LABELS[seg]) return SEGMENT_LABELS[seg];
    if (ULID_RE.test(seg)) {
      const team = teams.find((t) => t.team_id === seg);
      if (team) return team.team_name || team.team_slug || "Team";
      if (prev === "apps") {
        if (seg === lastSeg) {
          const fromList = appsList.data?.apps?.find((a) => a.id === seg);
          if (fromList?.name) return fromList.name;
          return appDetail.data?.name ?? "Application";
        }
        return "Application";
      }
      return "Team settings";
    }
    return decodeURIComponent(seg);
  };

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1 text-sm">
      <Link
        to="/"
        className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
      >
        Home
      </Link>
      {segments.map((seg, i) => {
        const href = `/${segments.slice(0, i + 1).join("/")}`;
        const label = labelFor(seg, segments[i - 1]);
        const last = i === segments.length - 1;
        return (
          <Fragment key={href}>
            <ChevronRight aria-hidden className="h-3.5 w-3.5 shrink-0 text-muted-foreground/60" />
            {last ? (
              <span aria-current="page" className="truncate font-medium">
                {label}
              </span>
            ) : (
              <Link
                to={href}
                className="truncate text-muted-foreground transition-colors hover:text-foreground"
              >
                {label}
              </Link>
            )}
          </Fragment>
        );
      })}
    </nav>
  );
}

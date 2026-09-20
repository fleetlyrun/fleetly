// 面包屑：从 location 派生（Home / Applications / <app> / <tab>）。
// 已知路由段映射为业务名；未知段按应用名解码展示（详情页只有一层动态段，
// 无歧义）。末段为当前页（纯文本），前段可点。

import { Fragment } from "react";
import { Link, useLocation } from "react-router-dom";
import { ChevronRight } from "lucide-react";

const SEGMENT_LABELS: Record<string, string> = {
  apps: "Applications",
  system: "System",
  events: "Events",
  deployments: "Deployments",
  logs: "Logs",
  env: "Env",
  domains: "Domains",
};

export function Breadcrumbs() {
  const location = useLocation();
  const segments = location.pathname.split("/").filter(Boolean);

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
        const label = SEGMENT_LABELS[seg] ?? decodeURIComponent(seg);
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

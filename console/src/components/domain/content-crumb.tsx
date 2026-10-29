import { Link } from "@tanstack/react-router";
import { useCollectionData } from "@lynx-go/uikit-core";
import { projectsResource } from "@/kit/projects";

// 内容区面包屑（List/Detail 原型页头位）：项目名 > 区段 > 当前资源名。
// 首段恒为项目名（目录反查，未达回退 Project）——W-1/W-2 走查教训：
// 各页自拼 crumb 曾打出裸项目 ID 与 "web / web"，故收编为唯一形态。
export function ContentCrumb({
  projectId,
  section,
  current,
}: {
  projectId: string;
  section?: { label: string; to: string };
  current?: string;
}) {
  // 目录伴读（M1 useCollectionData）：项目名反查单源，60s 轮询同壳层目录拍。
  const projects = useCollectionData(projectsResource, { window: { size: 100 }, refetchInterval: 60_000 });
  const projectName = projects.rows.find((project) => project.id === projectId)?.name;
  return (
    <span className="text-xs text-muted">
      <Link to="/p/$projectId" params={{ projectId }} className="hover:text-fg">
        {projectName ?? "Project"}
      </Link>
      {section ? (
        <>
          <span className="mx-1.5">/</span>
          <Link to={section.to} params={{ projectId }} className="hover:text-fg">
            {section.label}
          </Link>
        </>
      ) : null}
      {current ? (
        <>
          <span className="mx-1.5">/</span>
          <span className="text-fg">{current}</span>
        </>
      ) : null}
    </span>
  );
}

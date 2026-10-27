import { Link } from "@tanstack/react-router";
import { useProjectName } from "@/kit/projects";

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
  const projectName = useProjectName(projectId);
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

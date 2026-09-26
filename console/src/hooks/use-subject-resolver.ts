// subject 反解器的数据装配 hook（2026-09-26 走查 W2-7）：聚合 app/db/team/
// project/node（及平台管理员的 users）列表缓存，返回 subjectLabel 的绑定了
// 数据的投影函数。查询键与各列表页/顶栏共享（同 key 去重——导航即热数据，
// 不叠加请求风暴）；users 仅平台管理员发起（非管理员 403 噪音面，与 s3
// 探测同款 enabled 门）。

import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { listApps, listDatabases, listNodes, listProjects, listTeams, listUsers } from "@/api/endpoints";
import { useIsPlatformAdmin } from "@/lib/context";
import { type SubjectLookup, subjectLabel } from "@/lib/subject";

const LOOKUP_STALE_MS = 30_000;

export function useSubjectResolver(): (subject: string | undefined) => string {
  const isPlatformAdmin = useIsPlatformAdmin();
  const apps = useQuery({ queryKey: ["apps"], queryFn: () => listApps(), staleTime: LOOKUP_STALE_MS });
  const databases = useQuery({
    queryKey: ["databases", ""],
    queryFn: () => listDatabases(),
    staleTime: LOOKUP_STALE_MS,
  });
  const teams = useQuery({ queryKey: ["teams", "all"], queryFn: () => listTeams(), staleTime: LOOKUP_STALE_MS });
  const projects = useQuery({
    queryKey: ["projects", "context"],
    queryFn: () => listProjects(),
    staleTime: LOOKUP_STALE_MS,
  });
  const nodes = useQuery({
    queryKey: ["system", "nodes"],
    queryFn: () => listNodes(),
    staleTime: LOOKUP_STALE_MS,
  });
  const users = useQuery({
    queryKey: ["users"],
    queryFn: () => listUsers(),
    enabled: isPlatformAdmin,
    staleTime: LOOKUP_STALE_MS,
    retry: false,
  });

  return useMemo(() => {
    const lookup: SubjectLookup = {
      apps: apps.data?.apps,
      databases: databases.data?.databases,
      teams: teams.data?.teams,
      projects: projects.data?.projects,
      nodes: nodes.data?.nodes,
      users: users.data?.users,
    };
    return (subject: string | undefined) => subjectLabel(subject, lookup);
  }, [
    apps.data,
    databases.data,
    teams.data,
    projects.data,
    nodes.data,
    users.data,
  ]);
}

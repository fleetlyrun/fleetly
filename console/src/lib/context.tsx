// 团队/项目上下文（v0.3 W2-S5，rbac-teams 设计 §7「顶栏：团队切换器 +
// 项目切换器」）：当前选择持久化 localStorage（键名沿仓内
// fleetly.console.* 前缀惯例）；项目选择随团队切换而失效（跨队项目无
// 意义）。资源页经 useProjectContext().projectRef 携带 `team/prj` 收窄
// （服务端 ?project= 语义，apps.proto/databases.proto ListXxxRequest）；
// 跨团队展示一律限定形 team/prj（D-W0-9）。
//
// 门语义：本模块派生的角色能力（useTeamCapabilities）只是前端体验门——
// viewer 隐藏写按钮等；服务端角色门（W2-S4 ResolvePermission）才是硬门。
// 项目覆写角色不进 Me 投影，能力判定按团队角色（被覆写升权的用户少看了
// 按钮——如实披露的保守面，见 S5 执行汇总）。

import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import { listProjects, me } from "@/api/endpoints";
import type { ProjectView, TeamMembership } from "@/api/types";

const CONTEXT_STORAGE_KEY = "fleetly.console.context";

type StoredContext = { team: string | null; project: string | null };

function loadStoredContext(): StoredContext {
  try {
    const raw = localStorage.getItem(CONTEXT_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<StoredContext>;
      return {
        team: typeof parsed.team === "string" ? parsed.team : null,
        project: typeof parsed.project === "string" ? parsed.project : null,
      };
    }
  } catch {
    // 存储不可用/形态漂移——回落无选择（全量可见集）。
  }
  return { team: null, project: null };
}

export interface TeamProjectContextValue {
  /** 我所在团队（Me 投影；平台管理员 = 全部团队只读）。 */
  teams: TeamMembership[];
  /** 可见项目全集（团队选择后的子集由 selectedTeam 过滤）。 */
  projects: ProjectView[];
  /** 当前选中团队（slug 匹配；null = 未收窄）。 */
  selectedTeamSlug: string | null;
  /** 当前选中项目 slug（队内唯一；null = 未收窄——跨队随团队切换失效）。 */
  selectedProjectSlug: string | null;
  /** 当前选中项目的归属团队 slug（限定形第一段；无选择 = ""）。 */
  projectRef: string;
  /** 限定形 app 引用（有项目上下文 = team/prj/app，否则原名透传）。 */
  qualifyApp(name: string): string;
  setTeamSlug(slug: string | null): void;
  setProjectSlug(slug: string | null): void;
}

const noop = () => {};

/** 无 Provider 时的中性缺省（全量可见、零收窄）——直连页面测试复用。 */
const NeutralContext: TeamProjectContextValue = {
  teams: [],
  projects: [],
  selectedTeamSlug: null,
  selectedProjectSlug: null,
  projectRef: "",
  qualifyApp: (name: string) => name,
  setTeamSlug: noop,
  setProjectSlug: noop,
};

const TeamProjectContext = createContext<TeamProjectContextValue>(NeutralContext);

export function TeamProjectProvider({ children }: { children: ReactNode }) {
  const [selected, setSelected] = useState<StoredContext>(loadStoredContext);

  // Me 投影（与 user-menu 同键共享缓存）+ 可见项目集。静默失败 = 上下文
  // 降级为无选择（资源页全量可见集）——列表读面失败不阻塞页面。
  const meQuery = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => me(),
    staleTime: 5 * 60 * 1000,
    refetchOnWindowFocus: false,
    retry: false,
  });
  const projectsQuery = useQuery({
    queryKey: ["projects", "context"],
    queryFn: () => listProjects(),
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const teams = useMemo(() => meQuery.data?.teams ?? [], [meQuery.data]);
  const projects = useMemo(() => projectsQuery.data?.projects ?? [], [projectsQuery.data]);

  // 持久化落点：setter 内直写（不经 effect—— setState-in-effect 纪律）。
  const persist = useCallback((next: StoredContext) => {
    try {
      localStorage.setItem(CONTEXT_STORAGE_KEY, JSON.stringify(next));
    } catch {
      // 忽略：持久化失败只影响下次启动的记忆，不影响会话内行为。
    }
  }, []);

  const setTeamSlug = useCallback(
    (slug: string | null) => {
      // 切团队即失效项目选择（项目 slug 只在队内唯一）。
      const next = { team: slug, project: null };
      setSelected(next);
      persist(next);
    },
    [persist],
  );

  const setProjectSlug = useCallback(
    (slug: string | null) => {
      setSelected((prev) => {
        const next = { ...prev, project: slug };
        persist(next);
        return next;
      });
    },
    [persist],
  );

  // 团队选择漂移守卫：持久化的 slug 不在可见集（被移出团队/存储串台）→
  // 渲染期派生归位（不改持久化层——重新可见时记忆仍成立）。
  const effective = useMemo<StoredContext>(() => {
    if (selected.team && teams.length > 0 && !teams.some((t) => t.team_slug === selected.team)) {
      return { team: null, project: null };
    }
    return selected;
  }, [selected, teams]);

  const value = useMemo<TeamProjectContextValue>(() => {
    const selectedProject =
      effective.team && effective.project
        ? (projects.find(
            (p) => p.team_slug === effective.team && p.slug === effective.project,
          ) ?? null)
        : null;
    return {
      teams,
      projects,
      selectedTeamSlug: effective.team,
      selectedProjectSlug: effective.project,
      projectRef:
        effective.team && selectedProject ? `${effective.team}/${selectedProject.slug}` : "",
      qualifyApp: (name: string) =>
        effective.team && selectedProject
          ? `${effective.team}/${selectedProject.slug}/${name}`
          : name,
      setTeamSlug,
      setProjectSlug,
    };
  }, [teams, projects, effective, setTeamSlug, setProjectSlug]);

  return <TeamProjectContext.Provider value={value}>{children}</TeamProjectContext.Provider>;
}

export function useProjectContext(): TeamProjectContextValue {
  return useContext(TeamProjectContext);
}

// ── 角色能力（§3.2 矩阵的前端投影；仅体验门）────────────────────────────

/** 四档团队角色的蕴含排序（owner > admin > developer > viewer）。 */
const ROLE_RANK: Record<string, number> = {
  viewer: 0,
  developer: 1,
  admin: 2,
  owner: 3,
};

export function roleAtLeast(role: string | null | undefined, min: string): boolean {
  if (!role) return false;
  return (ROLE_RANK[role] ?? -1) >= (ROLE_RANK[min] ?? 99);
}

export interface TeamCapabilities {
  /** 我的团队角色（无团队上下文 = null——能力全关，服务端亦拒）。 */
  role: string | null;
  /** read 面：所有角色恒真（viewer 起步）。 */
  canRead: boolean;
  /** deploy 面：developer+（部署/回滚/env 写/cron/终端）。 */
  canDeploy: boolean;
  /** admin 面：admin+（app 删除/env 明文/secrets/构建/库生命周期）。 */
  canAdminResources: boolean;
  /** owner 面：成员/角色/邀请/项目创建与删除。 */
  canManageTeam: boolean;
  /** 邀请面：owner/admin（§3.1；所邀角色 ≤ 自身由服务端把关）。 */
  canInvite: boolean;
}

/** 能力全开（Me 投影不可用时的缺省——见 useTeamCapabilities 头注）。 */
const OPEN_CAPABILITIES: TeamCapabilities = {
  role: null,
  canRead: true,
  canDeploy: true,
  canAdminResources: true,
  canManageTeam: true,
  canInvite: true,
};

export function capabilitiesForRole(role: string | null | undefined): TeamCapabilities {
  return {
    role: role ?? null,
    canRead: roleAtLeast(role, "viewer"),
    canDeploy: roleAtLeast(role, "developer"),
    canAdminResources: roleAtLeast(role, "admin"),
    canManageTeam: roleAtLeast(role, "owner"),
    canInvite: roleAtLeast(role, "admin"),
  };
}

/**
 * 当前选中团队的能力视图（未选团队时回落「我所在唯一团队」——单队用户
 * （个人队 owner）零选择也有正确能力；多队未选 = 无能力，促显式选择）。
 *
 * fail-open 口径：Me 投影不可用（teams 空——接口失败/测试直挂页面）时
 * 返回能力全开。前端门只是体验优化，服务端角色门（ResolvePermission）
 * 才是硬门；无角色信息就隐藏写按钮只会制造「按钮无故消失」的错觉，方向
 * 性错误——如实全开，让 403 信封说话。
 */
export function useTeamCapabilities(): TeamCapabilities {
  const { teams, selectedTeamSlug } = useProjectContext();
  const role = useMemo(() => {
    if (selectedTeamSlug) {
      return teams.find((t) => t.team_slug === selectedTeamSlug)?.role ?? null;
    }
    if (teams.length === 1) return teams[0].role;
    return null;
  }, [teams, selectedTeamSlug]);
  if (teams.length === 0) return OPEN_CAPABILITIES;
  return capabilitiesForRole(role);
}

/** 平台管理员标志（Me 投影；透明度/平台管理面的导航与页面门）。 */
export function useIsPlatformAdmin(): boolean {
  const { data } = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => me(),
    staleTime: 5 * 60 * 1000,
    refetchOnWindowFocus: false,
    retry: false,
  });
  return data?.user?.is_platform_admin === true;
}

// 团队列表页（/teams，v0.3 W2-S5，rbac-teams 设计 §7）：我所在团队
// （Me 投影——角色徽章）+ 平台管理员的跨团队只读面（ListTeams 全量，
// support 视角——成员角色未知，仅列名与限定形 slug）。行点击进团队设置
// 页（成员/邀请/项目管理）。

import { useQuery } from "@tanstack/react-query";
import { ChevronRight, UsersRound } from "lucide-react";
import { Link } from "react-router-dom";

import { listTeams } from "@/api/endpoints";
import { PageHeader } from "@/components/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useIsPlatformAdmin, useProjectContext } from "@/lib/context";
import { timeAgo } from "@/lib/utils";

export function TeamsPage() {
  const { teams } = useProjectContext();
  const isPlatformAdmin = useIsPlatformAdmin();

  // 平台管理员的全量团队面（只读——写面 403；ListTeams 对管理员回全部）。
  const allTeamsQuery = useQuery({
    queryKey: ["teams", "all"],
    queryFn: listTeams,
    enabled: isPlatformAdmin,
    staleTime: 30_000,
  });
  const allTeams = allTeamsQuery.data?.teams ?? [];
  const mineIds = new Set(teams.map((t) => t.team_id));
  const others = allTeams.filter((t) => !mineIds.has(t.id ?? ""));

  return (
    <div className="space-y-4" data-testid="teams-page">
      <PageHeader
        title="Teams"
        description="Your teams and projects. Open a team to manage members, invites and project role overrides."
      />
      <Card>
        <CardContent className="p-0">
          {teams.length === 0 ? (
            <p className="p-5 text-sm text-muted-foreground">
              You are not a member of any team yet.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Team</TableHead>
                  <TableHead>Slug</TableHead>
                  <TableHead>My role</TableHead>
                  <TableHead className="w-10" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {teams.map((t) => (
                  <TableRow key={t.team_id} data-testid="team-row" data-slug={t.team_slug}>
                    <TableCell>
                      <Link
                        to={`/teams/${encodeURIComponent(t.team_id ?? "")}`}
                        className="flex items-center gap-2.5 font-medium hover:underline"
                      >
                        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted/40">
                          <UsersRound aria-hidden className="h-4 w-4 text-muted-foreground" />
                        </span>
                        {t.team_name || t.team_slug}
                      </Link>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {t.team_slug}
                    </TableCell>
                    <TableCell>
                      <span
                        data-testid="team-role-badge"
                        className="inline-flex items-center rounded-md border px-1.5 py-0.5 text-xs font-medium"
                      >
                        {t.role}
                      </span>
                    </TableCell>
                    <TableCell className="w-10 text-right">
                      <ChevronRight aria-hidden className="ml-auto h-4 w-4 text-muted-foreground/60" />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* 平台管理员跨团队展示（限定形 slug，D-W0-9；只读 support 视角）。 */}
      {isPlatformAdmin && others.length > 0 ? (
        <Card data-testid="teams-all">
          <CardContent className="p-0">
            <div className="border-b px-4 py-3 text-sm font-semibold">All teams (platform view)</div>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Team</TableHead>
                  <TableHead>Slug</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {others.map((t) => (
                  <TableRow key={t.id} data-testid="team-all-row">
                    <TableCell>
                      <Link
                        to={`/teams/${encodeURIComponent(t.id ?? "")}`}
                        className="font-medium hover:underline"
                      >
                        {t.name} <span className="font-mono text-xs text-muted-foreground">({t.slug})</span>
                      </Link>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">{t.slug}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{timeAgo(t.created_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      ) : null}
      {isPlatformAdmin ? (
        <p className="text-xs text-muted-foreground">
          Platform administrators have read-only access across teams; team writes require a
          member role (separation of duties).
        </p>
      ) : null}
      <Button asChild variant="ghost" size="sm">
        <Link to="/apps">Back to applications</Link>
      </Button>
    </div>
  );
}

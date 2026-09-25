// 项目一级页（/projects，用户裁决 2026-09-24「Projects 提为侧边栏一级入口」）：
// 项目此前只藏在 Teams → 团队设置 → Projects tab 里，可发现性差。本页跨团队
// 聚合可见项目（context 的 listProjects 全集——与顶栏切换器同数据源），行内
// 限定形 team/prj（D-W0-9）+ 我的生效角色（覆写行优先于团队角色，§3.3 B 形
// ——与 context.useTeamCapabilities 同判定式）+ 团队设置入口。创建卡片按
// 「目标团队 owner」渲染（§3.2：项目创建/删除 = owner；个人队注册即 owner，
// 单队用户零配置可见）。
//
// 本页只做读面聚合 + 创建；删除与项目覆写成员仍在团队设置页（owner 面）——
// 单一管理面，本页不复制危险操作。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FolderKanban, Loader2, Plus } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";

import { createProject } from "@/api/endpoints";
import { errorEnvelopeFrom, type ErrorEnvelope } from "@/api/errors";
import { EnvelopeAlert } from "@/components/envelope-alert";
import { PageHeader } from "@/components/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useProjectContext } from "@/lib/context";
import { formatTime } from "@/lib/utils";

export function ProjectsPage() {
  const { teams, projects, projectOverrides } = useProjectContext();

  // 生效角色：项目覆写行优先（team_id + prj_slug 命中），否则团队角色；
  // 无成员关系（平台管理员跨队只读视角）= null → 展示「—」。
  const roleFor = (teamId: string | undefined, slug: string | undefined) => {
    const override = projectOverrides.find(
      (o) => o.team_id === teamId && o.prj_slug === slug,
    );
    if (override) return override.role ?? null;
    return teams.find((t) => t.team_id === teamId)?.role ?? null;
  };

  // 创建目标团队 = 我任 owner 的团队（§3.2）。
  const ownedTeams = teams.filter((t) => t.role === "owner");

  const queryClient = useQueryClient();
  const [teamId, setTeamId] = useState("");
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<ErrorEnvelope | null>(null);

  const createMutation = useMutation({
    mutationFn: () =>
      createProject({
        team_id: teamId || ownedTeams[0]?.team_id || "",
        slug: slug.trim(),
        name: name.trim(),
        description,
      }),
    onSuccess: () => {
      setSlug("");
      setName("");
      setDescription("");
      setError(null);
      // ["projects"] 前缀失效：本页与切换器（context）+ 团队设置 tab 同刷。
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onCreate(e: FormEvent) {
    e.preventDefault();
    if (slug.trim() && name.trim()) createMutation.mutate();
  }

  return (
    <div className="space-y-4" data-testid="projects-page">
      <PageHeader
        title="Projects"
        description="Projects across your teams — the isolation unit for apps and databases. Team admins can scope member roles per project from a team's settings."
      />

      {ownedTeams.length > 0 ? (
        <Card data-testid="project-create">
          <CardHeader className="border-b pb-3">
            <CardTitle className="text-sm font-semibold">Create a project</CardTitle>
            <CardDescription className="mt-1">
              Requires owner in the target team (your personal team qualifies). Slug is word-only
              ([a-z0-9], 2–32 chars), immutable, and becomes the middle segment of the
              infrastructure naming formula.
            </CardDescription>
          </CardHeader>
          <form className="flex flex-wrap items-end gap-2 p-4" onSubmit={onCreate}>
            <div className="space-y-1.5">
              <Label htmlFor="project-team">Team</Label>
              {/* 原生 select——jsdom 可测性取舍（TeamPage 同款）。 */}
              <select
                id="project-team"
                data-testid="project-team-select"
                className="h-9 w-44 rounded-md border border-input bg-background px-3 text-sm"
                value={teamId || ownedTeams[0]?.team_id || ""}
                onChange={(e) => setTeamId(e.target.value)}
              >
                {ownedTeams.map((t) => (
                  <option key={t.team_id} value={t.team_id ?? ""}>
                    {t.team_name || t.team_slug} ({t.team_slug})
                  </option>
                ))}
              </select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="project-slug">Slug</Label>
              <Input
                id="project-slug"
                data-testid="project-slug-input"
                className="w-44 font-mono text-xs"
                placeholder="prod"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="project-name">Name</Label>
              <Input
                id="project-name"
                data-testid="project-name-input"
                className="w-56"
                placeholder="Production"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="project-description">Description</Label>
              <Input
                id="project-description"
                data-testid="project-description-input"
                className="w-72"
                placeholder="(optional)"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
            <Button
              type="submit"
              size="sm"
              data-testid="project-create-submit"
              disabled={!slug.trim() || !name.trim() || createMutation.isPending}
            >
              {createMutation.isPending ? (
                <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Plus aria-hidden className="h-3.5 w-3.5" />
              )}
              Create project
            </Button>
            <div className="basis-full">
              {error ? (
                <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
              ) : null}
            </div>
          </form>
        </Card>
      ) : null}

      <Card>
        <CardContent className="p-0">
          {projects.length === 0 ? (
            <p className="p-5 text-sm text-muted-foreground">
              No projects visible yet — projects you create or are granted access to appear here.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Project</TableHead>
                  <TableHead>Team</TableHead>
                  <TableHead>My role</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead>Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {projects.map((p) => {
                  const membership = teams.find((t) => t.team_id === p.team_id);
                  const role = roleFor(p.team_id, p.slug);
                  return (
                    <TableRow key={p.id} data-testid="project-row" data-slug={p.slug}>
                      <TableCell>
                        <div className="flex items-center gap-2.5 font-medium">
                          <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted/40">
                            <FolderKanban aria-hidden className="h-4 w-4 text-muted-foreground" />
                          </span>
                          {p.name}
                        </div>
                        {/* 跨团队展示限定形（D-W0-9）。 */}
                        <div className="font-mono text-xs text-muted-foreground">
                          {p.team_slug}/{p.slug}
                        </div>
                      </TableCell>
                      <TableCell>
                        {membership ? (
                          <Link
                            to={`/teams/${encodeURIComponent(membership.team_id ?? "")}`}
                            className="hover:underline"
                          >
                            {membership.team_name || membership.team_slug}
                          </Link>
                        ) : (
                          <span className="text-muted-foreground">{p.team_slug}</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {role ? (
                          <Badge variant="secondary" data-testid="project-role-badge">
                            {role}
                          </Badge>
                        ) : (
                          <span className="text-xs text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">{p.description}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {formatTime(p.created_at)}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

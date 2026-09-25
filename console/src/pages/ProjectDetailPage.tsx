// 项目详情页（/projects/:projectId，2026-09-25 用户裁决「项目怎么查看或
// 编辑详情」——此前 Projects 一级页只聚合+创建，项目信息无查看/编辑面）：
//   - Info 卡：slug（不可变）/created/描述；name+description 编辑（服务端
//     UpdateProject = 团队 owner 硬门，前端按成员角色渲染编辑钮——个人队
//     owner 天然可编辑；平台管理员非成员只读视角）；
//   - Applications 卡：项目内应用清单（listApps ?project=team/prj 收窄，
//     行链接 id 寻址进详情——同名应用安全）；
//   - Databases 卡：listDatabases ?project= 收窄（同 id 寻址）；
//   - 成员覆写管理在团队设置 Projects tab（单一管理面）——卡片底部链接。
// 面包屑经 ["project", id] 详情缓存反解项目名。

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Boxes,
  Database,
  Loader2,
  Pencil,
  Save,
} from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";

import { getProject, listApps, listDatabases, updateProject } from "@/api/endpoints";
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
import { formatTime, timeAgo } from "@/lib/utils";

export function ProjectDetailPage() {
  const { projectId = "" } = useParams();
  const { teams } = useProjectContext();
  const queryClient = useQueryClient();
  const [error, setError] = useState<ErrorEnvelope | null>(null);
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");

  const projectQuery = useQuery({
    queryKey: ["project", projectId],
    queryFn: () => getProject(projectId),
  });
  const project = projectQuery.data?.project;

  const updateMutation = useMutation({
    mutationFn: () => updateProject(projectId, { name: name.trim(), description }),
    onSuccess: () => {
      setEditing(false);
      setError(null);
      void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
      void queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
    onError: (err) => setError(errorEnvelopeFrom(err)),
  });

  function onSave(e: FormEvent) {
    e.preventDefault();
    if (name.trim()) updateMutation.mutate();
  }

  if (projectQuery.isPending) {
    return <p className="text-sm text-muted-foreground">Loading project…</p>;
  }
  if (projectQuery.isError || !project) {
    const envelope = errorEnvelopeFrom(projectQuery.error);
    return (
      <EnvelopeAlert
        code={envelope.code}
        message={envelope.message}
        suggestion={envelope.suggestion}
        docs={envelope.docs}
      />
    );
  }

  // 我的团队角色（Me 投影）：owner 才见编辑钮（服务端 UpdateProject 硬门
  // 同规）；非成员（平台管理员跨队视角）只读。
  const membership = teams.find((t) => t.team_id === project.team_id);
  const myRole = membership?.role ?? null;
  const canEdit = myRole === "owner";
  const qualified = `${project.team_slug}/${project.slug}`;

  return (
    <div className="space-y-4" data-testid="project-detail-page">
      <PageHeader
        title={project.name ?? project.slug ?? "Project"}
        description={
          <>
            <span className="font-mono">{qualified}</span>
            {membership ? (
              <>
                {" · "}
                <Link
                  to={`/teams/${encodeURIComponent(membership.team_id ?? "")}`}
                  className="hover:underline"
                >
                  team settings
                </Link>
                {" · "}
                <Link
                  to={`/teams/${encodeURIComponent(membership.team_id ?? "")}`}
                  className="hover:underline"
                >
                  members &amp; role overrides
                </Link>
              </>
            ) : (
              " · read-only (no team membership)"
            )}
          </>
        }
      />

      <Card data-testid="project-info-card">
        <CardHeader className="border-b pb-3">
          <CardTitle className="text-sm font-semibold">Project details</CardTitle>
          <CardDescription className="mt-1">
            Slug is immutable and the middle segment of the infrastructure naming formula.
            {canEdit
              ? " Name and description are editable (team owner)."
              : " Only the team owner can edit these fields."}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3 pt-4">
          {error ? (
            <EnvelopeAlert code={error.code} message={error.message} suggestion={error.suggestion} />
          ) : null}
          {editing ? (
            <form className="flex flex-wrap items-end gap-2" onSubmit={onSave}>
              <div className="space-y-1.5">
                <Label htmlFor="project-edit-name">Name</Label>
                <Input
                  id="project-edit-name"
                  data-testid="project-name-input"
                  className="w-56"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="project-edit-description">Description</Label>
                <Input
                  id="project-edit-description"
                  data-testid="project-description-input"
                  className="w-72"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>
              <Button
                type="submit"
                size="sm"
                data-testid="project-save"
                disabled={!name.trim() || updateMutation.isPending}
              >
                {updateMutation.isPending ? (
                  <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save aria-hidden className="h-3.5 w-3.5" />
                )}
                Save
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                data-testid="project-edit-cancel"
                onClick={() => setEditing(false)}
              >
                Cancel
              </Button>
            </form>
          ) : (
            <div className="grid gap-x-8 gap-y-2 text-sm sm:grid-cols-[10rem_1fr]">
              <span className="text-muted-foreground">Slug</span>
              <span className="font-mono text-xs">{project.slug}</span>
              <span className="text-muted-foreground">Name</span>
              <span data-testid="project-detail-name">{project.name}</span>
              <span className="text-muted-foreground">Description</span>
              <span data-testid="project-detail-description" className="text-muted-foreground">
                {project.description || "—"}
              </span>
              <span className="text-muted-foreground">Created</span>
              <span className="text-xs text-muted-foreground">{formatTime(project.created_at)}</span>
            </div>
          )}
          {canEdit && !editing ? (
            <Button
              variant="outline"
              size="sm"
              data-testid="project-edit-open"
              onClick={() => {
                setName(project.name ?? "");
                setDescription(project.description ?? "");
                setEditing(true);
              }}
            >
              <Pencil aria-hidden className="h-3.5 w-3.5" />
              Edit details
            </Button>
          ) : null}
        </CardContent>
      </Card>

      <ProjectApps teamSlug={project.team_slug ?? ""} slug={project.slug ?? ""} />
      <ProjectDatabases teamSlug={project.team_slug ?? ""} slug={project.slug ?? ""} />
    </div>
  );
}

/** 项目内应用清单（服务端 ?project= 收窄；行 id 寻址进详情）。 */
function ProjectApps({ teamSlug, slug }: { teamSlug: string; slug: string }) {
  const query = useQuery({
    queryKey: ["apps", `${teamSlug}/${slug}`],
    queryFn: () => listApps({ project: `${teamSlug}/${slug}` }),
  });
  const apps = query.data?.apps ?? [];

  return (
    <Card data-testid="project-apps-card">
      <CardHeader className="border-b pb-3">
        <CardTitle className="flex items-center gap-2 text-sm font-semibold">
          <Boxes aria-hidden className="h-4 w-4 text-muted-foreground" />
          Applications
        </CardTitle>
        <CardDescription className="mt-1">Apps deployed in this project.</CardDescription>
      </CardHeader>
      <CardContent className="p-0">
        {apps.length === 0 ? (
          <p className="p-5 text-sm text-muted-foreground">No applications in this project yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Application</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Updated</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {apps.map((a) => (
                <TableRow key={a.id} data-testid="project-app-row">
                  <TableCell>
                    <Link
                      to={`/apps/${encodeURIComponent(a.id ?? "")}`}
                      className="font-medium hover:underline"
                    >
                      {a.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{a.derived_state}</Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{timeAgo(a.updated_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

/** 项目内数据库清单（服务端 ?project= 收窄；行 id 寻址——同名库安全）。 */
function ProjectDatabases({ teamSlug, slug }: { teamSlug: string; slug: string }) {
  const query = useQuery({
    queryKey: ["databases", `${teamSlug}/${slug}`],
    queryFn: () => listDatabases({ project: `${teamSlug}/${slug}` }),
  });
  const dbs = query.data?.databases ?? [];

  return (
    <Card data-testid="project-databases-card">
      <CardHeader className="border-b pb-3">
        <CardTitle className="flex items-center gap-2 text-sm font-semibold">
          <Database aria-hidden className="h-4 w-4 text-muted-foreground" />
          Databases
        </CardTitle>
        <CardDescription className="mt-1">Managed databases in this project.</CardDescription>
      </CardHeader>
      <CardContent className="p-0">
        {dbs.length === 0 ? (
          <p className="p-5 text-sm text-muted-foreground">No databases in this project yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Instance</TableHead>
                <TableHead>Engine</TableHead>
                <TableHead>Status</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {dbs.map((d) => (
                <TableRow key={d.id} data-testid="project-db-row">
                  <TableCell>
                    <Link
                      to={`/databases/${encodeURIComponent(d.id ?? "")}`}
                      className="font-medium hover:underline"
                    >
                      {d.name}
                    </Link>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{d.template}</TableCell>
                  <TableCell>
                    <Badge variant="secondary">{d.status}</Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

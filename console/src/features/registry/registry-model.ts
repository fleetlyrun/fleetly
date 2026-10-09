import type { components } from "@/api/delivery";

type Deployment = components["schemas"]["v1Deployment"];
type Revision = components["schemas"]["v1Revision"];

// Registry v1 纯派生（IA v3 T5，§5.1）：按 app 聚"当前运行内容"——最新
// 一次部署的 revision digest（内容寻址锚）+ commit。image ref 在 spec 内
// 不可读（§4.1 同款通路缺口，二期），digest 是一期诚实的"跑的哪个"。

export function latestDeploymentPerApp(deployments: Deployment[] | undefined): Map<string, Deployment> {
  const byApp = new Map<string, Deployment>();
  for (const deployment of deployments ?? []) {
    const appId = deployment.app_id ?? "";
    if (appId === "") continue;
    const existing = byApp.get(appId);
    if (existing == null) byApp.set(appId, deployment);
  }
  return byApp;
}

export function digestByRevisionId(revisionsByApp: Array<Revision[] | undefined>): Map<string, string> {
  const index = new Map<string, string>();
  for (const revisions of revisionsByApp) {
    for (const revision of revisions ?? []) {
      if (revision.id != null && revision.digest) index.set(revision.id, revision.digest);
    }
  }
  return index;
}

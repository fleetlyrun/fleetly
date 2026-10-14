package cmd

// 按名 create-or-reuse 的查重单源（P0-7.5）：structure 面没有 GetByName
// 点查（Get 只按 id）——按名判存在必须经 List。此前调用点只拉首页
//（服务端缺省 50 条）线性找名：实体超过 50 时假阴性，误判"不存在"后
// 重复 Create 撞 E_ALREADY_EXISTS。此处统一游标全量收敛（after_*_seq +
// limit 200 = 服务端 ListPage 钳制上限），不新增能力面；ensure 族三处
//（quickstart/templates/dokploy）共用。

import (
	"context"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// listPageLimit 是按名查重的页大小（与服务端 ListPage 的 200 钳制上限
// 对齐——少一轮往返，也不越服务端回落值）。
const listPageLimit = 200

// findProjectByName 全平台按名找项目（quickstart/templates/dokploy 的
// project create-or-reuse 查重单源）。
func findProjectByName(ctx context.Context, c *sdk.Client, name string) (string, bool, error) {
	var after int64
	for {
		page, err := c.Projects.ListProjects(ctx, &structurev1.ListProjectsRequest{
			AfterProjectSeq: after, Limit: listPageLimit,
		})
		if err != nil {
			return "", false, err
		}
		for _, p := range page.GetProjects() {
			if p.GetName() == name {
				return p.GetId(), true, nil
			}
		}
		if len(page.GetProjects()) < listPageLimit {
			return "", false, nil // 尾页（不足一页）即收敛
		}
		after = page.GetProjects()[len(page.GetProjects())-1].GetSeq()
	}
}

// findAppByName 项目内按名找 App（app create-or-reuse 查重单源）。
func findAppByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, bool, error) {
	var after int64
	for {
		page, err := c.Apps.ListApps(ctx, &structurev1.ListAppsRequest{
			ProjectId: projectID, AfterAppSeq: after, Limit: listPageLimit,
		})
		if err != nil {
			return "", false, err
		}
		for _, a := range page.GetApps() {
			if a.GetName() == name {
				return a.GetId(), true, nil
			}
		}
		if len(page.GetApps()) < listPageLimit {
			return "", false, nil // 尾页（不足一页）即收敛
		}
		after = page.GetApps()[len(page.GetApps())-1].GetSeq()
	}
}

// findDatabaseByName 项目内按名找数据库（dokploy 的 database
// create-or-reuse 查重单源）。
func findDatabaseByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, bool, error) {
	var after int64
	for {
		page, err := c.Databases.ListDatabases(ctx, &structurev1.ListDatabasesRequest{
			ProjectId: projectID, AfterDatabaseSeq: after, Limit: listPageLimit,
		})
		if err != nil {
			return "", false, err
		}
		for _, d := range page.GetDatabases() {
			if d.GetName() == name {
				return d.GetId(), true, nil
			}
		}
		if len(page.GetDatabases()) < listPageLimit {
			return "", false, nil // 尾页（不足一页）即收敛
		}
		after = page.GetDatabases()[len(page.GetDatabases())-1].GetSeq()
	}
}

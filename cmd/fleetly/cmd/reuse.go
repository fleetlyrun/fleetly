package cmd

// 按名 create-or-reuse 的查重单源（P0-7.5 起；ADR-0092 起改走服务端
// Resolve 点查——退役客户端游标扫 List 形态：超过首页 50 条的假阴性
// 与全量拉取成本一并消失）。ensure 族三处（quickstart/templates/dokploy）
// 共用；语义 = 命中返回 id，NotFound 返回 ("", false, nil)，其余错误上抛。

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// findProjectByName 在调用方 team 作用域按名找项目（quickstart/templates/
// dokploy 的 project create-or-reuse 查重单源）。
func findProjectByName(ctx context.Context, c *sdk.Client, name string) (string, bool, error) {
	resp, err := c.Projects.ResolveProject(ctx, &structurev1.ResolveProjectRequest{Name: name})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return resp.GetProject().GetId(), true, nil
}

// findAppByName 项目内按名找 App（app create-or-reuse 查重单源）。
func findAppByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, bool, error) {
	id, err := resolveAppByName(ctx, c, projectID, name)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// findDatabaseByName 项目内按名找数据库（dokploy 的 database
// create-or-reuse 查重单源）。
func findDatabaseByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, bool, error) {
	id, err := resolveDatabaseByName(ctx, c, projectID, name)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

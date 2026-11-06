package cmd

// name 寻址的 CLI 边界解析单源（ADR-0092：name 在边界解析，id 在内核——
// 服务端 Resolve 五 RPC 是唯一真源，本层只做形态判别与转发）。三形态：
// 完整 id（26 位 ULID 短路直取）、裸 name（team/project 作用域内解析）、
// 复合 project/name（无兄弟旗标时的定位形态）。

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	automationv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/automation/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// mintedIDRe 是新形铸造 id 判据（与 internal/.../entityid.LooksMinted 同源
// 的 CLI 侧镜像——cmd 不依赖 internal 包，注释锚定单源）。
var mintedIDRe = regexp.MustCompile(`^[0-9a-hjkmnp-tv-z]{26}$`)

// resolveProjectID 把 project 引用（id|name）解析为 id（name 在调用方
// team 作用域内由服务端解析）。
func resolveProjectID(ctx context.Context, c *sdk.Client, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	resp, err := c.Projects.ResolveProject(ctx, &structurev1.ResolveProjectRequest{Name: ref})
	if err != nil {
		return "", err
	}
	return resp.GetProject().GetId(), nil
}

// resolveAppID 把 app 引用（id | name（需 projectFlag）| project/name 复合）
// 解析为 id。
func resolveAppID(ctx context.Context, c *sdk.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolveAppByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("app name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolveAppByName(ctx, c, projectID, ref)
}

func resolveAppByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, error) {
	resp, err := c.Apps.ResolveApp(ctx, &structurev1.ResolveAppRequest{Project: projectID, Name: name})
	if err != nil {
		return "", err
	}
	return resp.GetApp().GetId(), nil
}

// resolveDatabaseID 把 database 引用（id | name（需 projectFlag）|
// project/name 复合）解析为 id。
func resolveDatabaseID(ctx context.Context, c *sdk.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolveDatabaseByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("database name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolveDatabaseByName(ctx, c, projectID, ref)
}

// resolvePubSubID 解析 pubsub 引用（id 直通 / project/name / --project +
// name；resolveDatabaseID 同形，B4b）。
func resolvePubSubID(ctx context.Context, c *sdk.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolvePubSubByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("pubsub name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolvePubSubByName(ctx, c, projectID, ref)
}

func resolvePubSubByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, error) {
	resp, err := c.PubSubs.ResolvePubSub(ctx, &structurev1.ResolvePubSubRequest{Project: projectID, Name: name})
	if err != nil {
		return "", err
	}
	return resp.GetPubsub().GetId(), nil
}

func resolveDatabaseByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, error) {
	resp, err := c.Databases.ResolveDatabase(ctx, &structurev1.ResolveDatabaseRequest{Project: projectID, Name: name})
	if err != nil {
		return "", err
	}
	return resp.GetDatabase().GetId(), nil
}

// resolveTaskID 把 task 引用（id | name（需 projectFlag）| project/name
// 复合）解析为 id。
func resolveTaskID(ctx context.Context, c *sdk.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolveTaskByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("task name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolveTaskByName(ctx, c, projectID, ref)
}

func resolveTaskByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, error) {
	resp, err := c.Tasks.ResolveTask(ctx, &automationv1.ResolveTaskRequest{Project: projectID, Name: name})
	if err != nil {
		return "", err
	}
	return resp.GetTask().GetId(), nil
}

// resolveScheduleID 把 schedule 引用（id | name（需 projectFlag）|
// project/name 复合）解析为 id。
func resolveScheduleID(ctx context.Context, c *sdk.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolveScheduleByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("schedule name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolveScheduleByName(ctx, c, projectID, ref)
}

func resolveScheduleByName(ctx context.Context, c *sdk.Client, projectID, name string) (string, error) {
	resp, err := c.Schedules.ResolveSchedule(ctx, &automationv1.ResolveScheduleRequest{Project: projectID, Name: name})
	if err != nil {
		return "", err
	}
	return resp.GetSchedule().GetId(), nil
}

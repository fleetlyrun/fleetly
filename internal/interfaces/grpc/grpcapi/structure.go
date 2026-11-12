package grpcapi

import (
	"context"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
)

// RequireActiveProject 在调用方事务内校验 Project 存活（ADR-0023 活跃行
// 口径：tombstone 后一律不存在）。
func RequireActiveProject(ctx context.Context, st transact.Stores, id string) error {
	p, err := st.Projects.Get(ctx, id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return apperr.New("E_NOT_FOUND", "project %s not found", id)
		}
		return err
	}
	if p.Deleted() {
		return apperr.New("E_NOT_FOUND", "project %s not found", id)
	}
	return nil
}

// RequireProjectApp 在调用方事务内校验 App 在指定 Project 内存活（Route
// 的父引用面：apps 活跃行口径 + 归属一致——跨项目悬空引用按"本项目无此
// App"拒绝）。
func RequireProjectApp(ctx context.Context, st transact.Stores, projectID, appID string) error {
	a, err := st.Apps.Get(ctx, appID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return apperr.New("E_NOT_FOUND", "app %s not found in project %s", appID, projectID)
		}
		return err
	}
	if a.ProjectID != projectID {
		return apperr.New("E_NOT_FOUND", "app %s not found in project %s", appID, projectID)
	}
	return nil
}

// RejectActiveApps 是活跃 App 拒绝判定的核（repo 注入——事务内传
// st.Apps、DeleteProject 入口无锁预检传 db 绑定面，同一文案单源）。
func RejectActiveApps(ctx context.Context, apps app.Repo, projectID string) error {
	rows, err := apps.ListByProject(ctx, projectID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return apperr.New("E_CONFLICT",
			"project %s still holds %d app(s); delete them before deleting the project",
			projectID, len(rows))
	}
	return nil
}

package state

import (
	"context"
	"errors"
	"testing"
)

// tombstone 语义（state-model §2.6 + 冻结清单 §2.3「不复活测试」）：
// 删除 = deleting → deleted 状态位；恢复（DB 回填/同名重建尝试）不得把
// deleted 行翻回 active。
func TestAppTombstoneLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	app, err := st.CreateApp(ctx, "", "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if app.Lifecycle != LifecycleActive {
		t.Fatalf("new app lifecycle = %s, want active", app.Lifecycle)
	}

	// 非法迁移：active 直达 deleted 被拒绝（必须经 deleting，防跳过清理）。
	if err := st.MarkAppDeleted(ctx, app.ID); !errors.Is(err, ErrInvalidLifecycleTransition) {
		t.Fatalf("active→deleted must be rejected, got: %v", err)
	}

	if err := st.MarkAppDeleting(ctx, app.ID); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	got, err := st.GetAppByName(ctx, "demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Lifecycle != LifecycleDeleting || got.DeletingAt.IsZero() {
		t.Fatalf("lifecycle = %s deleting_at zero=%v, want deleting with stamp", got.Lifecycle, got.DeletingAt.IsZero())
	}

	// 重复进入 deleting 被拒绝。
	if err := st.MarkAppDeleting(ctx, app.ID); !errors.Is(err, ErrInvalidLifecycleTransition) {
		t.Fatalf("deleting→deleting must be rejected, got: %v", err)
	}

	if err := st.MarkAppDeleted(ctx, app.ID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	got, err = st.GetAppByName(ctx, "demo")
	if err != nil {
		t.Fatalf("get app after delete: %v", err)
	}
	if got.Lifecycle != LifecycleDeleted || got.DeletedAt.IsZero() {
		t.Fatalf("lifecycle = %s deleted_at zero=%v, want deleted with stamp", got.Lifecycle, got.DeletedAt.IsZero())
	}
}

// TestAppTombstoneNotResurrected 「恢复不复活」：同名重建与状态位覆盖
// 均不得把 deleted 翻回 active——deleted 名字在保留期内仍被 tombstone
// 占用（恢复流程若按 DB 回填重放，也只能看到 deleted 行）。
func TestAppTombstoneNotResurrected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	app, err := st.CreateApp(ctx, "", "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := st.MarkAppDeleting(ctx, app.ID); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if err := st.MarkAppDeleted(ctx, app.ID); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}

	// 同名 CreateApp：拒绝（ErrAppExists），且绝不复用/改写 tombstone 行。
	if _, err := st.CreateApp(ctx, "", "demo"); !errors.Is(err, ErrAppExists) {
		t.Fatalf("recreate over tombstone must return ErrAppExists, got: %v", err)
	}
	got, err := st.GetAppByName(ctx, "demo")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Lifecycle != LifecycleDeleted {
		t.Fatalf("tombstone resurrected: lifecycle = %s, want deleted", got.Lifecycle)
	}
	if got.ID != app.ID {
		t.Fatalf("row id changed: %s -> %s", app.ID, got.ID)
	}

	// 同 id 的 deleted 行重复进入 deleting/deleted 同样被拒绝（状态机
	// 单向：恢复路径不能通过生命周期接口翻回 active）。
	if err := st.MarkAppDeleting(ctx, app.ID); !errors.Is(err, ErrInvalidLifecycleTransition) {
		t.Fatalf("deleted→deleting must be rejected, got: %v", err)
	}
}

// TestAppNameOccupiedWhileDeleting deleting 名字同样占用。
func TestAppNameOccupiedWhileDeleting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app, err := st.CreateApp(ctx, "", "demo")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := st.MarkAppDeleting(ctx, app.ID); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	if _, err := st.CreateApp(ctx, "", "demo"); !errors.Is(err, ErrAppExists) {
		t.Fatalf("create while deleting must be ErrAppExists, got: %v", err)
	}
}

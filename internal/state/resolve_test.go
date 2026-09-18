package state

import (
	"context"
	"errors"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// 写前直读（state-model §2.2 读契约）：令牌一致放行、不一致/对象消失
// 返回 E_STATE_VERSION_CONFLICT（409）。
func TestResolveVersion(t *testing.T) {
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 5)
	fake.versions["service:fleetly-app-web"] = 9
	r := NewVersionResolver(fake)
	ctx := context.Background()

	// 一致令牌：放行。
	v, err := r.ResolveVersion(ctx, ObjectKindNode, "swarm-a")
	if err != nil {
		t.Fatalf("resolve node: %v", err)
	}
	if v.Index != 5 {
		t.Fatalf("node version = %d, want 5", v.Index)
	}
	if err := r.CheckVersion(ctx, ObjectKindNode, "swarm-a", ObjectVersion{Index: 5}); err != nil {
		t.Fatalf("check with fresh token: %v", err)
	}

	// 过期令牌：409 冲突（context 携带 kind/id/reason）。
	err = r.CheckVersion(ctx, ObjectKindNode, "swarm-a", ObjectVersion{Index: 4})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("stale token must be *apperr.Error, got %T: %v", err, err)
	}
	if appErr.Code() != "E_STATE_VERSION_CONFLICT" || appErr.HTTPStatus() != 409 {
		t.Fatalf("code/status = %s/%d, want E_STATE_VERSION_CONFLICT/409", appErr.Code(), appErr.HTTPStatus())
	}
	if appErr.Context()["reason"] != "version_mismatch" || appErr.Context()["id"] != "swarm-a" {
		t.Fatalf("conflict context = %+v", appErr.Context())
	}

	// 对象消失：同为 409 冲突（reason=object_not_found），不发明清单外码。
	err = r.CheckVersion(ctx, ObjectKindService, "gone-service", ObjectVersion{Index: 1})
	appErr = nil
	if !errors.As(err, &appErr) {
		t.Fatalf("missing object must be *apperr.Error, got %T: %v", err, err)
	}
	if appErr.Code() != "E_STATE_VERSION_CONFLICT" || appErr.HTTPStatus() != 409 {
		t.Fatalf("code/status = %s/%d, want E_STATE_VERSION_CONFLICT/409", appErr.Code(), appErr.HTTPStatus())
	}
	if appErr.Context()["reason"] != "object_not_found" {
		t.Fatalf("conflict context = %+v", appErr.Context())
	}

	// 未知 kind：普通错误（不是契约冲突）。
	if _, err := r.ResolveVersion(ctx, ObjectKind("volume"), "v1"); err == nil ||
		errors.As(err, &appErr) {
		t.Fatalf("unknown kind must be plain error, got: %v", err)
	}

	// service kind 直读。
	v, err = r.ResolveVersion(ctx, ObjectKindService, "fleetly-app-web")
	if err != nil || v.Index != 9 {
		t.Fatalf("service resolve = %d err=%v, want 9 nil", v.Index, err)
	}
}

// TestResolveVersionNoCachePollution 直读端口只读底座，不触碰观测缓存表
// （决策路径禁读缓存，D17）。
func TestResolveVersionNoCachePollution(t *testing.T) {
	st := newTestStore(t)
	fake := newFakeDocker()
	fake.addNode("swarm-a", "node-a", "ready", 5)
	r := NewVersionResolver(fake)
	ctx := context.Background()

	if _, err := r.ResolveVersion(ctx, ObjectKindNode, "swarm-a"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rows, err := st.ListCachedNodes(ctx)
	if err != nil {
		t.Fatalf("list cache: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("resolve must not write observation cache, got %d rows", len(rows))
	}
}

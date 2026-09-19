package state

// 发布状态机转移表的 state 侧穷举测试（S16-C5）：合法集合与引擎写点一一
// 对应；UpdateDeployment 的 CAS 分支按表拒写非法转移（含终态出边——
// ErrIllegalTransition 是 ErrDeploymentStateTransition 的特化，双哨兵皆真）。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTransitionMatrixExhaustive(t *testing.T) {
	// 合法转移全集（release-semantics §2.3：building 直通由 preparing→
	// releasing 边承载；observing 不可取消；引擎写点枚举见 machine.go 头注）。
	legal := map[DeploymentStatus]map[DeploymentStatus]bool{
		DeployQueued:    {DeployPreparing: true, DeployFailed: true, DeployCancelled: true},
		DeployPreparing: {DeployBuilding: true, DeployReleasing: true, DeployFailed: true, DeployCancelled: true},
		DeployBuilding:  {DeployReleasing: true, DeployFailed: true, DeployCancelled: true},
		DeployReleasing: {DeployObserving: true, DeployFailed: true, DeployCancelled: true},
		DeployObserving: {DeploySucceeded: true, DeployFailed: true},
		DeploySucceeded: {},
		DeployFailed:    {},
		DeployCancelled: {},
	}
	for _, from := range AllDeploymentStatuses() {
		for _, to := range AllDeploymentStatuses() {
			if got := CanTransitionDeployment(from, to); got != legal[from][to] {
				t.Fatalf("CanTransitionDeployment(%s, %s) = %v, want %v", from, to, got, legal[from][to])
			}
		}
	}
	// 未知状态一律非法。
	if CanTransitionDeployment(DeploymentStatus("bogus"), DeployQueued) ||
		CanTransitionDeployment(DeployQueued, DeploymentStatus("bogus")) {
		t.Fatal("unknown status should be illegal")
	}
}

// TestUpdateDeploymentRejectsIllegalTransition S16-C5 机制验收：CAS 前按
// 转移表校验——表外组合（如 observing → queued）即使 from 谓词与当前行
// 一致也拒写（行未被改动），返回 ErrIllegalTransition（且属
// ErrDeploymentStateTransition 家族——引擎既有幂等收敛分支不变）。
func TestUpdateDeploymentRejectsIllegalTransition(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "machine.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	app, err := st.CreateApp(ctx, "", "machine-app")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	rec, err := st.CreateDeployment(ctx, DeployRecord{AppID: app.ID, AppName: app.Name, Kind: "deploy"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	// 推进到 observing（合法链：queued → preparing → releasing → observing）。
	for _, step := range [][2]DeploymentStatus{
		{DeployQueued, DeployPreparing},
		{DeployPreparing, DeployReleasing},
		{DeployReleasing, DeployObserving},
	} {
		from, to := step[0], step[1]
		if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &to, PrevStatus: &from}); err != nil {
			t.Fatalf("legal transition %s -> %s: %v", from, to, err)
		}
	}

	// 非法转移（表外组合：observing → queued；from 谓词与当前行一致）→ 拒写。
	queued := DeployQueued
	observing := DeployObserving
	err = st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &queued, PrevStatus: &observing})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("observing -> queued err = %v, want ErrIllegalTransition", err)
	}
	if !errors.Is(err, ErrDeploymentStateTransition) {
		t.Fatalf("ErrIllegalTransition 应属 ErrDeploymentStateTransition 家族: %v", err)
	}
	// 行未被改动（仍 observing）。
	row, err := st.GetDeployment(ctx, rec.ID)
	if err != nil || row.Status != DeployObserving {
		t.Fatalf("row after rejected write = %+v err=%v, want observing", row, err)
	}

	// 终态出边同样被表拒绝（succeeded → failed，from 谓词构造终态）。
	succ, failed := DeploySucceeded, DeployFailed
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &succ, PrevStatus: &observing}); err != nil {
		t.Fatalf("observing -> succeeded: %v", err)
	}
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &failed, PrevStatus: &succ}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("succeeded -> failed err = %v, want ErrIllegalTransition", err)
	}
	// 未知目标状态拒绝（词表外——不落库即拒，无需 CAS 谓词）。
	bogus := DeploymentStatus("bogus")
	if err := st.UpdateDeployment(ctx, rec.ID, DeploymentPatch{Status: &bogus}); err == nil {
		t.Fatal("unknown target status accepted")
	}
}

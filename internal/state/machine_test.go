package state

// 发布状态机转移表的 state 侧穷举测试（S16-C5）：合法集合与引擎写点一一
// 对应；UpdateDeployment 的 CAS 分支按表拒写非法转移（含终态出边——
// ErrIllegalTransition 是 ErrDeploymentStateTransition 的特化，双哨兵皆真）。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
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
		t.Fatalf("ErrIllegalTransition should belong to the ErrDeploymentStateTransition family: %v", err)
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

// TestEnterPhaseSingleWritePoint T0-V2.2 机制验收：EnterPhase 单写点同事务
// 完成转移校验 + 字段写 + 拾取锚点 + 事件——
//  1. 拾取边（queued→preparing）携带锚点与事件：状态/锚点/事件三者同拍
//     落库（H11/S9 预算基线与转换原子）；
//  2. 非拾取边不刷新锚点（preparing→building 共用拾取基线——写点若在
//     building 入口重摆锚点即私自续预算，违反 S9）；
//  3. 拾取边缺锚：拒写（锚点漏写因单写点不可发生），行与事件均未动；
//  4. 表外组合：拒写且**不产生事件**（事务回滚——事件披露与转换落库
//     原子，杜绝「转换被拒、事件照发」的假信号）；
//  5. CAS 竞争落败：ErrDeploymentStateTransition 且无事件（同上）。
func TestEnterPhaseSingleWritePoint(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "enterphase.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	app, err := st.CreateApp(ctx, "", "enterphase-app")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	newRec := func() DeployRecord {
		t.Helper()
		rec, err := st.CreateDeployment(ctx, DeployRecord{AppID: app.ID, AppName: app.Name, Kind: "deploy"})
		if err != nil {
			t.Fatalf("create deployment: %v", err)
		}
		return rec
	}
	// 锚点由调用方时钟提供（引擎单测的假时钟下，写点自取墙钟会错位预算
	// 基准——契约见 Store.EnterPhase 注 3）。
	anchor := time.Unix(0, time.Now().UTC().UnixNano()).UTC()
	pickupEvent := func(id string) Event {
		return Event{Name: "deployment.release_started", Subject: "deployment:" + id}
	}

	// 1. 合法拾取边：锚点 + 状态 + 事件同一事务落库。
	rec := newRec()
	if err := st.EnterPhase(ctx, rec.ID, DeployQueued, DeployPreparing,
		DeploymentPatch{PhaseStartedAt: &anchor}, pickupEvent(rec.ID)); err != nil {
		t.Fatalf("enter preparing: %v", err)
	}
	row, err := st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if row.Status != DeployPreparing || !row.PhaseStartedAt.Equal(anchor) {
		t.Fatalf("row status=%s anchor=%v, want preparing/%v (same-transaction anchor write)", row.Status, row.PhaseStartedAt, anchor)
	}
	if names := eventNames(t, st); len(names) != 1 || names[0] != "deployment.release_started" {
		t.Fatalf("events = %v, want exactly [deployment.release_started]", names)
	}

	// 2. 非拾取边不刷新锚点：preparing→building 走写点、补丁不携带锚点，
	// 状态推进而基线保持拾取时刻。
	if err := st.EnterPhase(ctx, rec.ID, DeployPreparing, DeployBuilding, DeploymentPatch{}); err != nil {
		t.Fatalf("enter building: %v", err)
	}
	row, err = st.GetDeployment(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if row.Status != DeployBuilding || !row.PhaseStartedAt.Equal(anchor) {
		t.Fatalf("row status=%s anchor=%v, want building/%v (anchor must not be re-armed outside the pickup edge)",
			row.Status, row.PhaseStartedAt, anchor)
	}

	// 3. 拾取边缺锚：拒写（行保持 queued、无新事件）。
	rec2 := newRec()
	if err := st.EnterPhase(ctx, rec2.ID, DeployQueued, DeployPreparing, DeploymentPatch{}); err == nil {
		t.Fatal("queued -> preparing without phase_started_at anchor must be rejected (S9)")
	}
	if row2, err := st.GetDeployment(ctx, rec2.ID); err != nil || row2.Status != DeployQueued {
		t.Fatalf("row after rejected anchorless pickup = %+v err=%v, want queued untouched", row2, err)
	}
	if names := eventNames(t, st); len(names) != 1 {
		t.Fatalf("events after rejections = %v, want unchanged (no event without a committed transition)", names)
	}

	// 4. 表外组合（queued→queued 无边；事件入参随事务回滚）：拒写、无事件。
	if err := st.EnterPhase(ctx, rec2.ID, DeployQueued, DeployQueued, DeploymentPatch{},
		pickupEvent(rec2.ID)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("queued -> queued err = %v, want ErrIllegalTransition", err)
	}
	if names := eventNames(t, st); len(names) != 1 {
		t.Fatalf("events after illegal transition = %v, want unchanged (same-transaction rollback)", names)
	}

	// 5. CAS 竞争落败（合法边但行已推进）：家族哨兵、无事件。
	if err := st.EnterPhase(ctx, rec2.ID, DeployQueued, DeployPreparing,
		DeploymentPatch{PhaseStartedAt: &anchor}, pickupEvent(rec2.ID)); err != nil {
		t.Fatalf("enter preparing rec2: %v", err)
	}
	if err := st.EnterPhase(ctx, rec2.ID, DeployQueued, DeployPreparing,
		DeploymentPatch{PhaseStartedAt: &anchor}, pickupEvent(rec2.ID)); !errors.Is(err, ErrDeploymentStateTransition) {
		t.Fatalf("duplicate pickup err = %v, want ErrDeploymentStateTransition", err)
	}
	if names := eventNames(t, st); len(names) != 2 {
		t.Fatalf("events = %v, want 2 (one release_started per committed pickup, none for the lost race)", names)
	}
}

// eventNames 返回库内事件名序列（断言辅助）。
func eventNames(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, e := range rows {
		out = append(out, e.Name)
	}
	return out
}

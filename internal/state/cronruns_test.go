package state

// cron_runs 台账读写通道测试（E5 Cron）：CRUD、在途谓词、收口幂等、读面
// 收窄与留存窗（每 schedule 最近 20 条——janitor 增项的执行体）。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func createAppRow(t *testing.T, st *Store) App {
	t.Helper()
	app, err := seedAppE(t, st, "cronapp")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app
}

// TestCronRunLifecycle 在途 → 终态收口的全链：started 行入账、在途扫描可
// 见、FinishCronRun 置终态、二次收口 ErrCronRunNotStarted（幂等收敛谓词）。
func TestCronRunLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createAppRow(t, st)

	var created CronRun
	err := st.InTx(ctx, func(tx *Tx) error {
		r, err := tx.CreateCronRun(ctx, CronRun{
			AppID:       app.ID,
			Service:     "task",
			Expression:  "* * * * *",
			ScheduledAt: time.Unix(1700000000, 0).UTC(),
			StartedAt:   time.Unix(1700000001, 0).UTC(),
			Status:      CronRunStarted,
			JobService:  "fleetly-cron-cronapp-task-abc",
		})
		created = r
		return err
	})
	if err != nil {
		t.Fatalf("create cron run: %v", err)
	}
	if created.ID == "" {
		t.Fatal("run id not assigned")
	}

	inflight, err := st.ListInFlightCronRuns(ctx)
	if err != nil || len(inflight) != 1 {
		t.Fatalf("in-flight scan = %d rows, err %v", len(inflight), err)
	}
	if _, ok, err := st.LatestInFlightCronRun(ctx, app.ID, "task"); err != nil || !ok {
		t.Fatalf("latest in-flight missing: ok=%v err=%v", ok, err)
	}

	err = st.InTx(ctx, func(tx *Tx) error {
		return tx.FinishCronRun(ctx, created.ID, CronRunSucceeded, "", time.Unix(1700000060, 0).UTC())
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	// 二次收口：行已终态 → ErrCronRunNotStarted。
	err = st.InTx(ctx, func(tx *Tx) error {
		return tx.FinishCronRun(ctx, created.ID, CronRunFailed, "again", time.Unix(1700000061, 0).UTC())
	})
	if !errors.Is(err, ErrCronRunNotStarted) {
		t.Fatalf("second finish err = %v, want ErrCronRunNotStarted", err)
	}
	rows, err := st.ListCronRuns(ctx, app.ID, "task", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %d rows err=%v", len(rows), err)
	}
	if rows[0].Status != CronRunSucceeded || rows[0].Error != "" || rows[0].FinishedAt.IsZero() {
		t.Fatalf("finished row wrong: %+v", rows[0])
	}
}

// TestCronRunSkipRow skipped 行（无 started_at/finished_at/job_service）。
func TestCronRunSkipRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createAppRow(t, st)
	err := st.InTx(ctx, func(tx *Tx) error {
		_, err := tx.CreateCronRun(ctx, CronRun{
			AppID:       app.ID,
			Service:     "task",
			Expression:  "* * * * *",
			ScheduledAt: time.Unix(1700000000, 0).UTC(),
			Status:      CronRunSkipped,
			SkipReason:  CronSkipOverlap,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create skip row: %v", err)
	}
	inflight, err := st.ListInFlightCronRuns(ctx)
	if err != nil || len(inflight) != 0 {
		t.Fatalf("skipped row must not be in-flight: %d rows err=%v", len(inflight), err)
	}
	rows, err := st.ListCronRuns(ctx, app.ID, "", 10)
	if err != nil || len(rows) != 1 || rows[0].SkipReason != CronSkipOverlap {
		t.Fatalf("skip row readback wrong: %+v err=%v", rows, err)
	}
}

// TestCronRunListFilters service 收窄与 limit。
func TestCronRunListFilters(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createAppRow(t, st)
	err := st.InTx(ctx, func(tx *Tx) error {
		for _, svc := range []string{"task", "task", "other"} {
			if _, err := tx.CreateCronRun(ctx, CronRun{
				AppID:       app.ID,
				Service:     svc,
				Expression:  "* * * * *",
				ScheduledAt: time.Now().UTC(),
				Status:      CronRunSucceeded,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	all, err := st.ListCronRuns(ctx, app.ID, "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("all = %d rows err=%v", len(all), err)
	}
	narrow, err := st.ListCronRuns(ctx, app.ID, "other", 10)
	if err != nil || len(narrow) != 1 {
		t.Fatalf("narrowed = %d rows err=%v", len(narrow), err)
	}
	capped, err := st.ListCronRuns(ctx, app.ID, "", 2)
	if err != nil || len(capped) != 2 {
		t.Fatalf("capped = %d rows err=%v", len(capped), err)
	}
}

// TestCronRunRetentionKeepPerSchedule 留存窗：每 (app, service) 保留
// scheduled_at 最新的 20 条，更旧的删行；其他 schedule 不受牵连。
func TestCronRunRetentionKeepPerSchedule(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createAppRow(t, st)
	err := st.InTx(ctx, func(tx *Tx) error {
		base := time.Unix(1700000000, 0).UTC()
		for i := 0; i < 25; i++ {
			if _, err := tx.CreateCronRun(ctx, CronRun{
				AppID:       app.ID,
				Service:     "task",
				Expression:  "* * * * *",
				ScheduledAt: base.Add(time.Duration(i) * time.Minute),
				Status:      CronRunSucceeded,
			}); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if _, err := tx.CreateCronRun(ctx, CronRun{
				AppID:       app.ID,
				Service:     "other",
				Expression:  "* * * * *",
				ScheduledAt: base.Add(time.Duration(i) * time.Minute),
				Status:      CronRunSucceeded,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := st.PruneCronRunsKeepPerSchedule(ctx, CronRunKeeper)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 5 {
		t.Fatalf("pruned %d rows, want 5", n)
	}
	kept, err := st.ListCronRuns(ctx, app.ID, "task", 100)
	if err != nil || len(kept) != CronRunKeeper {
		t.Fatalf("kept %d rows, want %d (err %v)", len(kept), CronRunKeeper, err)
	}
	// 最新 20 条保留：最旧行（第一条）被删。
	if !kept[len(kept)-1].ScheduledAt.After(time.Unix(1700000004, 0).UTC()) {
		t.Fatalf("oldest kept row is not past the eviction boundary: %+v", kept[len(kept)-1].ScheduledAt)
	}
	other, err := st.ListCronRuns(ctx, app.ID, "other", 100)
	if err != nil || len(other) != 3 {
		t.Fatalf("other schedule touched: %d rows err=%v", len(other), err)
	}
}

// TestJanitorPrunesCronRuns janitor 一轮清理覆盖 cron_runs 留存窗。
func TestJanitorPrunesCronRuns(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	app := createAppRow(t, st)
	err := st.InTx(ctx, func(tx *Tx) error {
		base := time.Unix(1700000000, 0).UTC()
		for i := 0; i < CronRunKeeper+7; i++ {
			if _, err := tx.CreateCronRun(ctx, CronRun{
				AppID:       app.ID,
				Service:     "task",
				Expression:  "* * * * *",
				ScheduledAt: base.Add(time.Duration(i) * time.Minute),
				Status:      CronRunSucceeded,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	jr := NewJanitor(st, JanitorConfig{}, testLogger())
	if _, _, err := jr.PruneOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("prune once: %v", err)
	}
	kept, err := st.ListCronRuns(ctx, app.ID, "", 100)
	if err != nil || len(kept) != CronRunKeeper {
		t.Fatalf("janitor kept %d rows, want %d (err %v)", len(kept), CronRunKeeper, err)
	}
}

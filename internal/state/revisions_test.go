package state

// revisions 保留窗测试（T2.12）：最近 5 次成功部署可回滚、第 6 个固化时
// 最旧翻 superseded（不物理删）、列表即选项（active 集可回滚）、跨 app
// 目标解析拒绝。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openRevisionStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func createAppNamed(t *testing.T, st *Store, name string) App {
	t.Helper()
	app, err := seedAppE(t, st, name)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app
}

func createRevisionN(t *testing.T, st *Store, appID string, n int) []Revision {
	t.Helper()
	out := make([]Revision, 0, n)
	for i := 0; i < n; i++ {
		letter := "abcdef"[i : i+1]
		var rev Revision
		err := st.InTx(context.Background(), func(tx *Tx) error {
			r, err := tx.CreateRevision(context.Background(), RevisionWrite{
				AppID:             appID,
				ComposeNormalized: `{"rev":"` + letter + `"}`,
				Overlay:           "{}",
				DesiredHash:       "hash-" + letter,
			})
			rev = r
			return err
		})
		if err != nil {
			t.Fatalf("create revision %d: %v", i+1, err)
		}
		out = append(out, rev)
	}
	return out
}

func TestRevisionRetentionWindowTrimsOldest(t *testing.T) {
	st := openRevisionStore(t)
	app := createAppNamed(t, st, "demo")
	revs := createRevisionN(t, st, app.ID, 6)

	if len(revs) != 6 || revs[0].Seq != 1 || revs[5].Seq != 6 {
		t.Fatalf("unexpected revision seqs: %d", len(revs))
	}
	// 保留窗：只有最近 5 个 active（seq 6..2，降序）。
	rows, err := st.ListRevisions(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(rows) != RevisionKeepVersions {
		t.Fatalf("list = %d rows, want %d (retention window trimming)", len(rows), RevisionKeepVersions)
	}
	if rows[0].Seq != 6 || rows[0].Status != RevisionStatusActive {
		t.Fatalf("newest = seq %d status %s, want seq 6 active", rows[0].Seq, rows[0].Status)
	}
	if rows[len(rows)-1].Seq != 2 {
		t.Fatalf("oldest active = seq %d, want 2", rows[len(rows)-1].Seq)
	}
	// 淘汰位：第 1 个翻 superseded 但不物理删（存档可读）。
	archived, err := st.GetRevision(context.Background(), revs[0].ID)
	if err != nil {
		t.Fatalf("archived revision readable: %v", err)
	}
	if archived.Status != RevisionStatusSuperseded {
		t.Fatalf("archived status = %s, want superseded", archived.Status)
	}
	// 列表即选项：superseded 不可作回滚目标解析。
	if _, err := st.GetAppRevision(context.Background(), app.ID, revs[0].ID); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("GetAppRevision(superseded) = %v, want ErrRevisionNotFound", err)
	}
	if _, err := st.GetAppRevision(context.Background(), app.ID, revs[5].ID); err != nil {
		t.Fatalf("GetAppRevision(active) = %v, want nil", err)
	}
	// 连续多轮固化：窗口稳定在 5。
	createRevisionN(t, st, app.ID, 3)
	rows, err = st.ListRevisions(context.Background(), app.ID)
	if err != nil {
		t.Fatalf("list after more revisions: %v", err)
	}
	if len(rows) != RevisionKeepVersions {
		t.Fatalf("list = %d rows after churn, want %d", len(rows), RevisionKeepVersions)
	}
}

func TestGetAppRevisionRejectsForeignApp(t *testing.T) {
	st := openRevisionStore(t)
	a := createAppNamed(t, st, "a")
	b := createAppNamed(t, st, "b")
	revs := createRevisionN(t, st, a.ID, 1)
	if _, err := st.GetAppRevision(context.Background(), b.ID, revs[0].ID); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("cross-app resolution = %v, want ErrRevisionNotFound", err)
	}
}

func TestRevisionDesiredHashRoundTrip(t *testing.T) {
	st := openRevisionStore(t)
	app := createAppNamed(t, st, "demo")
	revs := createRevisionN(t, st, app.ID, 1)
	got, err := st.GetRevision(context.Background(), revs[0].ID)
	if err != nil {
		t.Fatalf("get revision: %v", err)
	}
	if got.DesiredHash != "hash-a" || got.ComposeNormalized != `{"rev":"a"}` || !got.Verified {
		t.Fatalf("revision round-trip mismatch: %+v", got)
	}
	if got.Status != RevisionStatusActive {
		t.Fatalf("status = %s, want active", got.Status)
	}
}

package main

// 回滚/版本/漂移 CLI 测试（T2-5b）：revisions list 保留窗展示、rollback
// 目标越界信封、drift show 未知应用。引擎语义测试在 internal/engine。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// seedAppWithRevisions 建库 + 应用 + 6 个版本（保留窗裁剪后 5 个可见），
// 返回 db 路径与全部 revision ID（含被淘汰的最旧者）。
func seedAppWithRevisions(t *testing.T, appName string) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	app, err := st.CreateApp(context.Background(), "", appName)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	var ids []string
	for i := 0; i < 6; i++ {
		letter := "abcdef"[i : i+1]
		var rev state.Revision
		if err := st.InTx(context.Background(), func(tx *state.Tx) error {
			r, err := tx.CreateRevision(context.Background(), state.RevisionWrite{
				AppID:             app.ID,
				ComposeNormalized: "{}",
				Overlay:           "{}",
				DesiredHash:       "seed-" + letter,
			})
			rev = r
			return err
		}); err != nil {
			t.Fatalf("create revision: %v", err)
		}
		ids = append(ids, rev.ID)
	}
	return db, ids
}

func TestCLIRevisionsListShowsRetentionWindow(t *testing.T) {
	db, ids := seedAppWithRevisions(t, "winapp")

	code, out, errOut := runCLI(t, "revisions", "list", "--db", db, "winapp")
	if code != 0 {
		t.Fatalf("revisions list exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	// 列表即选项：5 个 active、最新在前、最旧（第 1 个）被淘汰不出列表。
	if !strings.Contains(out, ids[5]) || !strings.Contains(out, ids[1]) {
		t.Fatalf("newest revisions missing: %q", out)
	}
	if strings.Contains(out, ids[0]) {
		t.Fatalf("superseded revision listed: %q", out)
	}
	if !strings.Contains(out, "keep=5") {
		t.Fatalf("retention window note missing: %q", out)
	}
}

func TestCLIRevisionsListJSON(t *testing.T) {
	db, _ := seedAppWithRevisions(t, "jsonapp")
	code, out, errOut := runCLI(t, "revisions", "list", "--db", db, "--json", "jsonapp")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{`"app": "jsonapp"`, `"keep_versions": 5`, `"revisions":`} {
		if !strings.Contains(out, want) {
			t.Fatalf("json missing %s: %s", want, out)
		}
	}
}

func TestCLIRollbackNoTargetEnvelope(t *testing.T) {
	db, _ := seedAppWithRevisions(t, "rbapp")

	// 未知应用 → E_ROLLBACK_NO_TARGET 信封（exit 1 + 四件套 stderr）。
	code, _, errOut := runCLI(t, "rollback", "--db", db, "--timeout", "1s", "ghost")
	if code != 1 {
		t.Fatalf("rollback ghost exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "E_ROLLBACK_NO_TARGET") {
		t.Fatalf("stderr missing E_ROLLBACK_NO_TARGET: %q", errOut)
	}

	// 越界 revision（被保留窗淘汰）→ 同码。
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	app, err := st.GetAppByName(context.Background(), "rbapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	_ = st.Close()
	code, _, errOut = runCLI(t, "rollback", "--db", db, "--timeout", "1s", "--to", app.ID, "rbapp")
	if code != 1 || !strings.Contains(errOut, "E_ROLLBACK_NO_TARGET") {
		t.Fatalf("out-of-window rollback exit=%d stderr=%q", code, errOut)
	}
}

func TestCLIDriftShowUnknownApp(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	// 未知应用在触底座之前即失败（无需 docker daemon）。
	code, _, errOut := runCLI(t, "drift", "show", "--db", db, "ghost")
	if code != 1 {
		t.Fatalf("drift show ghost exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "not found") && !strings.Contains(errOut, "不存在") {
		t.Fatalf("stderr missing not-found hint: %q", errOut)
	}
}

func TestCLIDriftEnableDisableAudit(t *testing.T) {
	db, _ := seedAppWithRevisions(t, "optapp")

	if code, _, errOut := runCLI(t, "drift", "enable", "--db", db, "optapp"); code != 0 {
		t.Fatalf("drift enable exit=%d stderr=%q", code, errOut)
	}
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st.Close() }()
	app, err := st.GetAppByName(context.Background(), "optapp")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if on, err := st.GetAppDriftConverge(context.Background(), app.ID); err != nil || !on {
		t.Fatalf("drift converge = %v (%v), want enabled", on, err)
	}
	if code, _, errOut := runCLI(t, "drift", "disable", "--db", db, "optapp"); code != 0 {
		t.Fatalf("drift disable exit=%d stderr=%q", code, errOut)
	}
	if on, err := st.GetAppDriftConverge(context.Background(), app.ID); err != nil || on {
		t.Fatalf("drift converge = %v (%v), want disabled", on, err)
	}
	// 审计：enable/disable 各一条（actor=human）。
	audits, err := st.RecentAudits(context.Background(), 20)
	if err != nil {
		t.Fatalf("audits: %v", err)
	}
	enabled, disabled := false, false
	for _, a := range audits {
		if a.Actor != "human" {
			continue
		}
		if a.Action == "reconcile.drift_converge_enabled" {
			enabled = true
		}
		if a.Action == "reconcile.drift_converge_disabled" {
			disabled = true
		}
	}
	if !enabled || !disabled {
		t.Fatalf("opt-in audits enabled=%v disabled=%v", enabled, disabled)
	}
}

package state

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestAuditFailClosed 证明 fail-closed（state-model §2.9）：审计与业务写
// 同事务；审计行写不出（数据库级 CHECK 违例——actor 为空）时，同事务内
// 已执行的业务写一并回滚，操作整体失败。审计写不可能被「静默跳过」。
func TestAuditFailClosed(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// ① 正常路径：业务写 + 合法审计同事务落库。
	proj := seedFixtureProject(t, st) // 事务外播种（store 写不可嵌套在 InTx 内）
	err := st.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateApp(ctx, "", "demo", proj.ID, proj.TeamID); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, AuditEntry{Actor: "human", Action: "app.create", Target: "app:demo", Result: "ok"})
	})
	if err != nil {
		t.Fatalf("healthy tx: %v", err)
	}
	if _, err := st.GetAppByName(ctx, "demo"); err != nil {
		t.Fatalf("app should exist after healthy tx: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	if len(audits) != 1 || audits[0].Action != "app.create" {
		t.Fatalf("audits = %+v, want exactly one app.create", audits)
	}

	// ② fail-closed 路径：审计行非法（actor 空串 → audit_log CHECK
	//    length(actor) > 0 违例，真实数据库错误）→ 业务写随事务回滚。
	err = st.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.CreateApp(ctx, "", "broken", proj.ID, proj.TeamID); err != nil {
			return err
		}
		// 审计写失败：缺 actor 的审计行不合法。
		return tx.WriteAudit(ctx, AuditEntry{Actor: "", Action: "app.create", Target: "app:broken", Result: "ok"})
	})
	if err == nil {
		t.Fatal("tx with invalid audit row must fail (fail-closed)")
	}
	if !strings.Contains(err.Error(), auditWritePrefix) {
		t.Fatalf("error should originate from audit write, got: %v", err)
	}
	if _, err := st.GetAppByName(ctx, "broken"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("business write must be rolled back with audit failure, got app err: %v", err)
	}
	audits, err = st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("read audits after rollback: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("failed tx must not leave audit rows, got %d", len(audits))
	}
}

// TestAuditErrorResultPath 记录失败审计的合法形态：result=error + 注册码。
func TestAuditErrorResultPath(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	err := st.InTx(ctx, func(tx *Tx) error {
		return tx.WriteAudit(ctx, AuditEntry{
			Actor: "system", Action: "app.delete", Target: "app:gone", Result: "error",
			ErrorCode: "E_STATE_VERSION_CONFLICT",
		})
	})
	if err != nil {
		t.Fatalf("error-result audit tx: %v", err)
	}
	audits, err := st.RecentAudits(ctx, 10)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	if len(audits) != 1 || audits[0].Result != "error" {
		t.Fatalf("audits = %+v, want one error-result row", audits)
	}
}

// TestDiffSummaryHostileInputIsAlwaysValidJSON MG-6 回归：审计 diff 摘要的
// 唯一构造器是 DiffSummary（json.Marshal 转义）——值含引号/反斜杠/控制
// 字符/换行时仍必须是可解析 JSON 且值原样往返。手拼形态（backtick 插值
// 零转义、%q 是 Go 转义非 JSON 转义）在此输入上破包——pr.yml 的
// antipattern-grep 门禁 A 在源面拦新发，本测试在行为面钉机制。
func TestDiffSummaryHostileInputIsAlwaysValidJSON(t *testing.T) {
	hostile := "quote\" backslash\\ ctrl\u0001 newline\n end"
	for _, tc := range []struct {
		name string
		got  string
		want map[string]any
	}{
		{"DiffSummary string value", DiffSummary("k", hostile), map[string]any{"k": hostile}},
		{"backupAuditSummary raw error text (former %q hand-concatenation spot)", backupAuditSummary(BackupWrite{
			Kind: "daily", Verify: BackupVerifyFailed, Error: hostile,
		}), map[string]any{"kind": "daily", "verify": "failed", "error": hostile}},
	} {
		if !json.Valid([]byte(tc.got)) {
			t.Fatalf("%s: summary is not valid JSON: %q", tc.name, tc.got)
		}
		var back map[string]any
		if err := json.Unmarshal([]byte(tc.got), &back); err != nil {
			t.Fatalf("%s: unmarshal failed: %v (%q)", tc.name, err, tc.got)
		}
		if len(back) != len(tc.want) {
			t.Fatalf("%s: key set mismatch: %v vs %v", tc.name, back, tc.want)
		}
		for k, v := range tc.want {
			if back[k] != v {
				t.Fatalf("%s: key %s value did not round-trip verbatim: %q vs %q", tc.name, k, back[k], v)
			}
		}
	}
	// 布尔/数值保持原生 JSON 字面量（非字符串化）。
	if got := DiffSummary("enabled", true, "services", 3); got != `{"enabled":true,"services":3}` {
		t.Fatalf("native type shape mismatch: %s", got)
	}
}

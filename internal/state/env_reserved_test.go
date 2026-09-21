package state

import (
	"context"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// E_ENV_KEY_RESERVED 守卫验收（managed-databases 设计 §2.5，E4/FZ-1，
// 验收 5）：用户（platform source）写 FLEETLY_* 保留名字空间拒绝；system
// 物化写放行；既有 system 行不可被 platform upsert 劫持 source。
func TestEnvReservedPrefixGuard(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	appID := createTestApp(t, st, "guard-app")

	// 1. 用户写保留前缀拒绝（422 E_ENV_KEY_RESERVED）。
	_, err := st.SetAppEnv(ctx, appID, "FLEETLY_DB_X", "cipher-secret-value", "platform")
	if err == nil {
		t.Fatal("platform write of FLEETLY_DB_X accepted (reserved namespace guard missing)")
	}
	appErr, ok := err.(*apperr.Error)
	if !ok {
		t.Fatalf("err = %T (%v), want *apperr.Error", err, err)
	}
	if appErr.Code() != "E_ENV_KEY_RESERVED" {
		t.Fatalf("code = %s, want E_ENV_KEY_RESERVED", appErr.Code())
	}
	if appErr.HTTPStatus() != 422 {
		t.Fatalf("http = %d, want 422", appErr.HTTPStatus())
	}
	// 值不进错误信息（secret 纪律）。
	if strings.Contains(err.Error(), "cipher-secret-value") {
		t.Fatalf("error message leaks value: %s", err)
	}
	// 行确实未写入。
	if _, err := st.GetAppEnv(ctx, appID, "FLEETLY_DB_X"); err == nil {
		t.Fatal("reserved key row exists after rejection")
	}

	// 2. 用户写普通键照常。
	if _, err := st.SetAppEnv(ctx, appID, "DATABASE_URL", "cipher-ok", "platform"); err != nil {
		t.Fatalf("platform write of normal key: %v", err)
	}

	// 3. system 物化写保留前缀放行（S4 连接串物化通道）。
	if _, err := st.SetAppEnv(ctx, appID, "FLEETLY_DB_PG_PROD_URL", "cipher-conn", "system"); err != nil {
		t.Fatalf("system write of FLEETLY_* key: %v", err)
	}
	row, err := st.GetAppEnv(ctx, appID, "FLEETLY_DB_PG_PROD_URL")
	if err != nil || row.Source != "system" {
		t.Fatalf("system row = %+v err=%v, want source=system", row, err)
	}

	// 4. 劫持守卫：既有 system 行不可被 platform upsert 顶掉 source。
	if _, err := st.SetAppEnv(ctx, appID, "FLEETLY_DB_PG_PROD_URL", "cipher-hijack", "platform"); err == nil {
		t.Fatal("platform upsert onto FLEETLY_* system row accepted (hijack guard missing)")
	} else if appErr, ok := err.(*apperr.Error); !ok || appErr.Code() != "E_ENV_KEY_RESERVED" {
		t.Fatalf("hijack err = %v, want E_ENV_KEY_RESERVED", err)
	}
	row, err = st.GetAppEnv(ctx, appID, "FLEETLY_DB_PG_PROD_URL")
	if err != nil || row.Value != "cipher-conn" || row.Source != "system" {
		t.Fatalf("row after hijack attempt = %+v err=%v, want untouched system row", row, err)
	}

	// 5. 大小写敏感：小写前缀不是保留名字空间（词表纪律）。
	if _, err := st.SetAppEnv(ctx, appID, "fleetly_db_x", "cipher-lower", "platform"); err != nil {
		t.Fatalf("platform write of lowercase fleetly_db_x: %v", err)
	}

	// 6. system 写任意键放行（平台内部物化不限 FLEETLY_ 前缀）。
	if _, err := st.SetAppEnv(ctx, appID, "PLAIN_SYSTEM_KEY", "cipher-sys", "system"); err != nil {
		t.Fatalf("system write of non-reserved key: %v", err)
	}
}

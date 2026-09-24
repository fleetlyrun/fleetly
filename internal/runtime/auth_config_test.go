package runtime

// auth 配置节测试（v0.3 W2-S4：config auth.session_ttl_hours 的缺省回落与
// 绝对上限封顶——rbac-teams §2.2）。

import (
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

func TestSessionTTLConfig(t *testing.T) {
	// 零值配置回落缺省 7 天滑动。
	var zero AppConfig
	if got := zero.SessionTTL(); got != state.DefaultSessionTTL {
		t.Fatalf("zero config SessionTTL = %v, want %v", got, state.DefaultSessionTTL)
	}
	// 显式小时数生效。
	cfg := AppConfig{Auth: AuthConfig{SessionTTLHours: 48}}
	if got := cfg.SessionTTL(); got != 48*time.Hour {
		t.Fatalf("48h config SessionTTL = %v, want 48h", got)
	}
	// 负值回落缺省（不允许误配成零/负寿命）。
	neg := AppConfig{Auth: AuthConfig{SessionTTLHours: -5}}
	if got := neg.SessionTTL(); got != state.DefaultSessionTTL {
		t.Fatalf("negative config SessionTTL = %v, want %v", got, state.DefaultSessionTTL)
	}
	// 超过 30 天绝对上限封顶（可调短、不可调过长寿命）。
	huge := AppConfig{Auth: AuthConfig{SessionTTLHours: 24 * 400}}
	if got := huge.SessionTTL(); got != state.MaxSessionLifetime {
		t.Fatalf("huge config SessionTTL = %v, want capped at %v", got, state.MaxSessionLifetime)
	}
}

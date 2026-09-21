package errcode

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// docCodes 是验收标准的内联文档清单（手抄自四份设计文档，逐条注明出处；
// 验收：与注册表数量与拼写完全一致）。抄录基准：docs/design/ 下
// 2026-09-17 四份文档当前版本。
var docCodes = map[string]string{ // code → 文档出处
	// release-semantics.md §2.7（17 E）
	"E_COMPOSE_UNSUPPORTED":         "release-semantics §2.7",
	"E_COMPOSE_MANAGED_FIELD":       "release-semantics §2.7",
	"E_COMPOSE_UNSAFE_STRATEGY":     "release-semantics §2.7",
	"E_BUILD_FAILED":                "release-semantics §2.7",
	"E_IMAGE_PULL_FAILED":           "release-semantics §2.7",
	"E_IMAGE_UNAVAILABLE":           "release-semantics §2.7",
	"E_SCHEDULER_PENDING_TIMEOUT":   "release-semantics §2.7",
	"E_TASK_START_FAILED":           "release-semantics §2.7",
	"E_HEALTH_TIMEOUT":              "release-semantics §2.7",
	"E_OBSERVE_CRASH_LOOP":          "release-semantics §2.7",
	"E_OBSERVE_UNHEALTHY":           "release-semantics §2.7",
	"E_DEPLOY_INTERRUPTED":          "release-semantics §2.7",
	"E_DEPLOY_POST_WINDOW_UNSTABLE": "release-semantics §2.7",
	"E_DEPLOY_DOWNTIME_FAILED":      "release-semantics §2.7",
	"E_ROLLBACK_FAILED":             "release-semantics §2.7",
	"E_ROLLBACK_NO_TARGET":          "release-semantics §2.7",
	"E_RUNTIME_UNAVAILABLE":         "release-semantics §2.7",

	// stateful-placement.md §2.8（8 E）
	"E_PLACEMENT_NODE_INVALID":      "stateful-placement §2.8",
	"E_PLACEMENT_NODE_NOT_FOUND":    "stateful-placement §2.8",
	"E_PLACEMENT_NODE_UNAVAILABLE":  "stateful-placement §2.8",
	"E_PLACEMENT_NODE_GONE":         "stateful-placement §2.8",
	"E_PLACEMENT_NO_ELIGIBLE_NODE":  "stateful-placement §2.8",
	"E_PLACEMENT_MOVE_REQUIRES_ACK": "stateful-placement §2.8",
	"E_PLACEMENT_LABEL_CONFLICT":    "stateful-placement §2.8",
	"E_VOLUME_NODE_MISMATCH":        "stateful-placement §2.8",

	// stateful-placement.md §2.9（1 E）
	"E_CAPABILITY_REQUIRES_MULTI_NODE": "stateful-placement §2.9",

	// state-model.md §2.7 / §2.9 / §2.4 / §2.2（4 E）
	"E_BACKUP_KEY_MISSING":     "state-model §2.7",
	"E_EVENT_CURSOR_EXPIRED":   "state-model §2.9",
	"E_LABEL_RESERVED":         "state-model §2.4",
	"E_STATE_VERSION_CONFLICT": "state-model §2.2 (same as architecture §2.3)",

	// architecture.md §2.4（2 E，E_COMPOSE_* 与 release-semantics 重复不另计）
	"E_DOMAIN_CONFLICT":    "architecture §2.4",
	"E_DOMAIN_UNSUPPORTED": "architecture §2.4",

	// T2.15 实现期新增（文档外码单独列出，待 T0.5 契约冻结确认）：架构 §2.5
	// 不变量「路由发布失败不回滚部署、单独告警」的审计错误码落点。
	"E_ROUTE_PUBLISH_FAILED": "T2.15 added during implementation (architecture §2.5 route-failure alert semantics; pending T0.5 freeze confirmation)",

	// MG-C3 实现期新增（文档外码单独列出，待 T0.5 契约冻结确认）：架构 §2.4
	// plan/apply 语义「破坏性操作要求 --confirm-destructive」的 deploy 入队
	// 门控错误码。
	"E_DEPLOY_CONFIRM_REQUIRED": "MG-C3 added during implementation (architecture §2.4 destructive-change confirmation gate; pending T0.5 freeze confirmation)",

	// M4-6 实现期新增（评审整改 B5，文档外码单独列出，待 T0.5 契约冻结
	// 确认）：RevokeToken 最后管理员守卫——吊销最后一枚未吊销 admin token
	// 会使平台锁死（重启不补种 bootstrap），409 拒绝。
	"E_TOKEN_LAST_ADMIN": "M4-6 added during implementation (review remediation B5: token last-admin guard; pending T0.5 freeze confirmation)",

	// multi-node.md §5.2（2 E，D-MN-11）：registry 模式部署前哨与推送的
	// 分层归因码（manifest 缺失复用 E_IMAGE_UNAVAILABLE，不另立新码）。
	"E_REGISTRY_UNAVAILABLE": "multi-node §5.2 (D-MN-11: registry-mode deploy preflight, registry unreachable)",
	"E_REGISTRY_PUSH_FAILED": "multi-node §5.2 (D-MN-11: push to the platform registry failed)",

	// multi-node.md §5.2（1 E，D-MN-13）：join 门禁——base_domain 缺失即
	// 多节点未启用，join 面显式拒绝（409）。
	"E_MULTI_NODE_REQUIRES_BASE_DOMAIN": "multi-node §5.2 (D-MN-13: join gate, base_domain missing)",

	// E3 对象存储 §5.2 实现期新增（2026-09-21 裁决轮落定，文档外码单独
	// 列出，待 T0.5 契约冻结确认）：E_S3_NOT_CONFIGURED = label 注入前哨
	//（E3-4/W3-S3 接线消费——s3.mode=unset 时 plan 阶段拒绝）；E_S3_TEST_
	// FAILED = 探针失败（detail/context 带失败步）；E_S3_PUBLIC_REQUIRES_
	// BASE_DOMAIN = 公网子域开关在无平台域名形态下拒绝。
	"E_S3_NOT_CONFIGURED":              "E3 object-storage §5.2 (label-injection sentinel; wired with E3-4)",
	"E_S3_CONFIG_CONFLICT":             "E3 object-storage §5.2 (mode vs explicit fields mutual exclusion)",
	"E_S3_TEST_FAILED":                 "E3 object-storage §5.2 (connection probe failed; failed step in the envelope context)",
	"E_S3_PUBLIC_REQUIRES_BASE_DOMAIN": "E3 object-storage §5.2 (public subdomain toggle without a base domain)",

	// 警告码（5 W）
	"W_DEPLOY_INSTABILITY":      "release-semantics §2.7",
	"W_DEPLOY_NO_HEALTHCHECK":   "release-semantics §2.7/§2.8",
	"W_ROLLBACK_IMAGE_RISK":     "release-semantics §2.7",
	"W_PLACEMENT_STATELESS_PIN": "stateful-placement §2.8",
	"W_ENV_PLATFORM_OVERRIDE":   "architecture §2.4",
}

// TestDocCodeSetMatchesRegistry 是验收标准 2：注册表码集与四份文档清单
// 逐一致（数量与拼写完全一致）。
func TestDocCodeSetMatchesRegistry(t *testing.T) {
	regIDs := Default().IDs()
	if len(regIDs) != len(docCodes) {
		t.Fatalf("registry has %d codes, doc list has %d", len(regIDs), len(docCodes))
	}
	for _, id := range regIDs {
		if _, ok := docCodes[id]; !ok {
			t.Errorf("registry code %q not in the doc list (out-of-doc codes must be listed separately and marked pending T0.5 freeze confirmation)", id)
		}
	}
	for id, source := range docCodes {
		if _, ok := Default().Get(id); !ok {
			t.Errorf("doc code %q (%s) missing from the registry: omission", id, source)
		}
	}
}

// TestRegisteredCountByKind 双保险：42 E + 5 W = 47（T2.15 增
// E_ROUTE_PUBLISH_FAILED、MG-C3 增 E_DEPLOY_CONFIRM_REQUIRED、M4-6 增
// E_TOKEN_LAST_ADMIN、E1-5 增 E_REGISTRY_UNAVAILABLE/E_REGISTRY_PUSH_FAILED、
// E1-8 增 E_MULTI_NODE_REQUIRES_BASE_DOMAIN、E3-2 增 E_S3_* 四码——错误码
// 只增纪律）。
func TestRegisteredCountByKind(t *testing.T) {
	errCount, warnCount := 0, 0
	for _, c := range Default().All() {
		if strings.HasPrefix(c.ID, "E_") {
			errCount++
		} else {
			warnCount++
		}
	}
	if errCount != 42 || warnCount != 5 {
		t.Fatalf("E_ = %d (want 42), W_ = %d (want 5)", errCount, warnCount)
	}
}

// TestDuplicateRegistrationRejected 验收标准 3：重复注册 fail-fast。
func TestDuplicateRegistrationRejected(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(Code{ID: "E_TEST_DUPLICATE", HTTP: 400, Summary: "s", Suggestion: "x"})
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic (fail-fast)")
		}
	}()
	r.MustRegister(Code{ID: "E_TEST_DUPLICATE", HTTP: 409, Summary: "s2", Suggestion: "y"})
}

// TestInvalidFormatRejected 验收标准 3：非法格式 fail-fast（不以 E_/W_ 开头、
// 含小写、空串、空段）。
func TestInvalidFormatRejected(t *testing.T) {
	cases := []string{
		"E_lower",
		"e_UPPER",
		"X_NOT_REGISTRY",
		"NOTPREFIXED",
		"",
		"E_",
		"E__DOUBLE",
		"E_TRAILING_",
		"W_lower_case",
	}
	for _, id := range cases {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("invalid code %q must panic at registration", id)
				}
			}()
			NewRegistry().MustRegister(Code{ID: id, HTTP: 400, Summary: "s", Suggestion: "x"})
		}()
	}
}

// TestHTTPMappingInvariants：E_ 码须 4xx/5xx；W_ 码不得携带 HTTP 状态。
func TestHTTPMappingInvariants(t *testing.T) {
	for _, c := range Default().All() {
		if strings.HasPrefix(c.ID, "W_") {
			if c.HTTP != 0 {
				t.Errorf("warning %s carries HTTP %d, want 0", c.ID, c.HTTP)
			}
			continue
		}
		if c.HTTP < 400 || c.HTTP > 599 {
			t.Errorf("error %s HTTP = %d, want 4xx/5xx", c.ID, c.HTTP)
		}
		if c.Docs() != DocsURLPrefix+c.ID {
			t.Errorf("%s docs anchor = %q, want prefix+ID", c.ID, c.Docs())
		}
	}
}

// TestDocumentedHTTPMappings 文档显式给定的 HTTP 映射照文档。
func TestDocumentedHTTPMappings(t *testing.T) {
	want := map[string]int{
		"E_DOMAIN_CONFLICT":                 409, // architecture §2.4
		"E_STATE_VERSION_CONFLICT":          409, // state-model §2.2
		"E_VOLUME_NODE_MISMATCH":            409, // stateful-placement §2.8（前哨 409）
		"E_PLACEMENT_MOVE_REQUIRES_ACK":     409, // stateful-placement §2.2
		"E_EVENT_CURSOR_EXPIRED":            410, // state-model §2.9
		"E_LABEL_RESERVED":                  422, // state-model §2.4
		"E_PLACEMENT_LABEL_CONFLICT":        422, // stateful-placement §2.2
		"E_PLACEMENT_NODE_INVALID":          422, // stateful-placement §2.2（解析失败 422+候选）
		"E_PLACEMENT_NODE_NOT_FOUND":        422, // stateful-placement §2.5
		"E_REGISTRY_UNAVAILABLE":            503, // multi-node §5.2（D-MN-11 前哨快速失败）
		"E_REGISTRY_PUSH_FAILED":            500, // multi-node §5.2（D-MN-11 推送失败）
		"E_MULTI_NODE_REQUIRES_BASE_DOMAIN": 409, // multi-node §5.2（D-MN-13 join 门禁）
	}
	for id, httpStatus := range want {
		c, ok := Default().Get(id)
		if !ok {
			t.Fatalf("%s not registered", id)
		}
		if c.HTTP != httpStatus {
			t.Errorf("%s HTTP = %d, want %d (documented explicitly)", id, c.HTTP, httpStatus)
		}
	}
}

// TestGoldenSnapshot 码集 golden 快照：新增/改写码必须显式更新 golden
// （防静默变更；-update 重生成）。
func TestGoldenSnapshot(t *testing.T) {
	golden := filepath.Join("testdata", "codes.golden")
	got := Default().Snapshot()
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden) //nolint:gosec // golden 为 testdata 固定路径
	if err != nil {
		t.Fatalf("read golden (run go test -update to regenerate): %v", err)
	}
	if string(want) != got {
		t.Fatalf("code set drifted from golden:\n--- golden ---\n%s\n--- registry ---\n%s", want, got)
	}
}

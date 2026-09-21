package compose

// E5 Cron compose 面测试：fleetly.cron label 家族的值契约（表达式五段标准
// 式/时区/超时）、replicas=0/省略契约（E_COMPOSE_UNSUPPORTED reason 细分）、
// 归一化快照携带 CronSchedule。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

func loadCron(t *testing.T, labels string, deploy string) (*Spec, error) {
	t.Helper()
	content := "name: cronapp\nservices:\n  task:\n    image: busybox\n" +
		deploy + labels
	spec, _, err := Load(t.Context(), writeCompose(t, content))
	return spec, err
}

// assertCronReason 断言 err 是 E_LABEL_RESERVED 信封且 reason 上下文匹配。
func assertCronReason(t *testing.T, err error, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var ae *apperr.Error
	if !asAppErrCron(err, &ae) {
		t.Fatalf("error is not an apperr envelope: %v", err)
	}
	if got := ae.Context()["reason"]; got != reason {
		t.Fatalf("reason = %q, want %q", got, reason)
	}
}

func asAppErrCron(err error, target **apperr.Error) bool {
	if e, ok := err.(*apperr.Error); ok {
		*target = e
		return true
	}
	return false
}

// TestCronLabelValid 解析合法声明：表达式 trim、时区归一（IANA 名）、超时
// 归一（time.Duration.String() 形态）。
func TestCronLabelValid(t *testing.T) {
	spec, err := loadCron(t, "    labels:\n      fleetly.cron: \"  */5 * * * *  \"\n      fleetly.cron.timezone: Asia/Shanghai\n      fleetly.cron.timeout: 90m\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := spec.Services[0]
	if svc.Cron == nil {
		t.Fatal("cron schedule not attached to the normalized service")
	}
	if svc.Cron.Expression != "*/5 * * * *" {
		t.Fatalf("expression = %q, want trimmed */5 * * * *", svc.Cron.Expression)
	}
	if svc.Cron.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, want Asia/Shanghai", svc.Cron.Timezone)
	}
	if svc.Cron.Timeout != "1h30m0s" {
		t.Fatalf("timeout = %q, want normalized 1h30m0s", svc.Cron.Timeout)
	}
}

// TestCronLabelDefaultsUTCAndBudget 时区/超时缺省：Timezone 空 = UTC，
// Timeout 空 = 平台看门狗预算（缺省不烘焙进快照，治理参数取当前平台配置）。
func TestCronLabelDefaultsUTCAndBudget(t *testing.T) {
	spec, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cs := spec.Services[0].Cron
	if cs == nil || cs.Timezone != "" || cs.Timeout != "" {
		t.Fatalf("defaults wrong: %+v", cs)
	}
}

// TestCronLabelInvalidExpression 非法/六段含秒表达式在 Load 期即拒（契约
// 天然成立——D-CR-1），错误信息携带 label 名。
func TestCronLabelInvalidExpression(t *testing.T) {
	for _, expr := range []string{"* * * * * *", "61 * * * *", "not a cron"} {
		_, err := loadCron(t, "    labels:\n      fleetly.cron: \""+expr+"\"\n", "")
		if err == nil {
			t.Fatalf("expression %q accepted", expr)
		}
		if !strings.Contains(err.Error(), LabelCron) {
			t.Fatalf("error %v does not name the label", err)
		}
		assertCronReason(t, err, "invalid_expression")
	}
}

// TestCronLabelInvalidTimezone 未知时区拒绝。
func TestCronLabelInvalidTimezone(t *testing.T) {
	_, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n      fleetly.cron.timezone: Mars/Olympus\n", "")
	if err == nil {
		t.Fatal("unknown timezone accepted")
	}
	assertCronReason(t, err, "invalid_timezone")
}

// TestCronLabelInvalidTimeout 非法/非正超时拒绝。
func TestCronLabelInvalidTimeout(t *testing.T) {
	for _, tv := range []string{"soon", "0s", "-5m"} {
		_, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n      fleetly.cron.timeout: "+tv+"\n", "")
		if err == nil {
			t.Fatalf("timeout %q accepted", tv)
		}
		assertCronReason(t, err, "invalid_timeout")
	}
}

// TestCronLabelsWithoutSchedule 孤儿时区/超时 label（无 fleetly.cron）拒绝
// ——静默无机会生效的声明是「以为配了」的悬案。
func TestCronLabelsWithoutSchedule(t *testing.T) {
	_, err := loadCron(t, "    labels:\n      fleetly.cron.timezone: UTC\n", "")
	if err == nil {
		t.Fatal("orphan timezone label accepted")
	}
	assertCronReason(t, err, "cron_without_schedule")
}

// TestCronReplicasContract replicas 契约（架构 §4.3）：>0 →
// E_COMPOSE_UNSUPPORTED（reason=cron_replicas）；0/省略合法。
func TestCronReplicasContract(t *testing.T) {
	_, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n", "    deploy:\n      replicas: 2\n")
	if err == nil {
		t.Fatal("replicas=2 with fleetly.cron accepted")
	}
	var ae *apperr.Error
	if !asAppErrCron(err, &ae) || ae.Code() != "E_COMPOSE_UNSUPPORTED" {
		t.Fatalf("error is not E_COMPOSE_UNSUPPORTED: %v", err)
	}
	if got := ae.Context()["reason"]; got != "cron_replicas" {
		t.Fatalf("reason = %q, want cron_replicas", got)
	}
	if _, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n", "    deploy:\n      replicas: 0\n"); err != nil {
		t.Fatalf("replicas=0 rejected: %v", err)
	}
}

// TestCronInSpecHashAndDiff cron 声明进 spec_hash（label 变更即期望态变更）
// 且 plan/diff 如实呈现（服务维度 Added/Updated 正常覆盖 cron 服务——
// 「只声明不部署」不把服务从 diff 面隐身）。
func TestCronInSpecHashAndDiff(t *testing.T) {
	with, err := loadCron(t, "    labels:\n      fleetly.cron: \"* * * * *\"\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	without, err := loadCron(t, "", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if with.SpecHash == without.SpecHash {
		t.Fatal("cron label did not change the spec hash")
	}
	plan, err := Diff(without, with)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	// 服务在两侧都存在（label 差异）→ Updated 报 cron.expression 字段变更
	// ——「只声明不部署」不把服务从 plan/diff 面隐身。
	if len(plan.Services.Updated) != 1 || plan.Services.Updated[0].Name != "task" {
		t.Fatalf("plan does not present the cron service honestly: %+v", plan.Services)
	}
	if len(plan.Services.Updated[0].Fields) == 0 || plan.Services.Updated[0].Fields[0].Path != "cron.expression" {
		t.Fatalf("field-level diff does not carry the cron change: %+v", plan.Services.Updated[0].Fields)
	}
}

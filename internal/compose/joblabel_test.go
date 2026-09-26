package compose

// DT-4 compose 面测试：fleetly.job label 家族的值契约（值词表仅 init）、
// 与 fleetly.cron 互斥、孤儿 fleetly.job.timeout 拒绝、expose 禁令与
// replicas 契约（typed 层补刀）、归一化快照携带 InitJob/InitJobTimeout 并
// 进 spec_hash。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// loadJob 载入一份带 labels/deploy 附加行的单服务 compose。
func loadJob(t *testing.T, labels string, deploy string) (*Spec, error) {
	t.Helper()
	content := "name: jobapp\nservices:\n  migrate:\n    image: busybox\n" +
		deploy + labels
	spec, _, err := Load(t.Context(), writeCompose(t, content))
	return spec, err
}

// assertJobReason 断言 err 是 E_LABEL_RESERVED 信封且 reason 上下文匹配。
func assertJobReason(t *testing.T, err error, reason string) {
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

// TestJobLabelValid 解析合法声明：fleetly.job: init（前后空白 trim）+
// fleetly.job.timeout 归一（time.Duration.String() 形态）。
func TestJobLabelValid(t *testing.T) {
	spec, err := loadJob(t, "    labels:\n      fleetly.job: \"  init \"\n      fleetly.job.timeout: 45m\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	svc := spec.Services[0]
	if !svc.InitJob {
		t.Fatal("init job declaration not attached to the normalized service")
	}
	if svc.InitJobTimeout != "45m0s" {
		t.Fatalf("timeout = %q, want normalized 45m0s", svc.InitJobTimeout)
	}
}

// TestJobLabelDefaults 无超时声明：InitJobTimeout 空 = 平台看门狗预算
//（缺省不烘焙进快照，治理参数取当前平台配置）。
func TestJobLabelDefaults(t *testing.T) {
	spec, err := loadJob(t, "    labels:\n      fleetly.job: init\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if svc := spec.Services[0]; !svc.InitJob || svc.InitJobTimeout != "" {
		t.Fatalf("defaults wrong: %+v", svc)
	}
}

// TestJobLabelInvalidValue 值词表仅 init：其他值一律 fail-loud
//（E_LABEL_RESERVED + reason=invalid_value——静默当未声明是「以为配了」
// 的悬案）。
func TestJobLabelInvalidValue(t *testing.T) {
	for _, v := range []string{"migrate", "pre", "INIT", "true", ""} {
		_, err := loadJob(t, "    labels:\n      fleetly.job: \""+v+"\"\n", "")
		if err == nil {
			t.Fatalf("value %q accepted", v)
		}
		if !strings.Contains(err.Error(), LabelJob) {
			t.Fatalf("error %v does not name the label", err)
		}
		assertJobReason(t, err, "invalid_value")
	}
}

// TestJobLabelExclusiveWithCron 与 fleetly.cron 互斥（两种一次性执行语义
// 不可并存——同一服务不能既按点触发又在发布期跑）。
func TestJobLabelExclusiveWithCron(t *testing.T) {
	_, err := loadJob(t, "    labels:\n      fleetly.job: init\n      fleetly.cron: \"* * * * *\"\n", "")
	if err == nil {
		t.Fatal("fleetly.job + fleetly.cron accepted")
	}
	assertJobReason(t, err, "job_cron_exclusive")
}

// TestJobTimeoutWithoutDeclaration 孤儿 fleetly.job.timeout（无 fleetly.job）
// 拒绝——静默无机会生效的声明是「以为配了」的悬案（cron 孤儿纪律同款）。
func TestJobTimeoutWithoutDeclaration(t *testing.T) {
	_, err := loadJob(t, "    labels:\n      fleetly.job.timeout: 30m\n", "")
	if err == nil {
		t.Fatal("orphan job timeout accepted")
	}
	assertJobReason(t, err, "job_without_declaration")
}

// TestJobLabelInvalidTimeout 非法/非正超时拒绝。
func TestJobLabelInvalidTimeout(t *testing.T) {
	for _, tv := range []string{"soon", "0s", "-5m"} {
		_, err := loadJob(t, "    labels:\n      fleetly.job: init\n      fleetly.job.timeout: "+tv+"\n", "")
		if err == nil {
			t.Fatalf("timeout %q accepted", tv)
		}
		assertJobReason(t, err, "invalid_timeout")
	}
}

// TestJobServiceExposeRejected init job 服务禁 expose：一次性进程没有长驻
// 监听面，expose（路由目标端口声明）是平台无法兑现的期望 →
// E_COMPOSE_UNSUPPORTED reason=init_job_expose。
func TestJobServiceExposeRejected(t *testing.T) {
	_, err := loadJob(t, "    labels:\n      fleetly.job: init\n    expose:\n      - \"8080\"\n", "")
	if err == nil {
		t.Fatal("expose + fleetly.job: init accepted")
	}
	var ae *apperr.Error
	if !asAppErrCron(err, &ae) || ae.Code() != "E_COMPOSE_UNSUPPORTED" {
		t.Fatalf("error is not E_COMPOSE_UNSUPPORTED: %v", err)
	}
	if got := ae.Context()["reason"]; got != "init_job_expose" {
		t.Fatalf("reason = %q, want init_job_expose", got)
	}
}

// TestJobReplicasContract replicas 契约：>0 → E_COMPOSE_UNSUPPORTED
//（reason=init_job_replicas）；0/省略合法（cron 同型）。
func TestJobReplicasContract(t *testing.T) {
	_, err := loadJob(t, "    labels:\n      fleetly.job: init\n", "    deploy:\n      replicas: 2\n")
	if err == nil {
		t.Fatal("replicas=2 with fleetly.job: init accepted")
	}
	var ae *apperr.Error
	if !asAppErrCron(err, &ae) || ae.Code() != "E_COMPOSE_UNSUPPORTED" {
		t.Fatalf("error is not E_COMPOSE_UNSUPPORTED: %v", err)
	}
	if got := ae.Context()["reason"]; got != "init_job_replicas" {
		t.Fatalf("reason = %q, want init_job_replicas", got)
	}
	if _, err := loadJob(t, "    labels:\n      fleetly.job: init\n", "    deploy:\n      replicas: 0\n"); err != nil {
		t.Fatalf("replicas=0 rejected: %v", err)
	}
}

// TestJobInSpecHashAndDiff init 声明进 spec_hash（label 变更即期望态变更）
// 且 plan/diff 如实呈现字段级变更。
func TestJobInSpecHashAndDiff(t *testing.T) {
	with, err := loadJob(t, "    labels:\n      fleetly.job: init\n", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	without, err := loadJob(t, "", "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if with.SpecHash == without.SpecHash {
		t.Fatal("init job label did not change the spec hash")
	}
	plan, err := Diff(without, with)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(plan.Services.Updated) != 1 || plan.Services.Updated[0].Name != "migrate" {
		t.Fatalf("plan does not present the init job service honestly: %+v", plan.Services)
	}
	found := false
	for _, f := range plan.Services.Updated[0].Fields {
		if f.Path == "init_job" {
			found = true
		}
	}
	if !found {
		t.Fatalf("field-level diff does not carry the init_job change: %+v", plan.Services.Updated[0].Fields)
	}
}

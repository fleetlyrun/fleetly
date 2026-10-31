package fleetlygrpc

// Diagnosis 上下文服务（B2a W2，zeabur 借鉴设计 §2.5；runtime/v1 观测/
// 排障面，不新开顶层上下文）：读时解释层——"为什么现在坏"。facts 装配
// 在 engine.DiagnoseFacts（经 Engine 导出面/repo 端口，零旁路），本层只
// 做：行级授权（沿 project 链——DiagnoseApp 走 app 锚、ListProjectFindings
// 走 project 锚）→ diagnosis.Evaluate（纯函数规则集，含 unexplained 兜底）
// → proto 映射（注册表 Summary 直通 + Params 证据投影 + Fix 深链占位符
// 填充）。纯读：无铸票、无幂等、不进冻结面（READ 桶，ADR-0081 附录）。

import (
	"context"
	"strconv"
	"strings"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
)

// DiagnosisService 是诊断读面（DiagnoseApp 单 App 深诊断 +
// ListProjectFindings 跨 App 聚合）。
type DiagnosisService struct {
	runtimev1.UnimplementedDiagnosisServiceServer
	s *Services
}

// DiagnoseApp 单 App 深诊断：装配 facts → 全量规则（含 unexplained 兜底）
// → 条目 + unreadable 面清单（读不了 ≠ 不存在——组件健康面 B2b 前恒
// 在列，诚实展示「诊断未覆盖」）。
func (svc *DiagnosisService) DiagnoseApp(ctx context.Context, req *runtimev1.DiagnoseAppRequest) (*runtimev1.DiagnoseAppResponse, error) {
	// app_id 必填已由 protovalidate 拦截器在受理面拦截（CEL 注解）。
	// 行级授权沿 project 链（App 行 → Project → Team；不存在的 App 404、
	// 他队 403）；App 行复用返回值（ProjectID 是 Fix 深链占位符的填充源）。
	appRow, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	facts := svc.s.Engine.DiagnoseFacts(ctx, appRow.ID)
	out := &runtimev1.DiagnoseAppResponse{UnreadableFaces: facts.UnreadableFaces()}
	for _, f := range diagnosis.Evaluate(facts) {
		out.Findings = append(out.Findings, findingMsg(f, appRow.ProjectID, appRow.ID))
	}
	return out, nil
}

// ListProjectFindings 跨 App 聚合（「需要你」页，v1 挂 project 域——
// 授权沿 project 链）。分页/扇出预算（设计 §2.4 成本口径）：页沿 ListApps
// 惯例推进（after_app_seq 游标 + limit，健康 App 推进游标但不占响应）；
// 廉价先行筛命中（非健康观测——纯内存判定，或 firing/pending 告警行——
// ListAll 单查）的 App 才深诊断（每 App 一次观测/账本往返），先行筛假
// 阳性（深诊断为空）不占响应。
func (svc *DiagnosisService) ListProjectFindings(ctx context.Context, req *runtimev1.ListProjectFindingsRequest) (*runtimev1.ListProjectFindingsResponse, error) {
	// project_id 必填已由 protovalidate 拦截器在受理面拦截（CEL 注解）。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	apps, err := svc.s.Apps.ListByProjectPage(ctx, req.GetProjectId(), req.GetAfterAppSeq(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	firing, err := svc.s.firingPendingApps(ctx)
	if err != nil {
		return nil, err
	}
	out := &runtimev1.ListProjectFindingsResponse{}
	for i := range apps {
		a := &apps[i]
		if !svc.s.Engine.DiagnoseAppCheapHit(a.ID) && !firing[a.ID] {
			continue // 先行筛未命中：不深诊断（扇出预算）
		}
		facts := svc.s.Engine.DiagnoseFacts(ctx, a.ID)
		findings := diagnosis.Evaluate(facts)
		if len(findings) == 0 {
			continue // 先行筛假阳性（如 firing-only 告警行无 workload 条目）
		}
		af := &runtimev1.AppFindings{
			AppId: a.ID, AppSeq: a.Seq, AppName: a.Name,
			UnreadableFaces: facts.UnreadableFaces(),
		}
		for _, f := range findings {
			af.Findings = append(af.Findings, findingMsg(f, a.ProjectID, a.ID))
		}
		out.Apps = append(out.Apps, af)
	}
	return out, nil
}

// firingPendingApps 返回先行筛的告警腿：firing 或 pending 态规则行的
// App 集（ListAll 单查建集——不逐 App 解析）。
func (s *Services) firingPendingApps(ctx context.Context) (map[string]bool, error) {
	rules, err := s.Engine.AlertRuleRepo().ListAll(ctx)
	if err != nil {
		return nil, mapStateError(err, "alert rule")
	}
	set := make(map[string]bool, len(rules))
	for i := range rules {
		if rules[i].State == alertrule.StateFiring || rules[i].PendingSince != "" {
			set[rules[i].AppID] = true
		}
	}
	return set, nil
}

// findingMsg 是 diagnosis.Finding → proto 传输形态。message = 注册表
// Summary 直通（语义冻结在注册表，实例层不转写）；evidence 从 Params 投影
// （state/exit_code/restart_count/recent_event 是规则层的动态证据键集）；
// fix.console_path 填充命名占位符（{project_id}/{app_id}——注册表模板
// 形态，装配面填充）。
func findingMsg(f diagnosis.Finding, projectID, appID string) *runtimev1.Finding {
	msg := &runtimev1.Finding{
		FindingId:  f.ID(),
		Severity:   string(f.Severity()),
		FaultClass: string(f.FaultClass),
		WorkloadId: f.WorkloadID,
		Process:    f.Process,
		Message:    f.Entry.Summary,
		Evidence: &runtimev1.FindingEvidence{
			State:       f.Params["state"],
			RecentEvent: f.Params["recent_event"],
		},
		Fix: &runtimev1.FindingFix{
			Verb:        f.Fix.Verb,
			Params:      f.Fix.Params,
			ConsolePath: fillConsolePath(f.Fix.ConsolePath, projectID, appID),
		},
	}
	if n := paramInt(f.Params["restart_count"]); n != nil {
		msg.Evidence.RestartCount = *n
	}
	msg.Evidence.ExitCode = paramInt(f.Params["exit_code"])
	return msg
}

// fillConsolePath 填充注册表深链模板的命名占位符（{project_id}/{app_id}）。
func fillConsolePath(tpl, projectID, appID string) string {
	if tpl == "" {
		return ""
	}
	path := strings.ReplaceAll(tpl, "{project_id}", projectID)
	return strings.ReplaceAll(path, "{app_id}", appID)
}

// paramInt 是 Params 纯字符串面的 int 投影（空/坏值 = nil，不猜）。
func paramInt(v string) *int32 {
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	i := int32(n) //nolint:gosec // 退出码/近窗计数域远小于 int32 上界
	return &i
}

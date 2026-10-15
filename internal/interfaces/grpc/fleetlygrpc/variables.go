package fleetlygrpc

// SharedVariables / AppVariables 上下文服务实现（F2.9 ADR-0043 Project 级
// 共享变量 + ADR-0072 三级合成顶层的 App 级变量）。从 structure.go 拆出
//（sizeguard 1200 行预算——networks.go 同款拆分序，P2 批二 A 化后行数
// 超预算触发）。

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/appvariable"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/sharedvariable"
)

// ---- SharedVariables（Project 级共享变量，ADR-0043） ----

type SharedVariablesService struct {
	structurev1.UnimplementedSharedVariablesServiceServer
	s *Services
}

// PutSharedVariable 落/覆盖一条共享变量，响应携带受影响 App 提示（近似
// 口径，ADR-0043 决策 4——改共享变量不触发任何自动重部署，提示"哪些 App
// 重部署会取新值"）。
func (svc *SharedVariablesService) PutSharedVariable(ctx context.Context, req *structurev1.PutSharedVariableRequest) (*structurev1.PutSharedVariableResponse, error) {
	// project_id/name 必填组合、name 的 env 键形态检查（spec.ValidEnvName）
	// 与 value 单值上限检查（16KiB）已退役为 PutSharedVariableRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，组合必填拆 per-field 文案；正则字面单源见
	// spec.EnvNamePattern（spec 包 descriptor 对齐测试执法）。
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &sharedvariable.SharedVariable{
		ProjectID: req.GetProjectId(), Name: req.GetName(), Value: req.GetValue(),
	}
	// 数量配额按"Project 内活跃名数"计（同名 put 是覆盖不占新位）——
	// 受理位检查（事务内读，ADR-0024 同款）。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.sharedVariableQuota(req.GetProjectId(), req.GetName()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.SharedVariables.Upsert(ctx, tx, row)
		},
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventVariableUpdated, Aggregate: "variable", ID: row.Name,
					Payload: mustJSON(structureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
					TeamID:  team,
					Action:  "variable.put", Resource: "variable/" + row.Name, AfterFP: row.Value,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	affected, err := svc.s.affectedApps(ctx, req.GetProjectId(), req.GetName(), req.GetValue())
	if err != nil {
		return nil, err
	}
	return &structurev1.PutSharedVariableResponse{Variable: sharedVariableMsg(*row), AffectedApps: affected}, nil
}

func (svc *SharedVariablesService) ListSharedVariables(ctx context.Context, req *structurev1.ListSharedVariablesRequest) (*structurev1.ListSharedVariablesResponse, error) {
	// 行级授权（ADR-0035）：值明文回显（非敏感契约）——跨租户读面。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.SharedVariables.ListPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	out := &structurev1.ListSharedVariablesResponse{}
	for _, row := range list {
		out.Variables = append(out.Variables, sharedVariableMsg(row))
	}
	return out, nil
}

func (svc *SharedVariablesService) DeleteSharedVariable(ctx context.Context, req *structurev1.DeleteSharedVariableRequest) (*structurev1.DeleteSharedVariableResponse, error) {
	// project_id/name 必填组合检查已退役为 DeleteSharedVariableRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，拆 per-field 文案。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	err := svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.SharedVariables.SoftDelete(ctx, tx, req.GetProjectId(), req.GetName())
		},
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, req.GetProjectId(), func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventVariableDeleted, Aggregate: "variable", ID: req.GetName(),
					Payload: mustJSON(structureEventPayload{ID: req.GetName(), ProjectID: req.GetProjectId()}),
					TeamID:  team,
					Action:  "variable.delete", Resource: "variable/" + req.GetName(),
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	// 删除后的受影响口径：键从共享层消失——最新冻结 env 仍带该键的 App
	// 重部署会失去它（absentValue 哨兵）。
	affected, err := svc.s.affectedApps(ctx, req.GetProjectId(), req.GetName(), absentValue)
	if err != nil {
		return nil, err
	}
	return &structurev1.DeleteSharedVariableResponse{AffectedApps: affected}, nil
}

// absentValue 是受影响口径的"键已从共享层消失"哨兵：任一进程 env 带该键
// 即受影响（冻结值与新状态恒不等）。
const absentValue = "\x00absent"

// sharedVariableQuota 是 per-Project 共享变量数配额检查（Configs 同款：
// 新名才占新位）。
func (s *Services) sharedVariableQuota(projectID, name string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := s.SharedVariables.GetByName(ctx, tx, projectID, name); err == nil {
			return nil
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		}
		active, err := s.SharedVariables.ListActive(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if len(active) >= maxSharedVariablesPerProject {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s already holds %d shared variables (limit %d)", projectID, len(active), maxSharedVariablesPerProject)
		}
		return nil
	}
}

// ---- AppVariable（ADR-0072 三级合成顶层）----

type AppVariablesService struct {
	structurev1.UnimplementedAppVariablesServiceServer
	s *Services
}

// PutAppVariable 落/覆盖一条 App 级平台变量。改变量不触发自动重部署——
// 重部署（Redeploy/Deploy/push）在冻结咽喉取新值合成。
func (svc *AppVariablesService) PutAppVariable(ctx context.Context, req *structurev1.PutAppVariableRequest) (*structurev1.PutAppVariableResponse, error) {
	// app_id/name 必填组合、name 的 env 键形态检查（spec.ValidEnvName）与
	// value 单值上限检查（16KiB）已退役为 PutAppVariableRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，组合必填拆 per-field 文案；正则字面单源见
	// spec.EnvNamePattern（spec 包 descriptor 对齐测试执法）。
	// 行级授权（ADR-0035）：App 域写面前置（含 App 存在性——变量不挂
	// 幽灵 App）。
	a, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := svc.s.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	row := &appvariable.AppVariable{AppID: req.GetAppId(), Name: req.GetName(), Value: req.GetValue()}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.appVariableQuota(req.GetAppId(), req.GetName())},
		write:  func(ctx context.Context, tx *sql.Tx) error { return svc.s.AppVariables.Upsert(ctx, tx, row) },
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, a.ProjectID, func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventAppVarUpdated, Aggregate: "app_variable", ID: a.ID + "/" + row.Name,
					Payload: mustJSON(structureEventPayload{ID: a.ID + "/" + row.Name, ProjectID: a.ProjectID}),
					TeamID:  team,
					Action:  "app_variable.put", Resource: "app_variable/" + a.ID + "/" + row.Name, AfterFP: row.Value,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	return &structurev1.PutAppVariableResponse{Variable: appVariableMsg(*row)}, nil
}

func (svc *AppVariablesService) ListAppVariables(ctx context.Context, req *structurev1.ListAppVariablesRequest) (*structurev1.ListAppVariablesResponse, error) {
	// app_id 必填检查已退役为 ListAppVariablesRequest 的 protovalidate CEL
	// 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	// 行级授权（ADR-0035）：值明文回显（非敏感契约）——跨租户读面。
	if err := svc.s.authorizeAppIDOnly(ctx, req.GetAppId()); err != nil {
		return nil, err
	}
	list, err := svc.s.AppVariables.ListPage(ctx, svc.s.DB.Runner(),
		req.GetAppId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	out := &structurev1.ListAppVariablesResponse{}
	for _, row := range list {
		out.Variables = append(out.Variables, appVariableMsg(row))
	}
	return out, nil
}

func (svc *AppVariablesService) DeleteAppVariable(ctx context.Context, req *structurev1.DeleteAppVariableRequest) (*structurev1.DeleteAppVariableResponse, error) {
	// app_id/name 必填组合检查已退役为 DeleteAppVariableRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，拆 per-field 文案。
	a, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	if err := svc.s.authorizeAppRow(ctx, a); err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.AppVariables.SoftDelete(ctx, tx, req.GetAppId(), req.GetName())
		},
		facts: []func() writes.FactRow{
			svc.s.factOf(ctx, a.ProjectID, func(team string) writes.FactRow {
				return writes.FactRow{
					Name: eventAppVarDeleted, Aggregate: "app_variable", ID: a.ID + "/" + req.GetName(),
					Payload: mustJSON(structureEventPayload{ID: a.ID + "/" + req.GetName(), ProjectID: a.ProjectID}),
					TeamID:  team,
					Action:  "app_variable.delete", Resource: "app_variable/" + a.ID + "/" + req.GetName(),
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			}),
		},
	})
	if err != nil {
		return nil, mapStateError(err, "variable")
	}
	return &structurev1.DeleteAppVariableResponse{}, nil
}

// appVariableQuota 是 per-App 变量数配额检查（共享变量同款：新名才占
// 新位）。
func (s *Services) appVariableQuota(appID, name string) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := s.AppVariables.GetByName(ctx, tx, appID, name); err == nil {
			return nil
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		}
		active, err := s.AppVariables.ListActive(ctx, tx, appID)
		if err != nil {
			return err
		}
		if len(active) >= maxAppVariablesPerApp {
			return apperr.New("E_QUOTA_EXCEEDED",
				"app %s already holds %d variables (limit %d)", appID, len(active), maxAppVariablesPerApp)
		}
		return nil
	}
}

// affectedApps 计算受影响 App 提示（ADR-0043 决策 4 的近似口径）：项目内
// 有 ≥1 Revision 的 App，其最新冻结 env 会因本次变更而不同（任一进程
// env[KEY] 与变更后状态不等；键缺席亦算——重冻结会补进）。App 层覆盖
// 同键的 App 会误报（冻结形态是扁平 map 无键来源账本）——提示是 DX 附注
// 不是行为承诺，误报代价 = 一次幂等重部署。
func (s *Services) affectedApps(ctx context.Context, projectID, name, newValue string) ([]string, error) {
	apps, err := s.Apps.ListByProject(ctx, s.DB.Runner(), projectID)
	if err != nil {
		return nil, err
	}
	var affected []string
	for _, a := range apps {
		rev, err := s.Revisions.Latest(ctx, s.DB.Runner(), a.ID)
		if errors.Is(err, state.ErrNotFound) {
			continue // 从未部署过的 App 首次部署自然取新值
		}
		if err != nil {
			return nil, err
		}
		if revisionUsesDifferentValue(rev.Spec, name, newValue) {
			affected = append(affected, a.ID)
		}
	}
	return affected, nil
}

// revisionUsesDifferentValue 报告冻结 spec 的任一进程 env[KEY] 与变更后
// 状态不同（absentValue 哨兵 = 键应消失：在场即不同）。反序列化失败按
// 不受影响处理——提示面不因冻结体解析问题放大故障。
func revisionUsesDifferentValue(blob []byte, name, newValue string) bool {
	s := &specv1.AppSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, s); err != nil {
		return false
	}
	differs := func(env map[string]string) bool {
		v, ok := env[name]
		if newValue == absentValue {
			return ok
		}
		return !ok || v != newValue
	}
	for _, p := range s.GetProcesses() {
		if differs(p.GetEnv()) {
			return true
		}
	}
	for _, j := range s.GetFirstBootJobs() {
		if differs(j.GetProcess().GetEnv()) {
			return true
		}
	}
	return false
}

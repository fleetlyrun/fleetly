package api

// DatabaseService 实现 server.v1.DatabaseService（E4 数据库托管，managed-
// databases §2.3 操作表 + §5.1 契约的 S2 子集）。库实例是独立一等资源
// （D-DB-1）：受理 = db_instances 行（provisioning）+ 凭据生成一次 + 事件
// 与审计同事务 fail-closed；收敛由 internal/database duty 异步承载——API
// 只做受理/守卫/投影，绝不触底座。
//
// 错误映射（D-DB-8 零新增码）：
//
//	state.ErrDatabaseNotFound           → E_DB_NOT_FOUND 404
//	state.ErrDatabaseStateConflict      → E_STATE_VERSION_CONFLICT 409
//	                                      （context 带 current_state + 合法
//	                                      前置态清单——同族乐观冲突语义）
//	state.ErrDatabaseIllegalTransition  → 同上 409 族
//	state.ErrDatabaseTerminal           → E_DB_NOT_FOUND 404（deleting/
//	                                      deleted 的操作目标按设计语义不可见）
//	dbtemplate.ErrUnknownTemplate       → E_DB_TEMPLATE_UNSUPPORTED 400
//	state.ErrDatabaseExists             → 409 退化信封（名字占用，无注册码）
//
// 明文纪律：凭据明文只存活于「生成 → 加密落库」与「解密 → 指纹/掩码投影」
// 的内存链；响应只带掩码 URL 与 hash8 指纹（reveal 面随 S4/S6）。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// connectionPasswordMask 是连接投影的密码掩码（固定形态——明文零离开存储；
// 显式 reveal 属 S4/S6 面）。
const connectionPasswordMask = "********"

// ProvisionKicker 是创建/恢复/重试受理后的收敛触发端口（实现方 = internal/
// database.Manager——接口在本包定义，方向纪律：api 定义端口、不感知实现
// 类型）。nil 容忍：未装配时受理照常成功，收敛等 duty 下一拍（≤10s）。
type ProvisionKicker interface {
	Kick()
}

// DatabaseService 实现 server.v1.DatabaseService。
type DatabaseService struct {
	serverv1.UnimplementedDatabaseServiceServer
	st   *state.Store
	box  *secrets.Box
	kick ProvisionKicker
}

// NewDatabaseService 构造 DatabaseService（box 是凭据生成/指纹的加解密器；
// kick 可 nil——受理后即时收敛拍，缺省时收敛由 duty 周期拍兜底）。
func NewDatabaseService(st *state.Store, box *secrets.Box, kick ProvisionKicker) *DatabaseService {
	return &DatabaseService{st: st, box: box, kick: kick}
}

// kickOnce 受理成功后的即时收敛请求（失败静默——kick 只是提前，不承载正
// 确性：错过的 kick 由 duty 周期拍消化）。
func (s *DatabaseService) kickOnce() {
	if s.kick != nil {
		s.kick.Kick()
	}
}

// CreateDatabase 创建库实例：模板校验（未知 → 400）→ 名校验 → 凭据生成 +
// 加密 → 实例行 + db.provision_started 事件 + 审计 db.create 同事务
// fail-closed。
func (s *DatabaseService) CreateDatabase(ctx context.Context, req *serverv1.CreateDatabaseRequest) (*serverv1.CreateDatabaseResponse, error) {
	tpl, err := dbtemplate.Get(req.GetTemplate())
	if err != nil {
		return nil, apperr.New("E_DB_TEMPLATE_UNSUPPORTED",
			"template %q is not in the platform registry (available: %s)", req.GetTemplate(), templateIDList()).
			WithContext("template", req.GetTemplate()).
			WithContext("available", templateIDList())
	}
	if err := state.ValidateDatabaseName(req.GetName()); err != nil {
		return nil, statusInvalidArgument(err.Error())
	}
	password, err := dbtemplate.GeneratePassword()
	if err != nil {
		return nil, fmt.Errorf("generate database credential: %w", err)
	}
	cipher, err := s.box.Encrypt([]byte(password))
	if err != nil {
		return nil, fmt.Errorf("encrypt database credential: %w", err)
	}
	var created state.DatabaseInstance
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		row, err := tx.CreateDatabaseInstance(ctx, state.DatabaseInstance{
			Name:             req.GetName(),
			Template:         req.GetTemplate(),
			ImageDigest:      tpl.Image,
			Settings:         databaseSettingsOf(req.GetLimits(), req.GetBackupPlan()),
			CredentialCipher: string(cipher),
		})
		if err != nil {
			return err
		}
		created = row
		if _, err := tx.AppendEvent(ctx, databaseLifecycleEvent("db.provision_started", row)); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, databaseAudit(ctx, "db.create", "database:"+row.ID,
			state.DiffSummary("template", row.Template)))
	})
	switch {
	case err == nil:
	case errors.Is(err, state.ErrDatabaseExists):
		return nil, conflict(fmt.Sprintf("database instance name %q is already registered (names stay reserved across the lifecycle)", req.GetName()))
	default:
		return nil, err
	}
	s.kickOnce()
	view, err := s.databaseView(ctx, created)
	if err != nil {
		return nil, err
	}
	return &serverv1.CreateDatabaseResponse{Database: view}, nil
}

// GetDatabase 库实例详情（脱敏投影）。
func (s *DatabaseService) GetDatabase(ctx context.Context, req *serverv1.GetDatabaseRequest) (*serverv1.GetDatabaseResponse, error) {
	inst, err := s.st.GetDatabaseInstanceByName(ctx, req.GetName())
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	view, err := s.databaseView(ctx, inst)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetDatabaseResponse{Database: view}, nil
}

// ListDatabases 库实例列表（name 字典序；deleted tombstone 不进默认列表
// ——与 apps 列表同口径）。
func (s *DatabaseService) ListDatabases(ctx context.Context, req *serverv1.ListDatabasesRequest) (*serverv1.ListDatabasesResponse, error) {
	rows, err := s.st.ListDatabaseInstances(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	out := make([]*serverv1.DatabaseView, 0, len(rows))
	for _, inst := range rows {
		if inst.State == state.DatabaseDeleted {
			continue
		}
		if len(out) >= limit {
			break
		}
		view, err := s.databaseView(ctx, inst)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return &serverv1.ListDatabasesResponse{Databases: out}, nil
}

// DeleteDatabase 删除受理：终端态守卫 → confirm 两段式 → 引用守卫
// （E_DB_REFERENCED 409 附引用清单）→ 卷处置选择落位 + deleting 转移 +
// 审计 db.delete 同事务；reap 由收敛 duty 幂等完成。
func (s *DatabaseService) DeleteDatabase(ctx context.Context, req *serverv1.DeleteDatabaseRequest) (*serverv1.DeleteDatabaseResponse, error) {
	inst, err := s.st.GetDatabaseInstanceByName(ctx, req.GetName())
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	if inst.State == state.DatabaseDeleting || inst.State == state.DatabaseDeleted {
		return nil, databaseNotFound(inst.Name, "already "+string(inst.State))
	}
	if req.GetConfirm() != inst.Name {
		return nil, statusInvalidArgument(
			"destructive operation: pass confirm=\"" + inst.Name + "\" to accept deletion (data volumes are kept by default; delete_volumes=true discards them irreversibly)")
	}
	// 删除前置态前哨（§2.3 操作表：ready/degraded/paused/failed——
	// provisioning 是收敛在途，等健康门收口或失败后再删；表外组合的显式
	// 409，context 带合法前置态清单）。
	switch inst.State {
	case state.DatabaseReady, state.DatabaseDegraded, state.DatabasePaused, state.DatabaseFailed:
	default:
		return nil, apperr.New("E_STATE_VERSION_CONFLICT",
			"database %q is in state %q; delete requires one of: ready, degraded, paused, failed (wait for the health gate or retry after failure)",
			inst.Name, inst.State).
			WithContext("current_state", string(inst.State)).
			WithContext("legal_prestates", "ready, degraded, paused, failed")
	}
	refs, err := s.st.ListDatabaseReferencesByDB(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	if len(refs) > 0 {
		return nil, s.referencedErr(ctx, inst, refs)
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.SetDatabaseDeleteVolumes(ctx, inst.ID, req.GetDeleteVolumes()); err != nil {
			return err
		}
		if err := tx.EnterDbPhase(ctx, inst.ID, inst.State, state.DatabaseDeleting,
			databaseLifecycleEvent("db.delete_started", inst)); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, databaseAudit(ctx, "db.delete", "database:"+inst.ID,
			state.DiffSummary("from", string(inst.State), "delete_volumes", req.GetDeleteVolumes())))
	})
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	s.kickOnce()
	return &serverv1.DeleteDatabaseResponse{Name: inst.Name, Status: string(state.DatabaseDeleting)}, nil
}

// SuspendDatabase 暂停（ready/degraded → paused）：scale-0 由收敛 duty 落
// 地；引用方连不上是诚实暴露（设计 §2.3）。
func (s *DatabaseService) SuspendDatabase(ctx context.Context, req *serverv1.SuspendDatabaseRequest) (*serverv1.SuspendDatabaseResponse, error) {
	inst, err := s.getMutableInstance(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.EnterDbPhase(ctx, inst.ID, inst.State, state.DatabasePaused,
			databaseLifecycleEvent("db.suspended", inst)); err == nil {
			return tx.WriteAudit(ctx, databaseAudit(ctx, "db.suspend", "database:"+inst.ID,
				state.DiffSummary("from", string(inst.State))))
		} else if !errors.Is(err, state.ErrDatabaseStateConflict) || inst.State != state.DatabaseReady {
			return err
		}
		// ready 期 CAS 落败 = 并发已到 degraded（观察 duty 抢先）：按
		// degraded 前置态重试（操作表第二合法前置态）。
		return tx.EnterDbPhase(ctx, inst.ID, state.DatabaseDegraded, state.DatabasePaused,
			databaseLifecycleEvent("db.suspended", inst))
	})
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	s.kickOnce()
	view, err := s.viewAfter(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	return &serverv1.SuspendDatabaseResponse{Database: view}, nil
}

// ResumeDatabase 恢复（paused → provisioning 重收敛）。
func (s *DatabaseService) ResumeDatabase(ctx context.Context, req *serverv1.ResumeDatabaseRequest) (*serverv1.ResumeDatabaseResponse, error) {
	inst, err := s.getMutableInstance(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.EnterDbPhase(ctx, inst.ID, state.DatabasePaused, state.DatabaseProvisioning,
			databaseLifecycleEvent("db.resumed", inst)); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, databaseAudit(ctx, "db.resume", "database:"+inst.ID,
			state.DiffSummary("from", string(inst.State))))
	})
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	s.kickOnce()
	view, err := s.viewAfter(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	return &serverv1.ResumeDatabaseResponse{Database: view}, nil
}

// RetryDatabase 显式重试（failed → provisioning；失败现场保留语义下的唯一
// 出边；收敛过健康门时 last_error 清空）。
func (s *DatabaseService) RetryDatabase(ctx context.Context, req *serverv1.RetryDatabaseRequest) (*serverv1.RetryDatabaseResponse, error) {
	inst, err := s.getMutableInstance(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	err = s.st.InTx(ctx, func(tx *state.Tx) error {
		if err := tx.EnterDbPhase(ctx, inst.ID, state.DatabaseFailed, state.DatabaseProvisioning,
			databaseLifecycleEvent("db.provision_started", inst)); err != nil {
			return err
		}
		return tx.WriteAudit(ctx, databaseAudit(ctx, "db.retry", "database:"+inst.ID,
			state.DiffSummary("from", string(inst.State))))
	})
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	s.kickOnce()
	view, err := s.viewAfter(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	return &serverv1.RetryDatabaseResponse{Database: view}, nil
}

// UpdateDatabaseSettings 设置变更（限额 + 备份计划；任意非终态准入、主状态
// 不变；限额变更的 spec 重建由收敛器在下一拍以 desired-hash 判据承载——
// 文档口径，非本 RPC 内动作）。
func (s *DatabaseService) UpdateDatabaseSettings(ctx context.Context, req *serverv1.UpdateDatabaseSettingsRequest) (*serverv1.UpdateDatabaseSettingsResponse, error) {
	inst, err := s.getMutableInstance(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	settings := databaseSettingsOf(req.GetLimits(), req.GetBackupPlan())
	if err := s.st.UpdateDatabaseSettings(ctx, inst.ID, settings); err != nil {
		return nil, mapDatabaseErr(err)
	}
	// 设置变更不在设计审计词表（§5.3）——走方法级 action（api.DatabaseService.
	// UpdateDatabaseSettings，拦截器注入的同款通用词根）。
	if err := s.st.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, auditEntry(ctx, "database:"+inst.ID,
			state.DiffSummary("cpu_seconds", settings.CPUSeconds, "memory_bytes", settings.MemoryBytes)))
	}); err != nil {
		return nil, err
	}
	view, err := s.viewAfter(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	return &serverv1.UpdateDatabaseSettingsResponse{Database: view}, nil
}

// ── 内部协作者 ──────────────────────────────────────────────────────────────

// getMutableInstance 取非终态实例（deleting/deleted 的操作目标 → 404——
// E_DB_NOT_FOUND 的「已进入 deleting/deleted」语义行）。
func (s *DatabaseService) getMutableInstance(ctx context.Context, name string) (state.DatabaseInstance, error) {
	inst, err := s.st.GetDatabaseInstanceByName(ctx, name)
	if err != nil {
		return state.DatabaseInstance{}, mapDatabaseErr(err)
	}
	if inst.State == state.DatabaseDeleting || inst.State == state.DatabaseDeleted {
		return state.DatabaseInstance{}, databaseNotFound(inst.Name, "already "+string(inst.State))
	}
	return inst, nil
}

// viewAfter 转移后重读并投影（Suspend/Resume/Retry/Settings 的响应面）。
func (s *DatabaseService) viewAfter(ctx context.Context, id string) (*serverv1.DatabaseView, error) {
	inst, err := s.st.GetDatabaseInstance(ctx, id)
	if err != nil {
		return nil, mapDatabaseErr(err)
	}
	return s.databaseView(ctx, inst)
}

// referencedErr 构造引用守卫冲突（E_DB_REFERENCED 409；context 列出引用
// app/服务清单——数据安全前哨的可行动面，设计 §2.4。清单以 app 名呈现——
// 引用行存 app_id，名字解析失败的孤儿行如实回退 ID）。
func (s *DatabaseService) referencedErr(ctx context.Context, inst state.DatabaseInstance, refs []state.DatabaseReference) error {
	list := make([]string, 0, len(refs))
	for _, r := range refs {
		refName := r.AppID
		if app, err := s.st.GetAppByID(ctx, r.AppID); err == nil {
			refName = app.Name
		}
		list = append(list, refName+"/"+r.Service)
	}
	return apperr.New("E_DB_REFERENCED",
		"database %q is referenced by %d app service(s) and cannot be deleted: %s (remove the fleetly.databases label and redeploy the referencing apps first)",
		inst.Name, len(refs), strings.Join(list, ", ")).
		WithContext("references", strings.Join(list, ",")).
		WithContext("count", fmt.Sprint(len(refs)))
}

// mapDatabaseErr 是状态层哨兵 → api 语义的统一映射（D-DB-8）。
func mapDatabaseErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrDatabaseNotFound):
		return databaseNotFound("", "")
	case errors.Is(err, state.ErrDatabaseStateConflict), errors.Is(err, state.ErrDatabaseIllegalTransition):
		return apperr.New("E_STATE_VERSION_CONFLICT",
			"database changed concurrently (CAS mismatch) — re-read the current state and retry with a legal prestate").
			WithContext("conflict", err.Error())
	case errors.Is(err, state.ErrDatabaseTerminal):
		return databaseNotFound("", "terminal state")
	default:
		return err
	}
}

// databaseNotFound 构造 E_DB_NOT_FOUND（404；细节进 message 与 context——可行动
// 面设计 §5.2「附候选清单」口径）。
func databaseNotFound(name, detail string) error {
	msg := "database instance not found"
	if name != "" {
		msg += ": " + name
	}
	if detail != "" {
		msg += " (" + detail + ")"
	}
	return apperr.New("E_DB_NOT_FOUND", "%s", msg).
		WithContext("name", name).
		WithContext("detail", detail)
}

// databaseSettingsOf 组装 settings（proto → state）。
func databaseSettingsOf(l *serverv1.DatabaseLimits, p *serverv1.DatabaseBackupPlan) state.DatabaseSettings {
	return state.DatabaseSettings{
		CPUSeconds:  l.GetCpuSeconds(),
		MemoryBytes: l.GetMemoryBytes(),
		Backup:      databaseBackupPlanOf(p),
	}
}

func databaseBackupPlanOf(p *serverv1.DatabaseBackupPlan) state.DatabaseBackupPlan {
	return state.DatabaseBackupPlan{
		IntervalHours: int(p.GetIntervalHours()),
		Keep:          int(p.GetKeep()),
		HourUTC:       int(p.GetHourUtc()),
	}
}

// templateIDList 是注册表词表的人读形态（错误信息可行动面）。
func templateIDList() string {
	var ids []string
	for _, t := range dbtemplate.List() {
		ids = append(ids, t.ID)
	}
	return strings.Join(ids, ", ")
}

// databaseView 构造脱敏投影：解密凭据只为指纹（naming.Hash8——「是不是那
// 个值」的比对面），明文零离开本函数内存链。
func (s *DatabaseService) databaseView(ctx context.Context, inst state.DatabaseInstance) (*serverv1.DatabaseView, error) {
	fingerprint := ""
	if inst.CredentialCipher != "" {
		plain, err := s.box.Decrypt([]byte(inst.CredentialCipher))
		if err != nil {
			return nil, fmt.Errorf("decrypt credential of database %s: %w", inst.Name, err)
		}
		fingerprint = naming.Hash8(string(plain))
	}
	v := &serverv1.DatabaseView{
		Id:          inst.ID,
		Name:        inst.Name,
		Template:    inst.Template,
		ImageDigest: inst.ImageDigest,
		Status:      string(inst.State),
		Placement:   inst.PlatformNodeID,
		LastError:   inst.LastError,
		CreatedAt:   timestamppb.New(inst.CreatedAt),
		UpdatedAt:   timestamppb.New(inst.UpdatedAt),
		Limits: &serverv1.DatabaseLimits{
			CpuSeconds:  inst.Settings.CPUSeconds,
			MemoryBytes: inst.Settings.MemoryBytes,
		},
		BackupPlan: &serverv1.DatabaseBackupPlan{
			IntervalHours: int32(inst.Settings.Backup.IntervalHours),
			Keep:          int32(inst.Settings.Backup.Keep),
			HourUtc:       int32(inst.Settings.Backup.HourUTC),
		},
	}
	if !inst.CredentialUpdatedAt.IsZero() {
		v.CredentialUpdatedAt = timestamppb.New(inst.CredentialUpdatedAt)
	}
	if fingerprint != "" {
		v.Connection = connectionView(inst, fingerprint)
	}
	if vols, err := s.st.ListOwnerVolumes(ctx, state.VolumeOwnerDatabase, inst.ID); err == nil && len(vols) > 0 {
		v.Volume = &serverv1.DatabaseVolumeView{
			Name:           vols[0].Name,
			Status:         string(vols[0].Status),
			PlatformNodeId: vols[0].PlatformNodeID,
		}
	}
	return v, nil
}

// connectionView 连接信息脱敏投影（§2.5 键集只读子集；host = 实例名 DNS
// 别名——引用方 app 内的可达名；url 密码段为固定掩码）。
func connectionView(inst state.DatabaseInstance, fingerprint string) *serverv1.DatabaseConnectionView {
	tpl, err := dbtemplate.Get(inst.Template)
	if err != nil {
		return nil // 模板在册期理论不可达（注册表只增）
	}
	vars, err := dbtemplate.ConnectionVars(inst.Template, inst.Name, connectionPasswordMask)
	if err != nil {
		return nil
	}
	prefix := dbtemplate.EnvPrefix(inst.Name)
	out := &serverv1.DatabaseConnectionView{
		Host:                vars[prefix+"_HOST"],
		Port:                int32(tpl.EnginePort),
		Url:                 vars[prefix+"_URL"],
		PasswordFingerprint: fingerprint,
	}
	if user, ok := vars[prefix+"_USER"]; ok {
		out.User = user
	}
	if pgDatabaseName, ok := vars[prefix+"_DATABASE"]; ok {
		out.Database = pgDatabaseName
	}
	return out
}

// databaseAudit 构造库操作审计条目（设计 §5.3 动作词表 db.*——与通用 auditEntry
// 的 api.* 方法级词根分立；actor 归因同款：API 无法区分人类/AI 代理，
// token 承载可追溯性）。
func databaseAudit(ctx context.Context, action, target, diff string) state.AuditEntry {
	tokenID := ""
	if p, ok := PrincipalFromContext(ctx); ok {
		tokenID = p.TokenID
	}
	return state.AuditEntry{
		Actor:        "human",
		ActorTokenID: tokenID,
		Action:       action,
		Target:       target,
		Result:       "ok",
		DiffSummary:  diff,
	}
}

// databaseLifecycleEvent 是生命周期受理事件（subject = database:<名>；payload 只
// 带事实字段——凭据材料零出现）。
func databaseLifecycleEvent(name string, inst state.DatabaseInstance) state.Event {
	return state.Event{
		Name:    name,
		Subject: "database:" + inst.Name,
		Payload: fmt.Sprintf(`{"instance":%q,"template":%q}`, inst.Name, inst.Template),
	}
}

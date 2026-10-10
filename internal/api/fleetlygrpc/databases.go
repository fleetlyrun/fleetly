package fleetlygrpc

// DatabasesService 实现（F1.12，ADR-0029）：托管数据服务聚合面。凭证
// 单真源 = Project Secret database:<name>（值 = 完整连接 URL，永不在响应
// 出现）；创建受理位四检查（父项目存活/配额/engine 值域/项目有活跃网络
// ——零网项目的库不可达，拒绝优于静默孤岛）；删除收口走 engine 拆载体
// 后 tombstone 一事务（先变更后留痕，ADR-0023 同款序）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/lynx-go/grpcapi/authz"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/engine/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/backup"
	browserepo "github.com/fleetlyrun/fleetly/internal/state/browse"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
)

// 数据库面事件名（usage 反扫的字面量锚点）。
const (
	eventDatabaseCreated       = "database.created"
	eventDatabaseBrowserOpened = "database.browser_opened"
	eventDatabaseDeleted       = "database.deleted"
	// 轮换事实（IA v3 二期⑤b）：engine 改密 + Secret 重写后由受理位与
	// 审计同事务落账（DeleteDatabase 同款先变更后留痕序）。
	eventDatabaseRotated = "database.credentials_rotated"
)

// maxDatabasesPerProject 是 per-Project 活跃数据库上限（ADR-0017 配额
// 族；库是长驻真实资源，保守缺省）。
const maxDatabasesPerProject = 16

type DatabasesService struct {
	structurev1.UnimplementedDatabasesServiceServer
	s *Services
}

// CreateDatabase：铸造凭证（随机密码 → 完整连接 URL → age 信封落
// Secret）+ 聚合行，一事务落账后 Kick 收敛环。同名活跃行冲突 →
// E_ALREADY_EXISTS；engine 值域外 → E_INVALID_ARGUMENT（列合法值）。
func (svc *DatabasesService) CreateDatabase(ctx context.Context, req *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" || req.GetEngine() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id, name and engine: must not be empty")
	}
	if _, ok := dbtemplate.InfoFor(req.GetEngine()); !ok {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine: %q is not a registered template (available: %v)", req.GetEngine(), dbtemplate.Engines())
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}

	id := newID()
	url, err := engine.DatabaseConnectionURL(req.GetEngine(), id, mintDatabasePassword())
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "database connection url could not be minted").WithCause(err)
	}
	// 恢复源受理（ADR-0039）：同 Project + 引擎一致 + 成功有产物；挂起位
	// 与聚合行同事务落（重启安全——备份环按行重放）。
	var restoreSrc *backup.Backup
	if req.GetRestoreFromBackup() != "" {
		src, err := svc.s.Backups.Get(ctx, svc.s.DB.Runner(), req.GetRestoreFromBackup())
		if err != nil {
			return nil, mapStateError(err, "backup")
		}
		if src.ProjectID != req.GetProjectId() {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s belongs to another project", src.ID)
		}
		if src.Engine != req.GetEngine() {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup engine %q does not match the requested engine %q", src.Engine, req.GetEngine())
		}
		if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s is not a succeeded backup with an object", src.ID)
		}
		restoreSrc = src
	}
	sealed, err := svc.s.Cipher.Produce([]byte(url))
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "database credential could not be sealed").WithCause(err)
	}
	secretName := engine.DBCredentialSecretName(req.GetName())
	secretRow := &secret.Secret{
		ID: newID(), ProjectID: req.GetProjectId(), Name: secretName,
		Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
	}
	row := &dbrepo.Database{
		ID: id, ProjectID: req.GetProjectId(), Name: req.GetName(),
		Engine: req.GetEngine(), CredentialsRef: secretName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.databaseQuota(req.GetProjectId()),
			svc.s.projectHasNetwork(req.GetProjectId()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Secrets.Upsert(ctx, tx, secretRow); err != nil {
				return err
			}
			if err := svc.s.Databases.Create(ctx, tx, row); err != nil {
				return err
			}
			if restoreSrc != nil {
				return svc.s.Databases.SetRestorePending(ctx, tx, row.ID, restoreSrc.ID)
			}
			return nil
		},
		events: []eventFact{structureEvent(eventDatabaseCreated, "database", row.ID, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.create",
			Resource: "database/" + row.ID, AfterFP: row.Engine + " " + row.Name,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	svc.s.Engine.KickDatabases()
	if restoreSrc != nil {
		svc.s.Engine.KickBackups()
	}
	return &structurev1.CreateDatabaseResponse{Database: databaseMsg(row)}, nil
}

func (svc *DatabasesService) GetDatabase(ctx context.Context, req *structurev1.GetDatabaseRequest) (*structurev1.GetDatabaseResponse, error) {
	row, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &structurev1.GetDatabaseResponse{Database: databaseMsg(row)}, nil
}

// ListDatabases 新→旧分页（ADR-0026 after_* + limit）。
func (svc *DatabasesService) ListDatabases(ctx context.Context, req *structurev1.ListDatabasesRequest) (*structurev1.ListDatabasesResponse, error) {
	// 空 project_id 先拒（E_INVALID_ARGUMENT）——与 ListApps 同口径；此前
	// 落到行级授权的 E_NOT_FOUND（404），调用方把"缺参"误判为"项目不存在"
	//（N3 交接 O1 收口，2026-10-06）。
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Databases.ListByProject(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterDatabaseId(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	out := &structurev1.ListDatabasesResponse{}
	for i := range list {
		out.Databases = append(out.Databases, databaseMsg(&list[i]))
	}
	return out, nil
}

// DeleteDatabase 收口删除（ADR-0029 决策 8）：engine 拆载体（幂等）→
// tombstone + 事件 + 审计一事务。数据卷与凭证 Secret 不随删（Project 级
// 材料，备份保留义——重建同名库复用卷、覆写凭证）。
func (svc *DatabasesService) DeleteDatabase(ctx context.Context, req *structurev1.DeleteDatabaseRequest) (*structurev1.DeleteDatabaseResponse, error) {
	if req.GetId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "id: must not be empty")
	}
	row, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.s.Engine.TeardownDatabase(ctx, req.GetId()); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "database %s not found", req.GetId())
		}
		return nil, apperr.New("E_INTERNAL", "database teardown failed").WithCause(err)
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Databases.SoftDelete(ctx, tx, req.GetId())
		},
		events: []eventFact{structureEvent(eventDatabaseDeleted, "database", req.GetId(), row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.delete",
			Resource: "database/" + req.GetId(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	svc.s.Engine.KickDatabases()
	return &structurev1.DeleteDatabaseResponse{}, nil
}

// RotateDatabasePassword 凭证轮换受理（IA v3 二期⑤b）：授权 + 在服检查
// 前置 → engine 方言改密 + Secret 重写（轮换真源在 engine/rotate.go）→
// 审计落账。事件由 engine 发射（database.credentials_rotated，紧贴 Secret
// 重写事实）；级联披露由调用方承担（Console 确认页/CLI 回执明示"引用库
// 的 App 须重新部署取新值"）。新连接串只进 Secret，响应永不回显。
func (svc *DatabasesService) RotateDatabasePassword(ctx context.Context, req *structurev1.RotateDatabasePasswordRequest) (*structurev1.RotateDatabasePasswordResponse, error) {
	if req.GetDatabaseId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "database_id: must not be empty")
	}
	row, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if row.Status != dbrepo.StatusRunning && row.Status != dbrepo.StatusDegraded {
		return nil, apperr.New("E_DATABASE_NOT_READY",
			"database %s is %s; rotation requires a running database (the data-plane change needs a live target)", row.Name, row.Status)
	}
	if err := svc.s.Engine.RotateDatabasePassword(ctx, row.ID); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "database %s not found", req.GetDatabaseId())
		}
		if errors.Is(err, engine.ErrRotateRejected) {
			return nil, apperr.New("E_DATABASE_ROTATE_FAILED", "%s", err.Error())
		}
		return nil, apperr.New("E_INTERNAL", "credential rotation failed").WithCause(err)
	}
	if err := svc.s.commit(ctx, writeFact{
		events: []eventFact{structureEvent(eventDatabaseRotated, "database", row.ID, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.rotate_password",
			Resource: "database/" + row.ID, AfterFP: row.CredentialsRef,
		}},
	}); err != nil {
		return nil, mapStateError(err, "database")
	}
	updated, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), row.ID)
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	return &structurev1.RotateDatabasePasswordResponse{Database: databaseMsg(updated)}, nil
}

// cascadeDeleteDatabases 级联收口项目下全部活跃 Database（DeleteProject
// 受理消费，2026-10-05 评审批台账 #4 / ADR-0029 追记）：逐库与
// DeleteDatabase 同款收口序——TeardownDatabase 先行（拆载体）→ tombstone
// + database.deleted 事件 + 审计一事务（先变更后留痕，ADR-0023 同款序）。
// 卷与凭证 Secret 不随级联删（Project 级材料，备份保留义——与单库删除
// 同口径）。失败语义：任一库失败即整体诚实失败（精确错误带库 ID 与
// name）；已收口的库保持 tombstone 不回滚——重试时枚举面自然跳过（幂等
// 收敛）。并发窗口：枚举后被他方单删的库按"已收口"跳过（NotFound 容
// 忍）；级联与项目 tombstone 之间新建的库由 DeleteProject 事务内的
// noActiveDatabases 复查拒绝（重试即收敛）。
func (s *Services) cascadeDeleteDatabases(ctx context.Context, projectID string) error {
	rows, err := s.listActiveDatabases(ctx, projectID)
	if err != nil {
		return err
	}
	for i := range rows {
		db := &rows[i]
		if err := s.Engine.TeardownDatabase(ctx, db.ID); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 并发单删已收口（幂等收敛）
			}
			return apperr.New("E_INTERNAL",
				"project delete: database %s (%s) teardown failed; databases torn down so far stay deleted - retry the project delete to converge",
				db.ID, db.Name).WithCause(err)
		}
		err = s.commit(ctx, writeFact{
			write: func(ctx context.Context, tx *sql.Tx) error {
				return s.Databases.SoftDelete(ctx, tx, db.ID)
			},
			events: []eventFact{structureEvent(eventDatabaseDeleted, "database", db.ID, db.ProjectID)},
			audits: []*audit.Entry{{
				ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.delete",
				Resource: "database/" + db.ID,
			}},
		})
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue // 并发赢家先落了 tombstone（幂等收敛）
			}
			return apperr.New("E_INTERNAL",
				"project delete: database %s (%s) could not be tombstoned", db.ID, db.Name).WithCause(err)
		}
	}
	if len(rows) > 0 {
		s.Engine.KickDatabases()
	}
	return nil
}

// listActiveDatabases 枚举项目下全部活跃 Database（级联的枚举面；
// ListByProject 分页循环——per-Project 配额 16 但配额可漂移，循环对
// 任意存量面收敛）。
func (s *Services) listActiveDatabases(ctx context.Context, projectID string) ([]dbrepo.Database, error) {
	var out []dbrepo.Database
	const page = 200 // 与 repo maxListLimit 对齐（超出值被钳回）
	after := ""
	for {
		rows, err := s.Databases.ListByProject(ctx, s.DB.Runner(), projectID, after, page)
		if err != nil {
			return nil, mapStateError(err, "database")
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, nil
		}
		after = rows[len(rows)-1].ID
	}
}

// TriggerBackup 手动触发（ADR-0039）：铸一行 pending 台账 + 审计，Kick
// 备份环；执行完成事实由事件与 ListBackups 观测（触发不等执行——
// 备份时长无上界承诺，同步面不成立）。
func (svc *DatabasesService) TriggerBackup(ctx context.Context, req *structurev1.TriggerBackupRequest) (*structurev1.TriggerBackupResponse, error) {
	if req.GetDatabaseId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "database_id: must not be empty")
	}
	db, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	row := &backup.Backup{
		ID: newID(), ProjectID: db.ProjectID, DatabaseID: db.ID,
		Engine: db.Engine, RetentionSecs: db.BackupRetentionSecs,
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Backups.Create(ctx, tx, row)
		},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.backup_trigger",
			Resource: "database/" + db.ID, AfterFP: row.ID,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	svc.s.Engine.KickBackups()
	return &structurev1.TriggerBackupResponse{Backup: backupMsg(row)}, nil
}

// ListBackups 新→旧分页（ADR-0026 after_* + limit）。
func (svc *DatabasesService) ListBackups(ctx context.Context, req *structurev1.ListBackupsRequest) (*structurev1.ListBackupsResponse, error) {
	if req.GetDatabaseId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "database_id: must not be empty")
	}
	db, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	list, err := svc.s.Backups.ListByDatabase(ctx, svc.s.DB.Runner(),
		req.GetDatabaseId(), req.GetAfterBackupId(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	out := &structurev1.ListBackupsResponse{}
	for i := range list {
		out.Backups = append(out.Backups, backupMsg(&list[i]))
	}
	return out, nil
}

// VerifyBackup 重算摘要比对回执（ADR-0039 决策 8；执行链未装配时精确
// 失败——不做"永远 ok"的假验证）。
func (svc *DatabasesService) VerifyBackup(ctx context.Context, req *structurev1.VerifyBackupRequest) (*structurev1.VerifyBackupResponse, error) {
	if req.GetBackupId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "backup_id: must not be empty")
	}
	row, err := svc.s.Backups.Get(ctx, svc.s.DB.Runner(), req.GetBackupId())
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	db, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), row.DatabaseID)
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	if row.Status != backup.StatusSucceeded || row.ObjectKey == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"backup %s is not a succeeded backup with an object (status %s)", row.ID, row.Status)
	}
	ok, detail, err := svc.s.Engine.VerifyBackup(ctx, req.GetBackupId())
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "backup verification failed").WithCause(err)
	}
	return &structurev1.VerifyBackupResponse{Ok: ok, Digest: row.Digest, Error: detail}, nil
}

// databaseMsg 行 → proto 映射（version/port 来自模板注册表单源；host 是
// 网内 DNS 名——诚实连接面，值/密码永不出现）。
func databaseMsg(row *dbrepo.Database) *structurev1.Database {
	msg := &structurev1.Database{
		Id: row.ID, ProjectId: row.ProjectID, Name: row.Name,
		Engine: row.Engine, CredentialsRef: row.CredentialsRef, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Host:              engine.DatabaseDNSName(row.ID),
		LastBackupAt:      row.LastBackupAt,
		RestoreFromBackup: row.RestoreFromBackup,
		RestoreError:      row.RestoreError,
	}
	if info, ok := dbtemplate.InfoFor(row.Engine); ok {
		msg.Version, msg.Port = info.Version, info.Port
	}
	return msg
}

// backupMsg 台账行 → proto 映射。
func backupMsg(row *backup.Backup) *structurev1.Backup {
	return &structurev1.Backup{
		Id: row.ID, ProjectId: row.ProjectID, DatabaseId: row.DatabaseID, Engine: row.Engine,
		ObjectKey: row.ObjectKey, Digest: row.Digest, SizeBytes: row.SizeBytes,
		Status: row.Status, Error: row.Error,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, CreatedAt: row.CreatedAt,
	}
}

// mintDatabasePassword 铸随机密码——公式单源已收敛到 dbtemplate.MintPassword
// （创建面与轮换面共用；IA v3 二期⑤b 收编）。
func mintDatabasePassword() string { return dbtemplate.MintPassword() }

// browse 票据常量（ADR-0051 决策 2）：120s TTL——实例冷启动（首次拉镜像）
// + 用户点击的窗口；exec 的 60s 不够。
const browseTicketTTL = 120 * time.Second

// BrowseDatabase 铸造数据浏览器会话（F3.6，ADR-0051）：受理四件一拍
// （browse 台账行 + database.browser_opened 事件 + database.browse 审计）
// → 引擎注册 + Kick → 铸 Launcher Ticket（120s 单用途）→ 回显入口 URL。
//
// 动态提权门（决策 6）：静态注解只表达最低门（databases:read）；
// read_write=true 或无只读执法的方言（mysql/adminer——读权用户不该拿到
// 可写控制台）在服务内要求 databases:write。freeze 豁免（诊断面同 exec
// 语义，ADR-0017 边界注记）。
func (svc *DatabasesService) BrowseDatabase(ctx context.Context, req *structurev1.BrowseDatabaseRequest) (*structurev1.BrowseDatabaseResponse, error) {
	if req.GetDatabaseId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "database_id: must not be empty")
	}
	if !svc.s.Engine.BrowseConfigured() {
		return nil, apperr.New("E_BROWSE_DISABLED",
			"the database browse face is not configured on this platform (browse.host_suffix is empty)")
	}
	db, err := svc.s.Databases.Get(ctx, svc.s.DB.Runner(), req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	if db.Status != dbrepo.StatusRunning && db.Status != dbrepo.StatusDegraded {
		return nil, apperr.New("E_DATABASE_NOT_READY",
			"database %s is %s; browse requires a running database (refused over an unusable session)", db.Name, db.Status)
	}
	browser, ok := dbbrowser.For(db.Engine)
	if !ok {
		return nil, apperr.New("E_BROWSER_UNSUPPORTED",
			"engine %q has no browse browser mapped in the dbbrowser registry", db.Engine)
	}
	readOnly := !req.GetReadWrite()
	if req.GetReadWrite() || browser.ReadOnlyEnforcement() == dbbrowser.EnforcementNone {
		if id, ok := authn.FromContext(ctx); !ok || !id.HasScope("databases", authz.ScopeWrite) {
			return nil, apperr.New("E_FORBIDDEN",
				"this browse form requires the databases:write scope (read_write, or engines with no read-only enforcement)").
				WithSuggestion("Open the session read-only (the default), or use a token carrying databases:write.")
		}
	}
	team, err := svc.s.Anchor.TeamOfProjectID(ctx, svc.s.DB.Runner(), db.ProjectID)
	if err != nil {
		return nil, mapAnchorError(err)
	}
	if err := svc.s.Engine.BrowseCheckQuota(team); err != nil {
		return nil, apperr.New("E_QUOTA_EXCEEDED",
			"too many concurrent browse sessions for this team (limit %d); wait for a session to expire or go idle", engine.BrowseMaxSessionsPerTeam).
			// browse 语境的处置提示分立（F3.6 挂账收口）：注册表默认文案面向
			// 可删除的资源行；会话是 TTL 回收的非常驻实体，处置是等待或换
			// 只读形态，不是清理条目。
			WithSuggestion("Wait for a session to reach its idle or hard TTL (both reclaim automatically), or close an open browser session before opening another.")
	}
	sessionID := newID()
	now := svc.s.DB.Clock().Now()
	row := &browserepo.Session{
		ID: sessionID, ProjectID: db.ProjectID, DatabaseID: db.ID,
		Engine: db.Engine, ReadOnly: readOnly,
		CreatedAt: state.FormatTime(now), ExpiresAt: state.FormatTime(now.Add(engine.BrowseHardTTL)),
	}
	detail, _ := json.Marshal(map[string]any{ //nolint:errcheck // 结构体字段恒可序列化
		"session_id": sessionID, "browser": browser.Name(),
		"read_only": readOnly, "enforcement": string(browser.ReadOnlyEnforcement()),
	})
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Browse.Create(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventDatabaseBrowserOpened, "database", db.ID, db.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "database.browse",
			Resource: "database/" + db.ID, Detail: string(detail),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "browse session")
	}
	info, err := svc.s.Engine.RegisterBrowseSession(engine.BrowseInput{
		SessionID: sessionID, TeamID: team, ProjectID: db.ProjectID, DatabaseID: db.ID,
		DatabaseName: db.Name, EngineName: db.Engine, ReadOnly: readOnly,
	})
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "browse session could not be registered; the ledger row stays and the loop will reclaim it").WithCause(err)
	}
	ticket, ttl, err := svc.s.eventTickets.issueWithTTL(ticketPurposeBrowse, sessionID, browseTicketTTL)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "browse launcher ticket could not be minted").WithCause(err)
	}
	url := svc.s.Engine.BrowseEntryURL(sessionID) + "?session=" + urlQueryEscape(sessionID) + "&ticket=" + urlQueryEscape(ticket)
	return &structurev1.BrowseDatabaseResponse{
		SessionId: sessionID, Url: url, Ticket: ticket,
		// G115：TTL 是 browseTicketTTL 常量（120s），域内恒小于 int32 上限。
		ExpiresIn: int32(ttl / time.Second), //nolint:gosec
		Browser:   info.Browser, ReadOnly: readOnly,
		Enforcement: browseEnforcementMsg(browser.ReadOnlyEnforcement()),
	}, nil
}

// browseEnforcementMsg 映射执法层级枚举（protojson 规范名单源）。
func browseEnforcementMsg(e dbbrowser.Enforcement) structurev1.BrowseReadOnlyEnforcement {
	switch e {
	case dbbrowser.EnforcementSession:
		return structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_SESSION
	case dbbrowser.EnforcementTool:
		return structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_TOOL
	default:
		return structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_NONE
	}
}

// urlQueryEscape 是 URL query 值转义（票据 base64url 形态实际零转义面，
// 显式转义是纵深）。
func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

// RedeemBrowseTicket 兑换 browse 票据（assembly 原生入口消费；purpose+
// 会话绑定、单用途——ADR-0051 决策 2）。
func (s *Services) RedeemBrowseTicket(sessionID, ticket string) bool {
	return s.eventTickets.redeem(ticketPurposeBrowse, sessionID, ticket)
}

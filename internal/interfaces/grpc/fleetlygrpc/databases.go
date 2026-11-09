package fleetlygrpc

// DatabasesService 实现（F1.12，ADR-0029）：托管数据服务聚合面。凭证
// 单真源 = Project Secret database:<name>（值 = 完整连接 URL，永不在响应
// 出现）；创建受理位四检查（父项目存活/配额/engine 值域/项目有活跃网络
// ——零网项目的库不可达，拒绝优于静默孤岛）；删除收口走 engine 拆载体
// 后 tombstone 一事务（先变更后留痕，ADR-0023 同款序）。

import (
	"context"
	"errors"
	"net/url"
	"time"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// 数据库面事件名（usage 反扫的字面量锚点）。
const (
	eventDatabaseCreated       = "database.created"
	eventDatabaseBrowserOpened = "database.browser_opened"
	eventDatabaseDeleted       = "database.deleted"
	// 轮换事实（IA v3 二期⑤b）：engine 改密 + Secret 重写后由受理位与
	// 审计同事务落账（DeleteDatabase 同款先变更后留痕序）。
	eventDatabaseRotated = "database.credentials_rotated"
	// 期望态变更事实（B2d，ADR-0095 决策 5/9）：Update 双车道执行后由
	// 受理位与审计同事务落账（先变更后留痕；载荷只携键集不携值——旋钮
	// 值经 Database 行回显面可见，事件面保持最小披露）。
	eventDatabaseUpdated = "database.updated"
)

// per-Project 活跃数据库上限（ADR-0017 配额族；库是长驻真实资源，保守缺省
// 16）自 B2e 起随kind descriptor 自述——真源 databasetemplate.Kind.Quota
//（ADR-0096 决策 1），受理门 acceptance.go databaseQuota 直读，本文件不再
// 持第二份常量。

type DatabasesService struct {
	structurev1.UnimplementedDatabasesServiceServer
	s *Services
}

// CreateDatabase：铸造凭证（随机密码 → 完整连接 URL → age 信封落
// Secret）+ 聚合行，一事务落账后 Kick 收敛环。同名活跃行冲突 →
// E_ALREADY_EXISTS；engine 值域外 → E_INVALID_ARGUMENT（列合法值）。
// B2c 受理四门（ADR-0094 决策 3 + ADR-0095 决策 1/7）：version（注册表
// 值域 + 弃用门 + 恢复兼容）、settings（ValidateSettings 单源）、
// resources（正数与上限）、恢复源版本轴（RestoreCompat——同版本放行、
// 只升不降、pgvector 分轴、空源版本放行+注记）。
func (svc *DatabasesService) CreateDatabase(ctx context.Context, req *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error) {
	// name 的 dns 标签形态检查（validName）与 project_id/name/engine 必填
	// 组合检查已退役为 CreateDatabaseRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法，组合必填拆
	// per-field 文案；正则字面单源见 entityid.NamePatternSource（descriptor
	// 对齐测试执法）。engine 值域查模板注册表（动态文案）留在服务内。
	if _, ok := databasetemplate.InfoFor(req.GetEngine()); !ok {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine: %q is not a registered template (available: %v)", req.GetEngine(), databasetemplate.Engines())
	}
	// 版本受理门（ADR-0094 决策 3）：空 = Default；未知版本拒（列合法值）；
	// 弃用门分档——全新建拒（列合法值 + 弃用说明 + 灾备指引），带恢复源
	// 放行（同版本重建的灾备义，决策 6；版本轴随后由 RestoreCompat 裁）。
	spec, err := databasetemplate.ResolveVersion(req.GetEngine(), req.GetVersion())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	legalVersions, _ := databasetemplate.VersionsFor(req.GetEngine())
	if err := databasetemplate.AdmitCreateVersion(req.GetEngine(), spec,
		databasetemplate.VersionIDs(legalVersions), req.GetRestoreFromBackup() != ""); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	// 旋钮受理门（ADR-0095 决策 1）：ValidateSettings 单源——白名单外键/
	// 类型/范围/enum 精确报错直通（绑版实例 Knobs——engine+version 键控）。
	tpl, err := databasetemplate.For(req.GetEngine(), spec.ID)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	if err := dataservicetemplate.ValidateSettings(tpl.Knobs(), req.GetSettings()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
	}
	// 资源受理门（ADR-0095 决策 7）：正整数 + 上限（64 vCPU / 256 GiB——
	// 单库载体占满控制面的形态拒绝优于静默；缺省 nil = 无限制 = 现状）。
	if err := validateDatabaseResources(req.GetResources()); err != nil {
		return nil, err
	}
	if svc.s.Cipher == nil {
		// 未配主密钥是显式关态（P4-3 批 3）：cipher 面缺席精确 503，自
		// E_SECRET_UNAVAILABLE 双语义拆出，文案逐字节保留。
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 库卷异源前置读（ADR-0097 决策 4）：同名重建形态（挂靠卷行已在）下
	// 读运行侧卷的实例标记——在场 = 上一实例的陈旧卷（新凭证对旧数据面
	// 的隐蔽认证失败在此显式化），受理即拒；ensure 链标记门兜底其余路径。
	if err := svc.s.Engine.CheckVolumeOrigin(ctx, dataservicetemplate.KindDatabase, req.GetProjectId(), req.GetName(), req.GetEngine()); err != nil {
		if errors.Is(err, engine.ErrVolumeForeignOrigin) {
			return nil, apperr.New("E_CONFLICT",
				"database name %q reuses a volume that still holds data from a previous database instance", req.GetName()).
				WithSuggestion("The retained runtime-side volume is not reusable (credentials are minted fresh on create). Remove the runtime-side volume if the data is disposable, or recreate the database under a new name (optionally restoring from a backup).").
				WithCause(err)
		}
		return nil, mapStateError(err, "volume")
	}

	// id 由 repo 行装配处铸造（ADR-0070）；连接 URL 内嵌 id，故 URL 铸造
	// 与凭证信封落 Secret 一并进写闭包（行创建之后、同事务）。
	secretName := engine.DBCredentialSecretName(req.GetName())
	row := &database.Database{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		Engine: req.GetEngine(), Version: spec.ID, CredentialsRef: secretName,
		Settings:           req.GetSettings(),
		CPUMillis:          resourcesCPU(req.GetResources()),
		MemoryMB:           resourcesMemory(req.GetResources()),
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
	// 恢复源受理（ADR-0039 + ADR-0094 决策 5）：同 Project + 引擎一致 +
	// 成功有产物 + 版本轴 RestoreCompat（同版本放行、只升不降、pgvector
	// 分轴）；空源版本（旧行「版本未记录」）= 同引擎放行 + 响应注记降级
	// 风险自负（红队 A10）；挂起位与聚合行同事务落（重启安全——备份环
	// 按行重放）。
	var restoreSrc *backup.Backup
	restoreNote := ""
	if req.GetRestoreFromBackup() != "" {
		src, err := svc.s.Backups.Get(ctx, req.GetRestoreFromBackup())
		if err != nil {
			return nil, mapStateError(err, "backup")
		}
		if src.ProjectID != req.GetProjectId() {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s belongs to another project", src.ID)
		}
		if src.Subject.Kind != dataservicetemplate.KindDatabase {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s is a %q kind artifact, not a database backup", src.ID, src.Subject.Kind)
		}
		if src.Engine != req.GetEngine() {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup engine %q does not match the requested engine %q", src.Engine, req.GetEngine())
		}
		if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s is not a succeeded backup with an object", src.ID)
		}
		if src.EngineVersion == "" {
			// 旧行（migration 00039 前）：版本未记录——同引擎放行，受理
			// 文案标注（design §2.4 红队 A10：空值门放行 major 降级会在
			// 运行时才炸、留半删态；诚实披露优于假验证）。
			restoreNote = "restore source backup predates engine version tracking; compatibility is unverified - downgrade risk is yours (red team A10)"
		} else if err := databasetemplate.RestoreCompat(req.GetEngine(), src.EngineVersion, spec.ID); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "restore_from_backup: %s", err)
		}
		restoreSrc = src
	}
	// 行 id 由事务内铸出（ADR-0070）：闭包求值在聚合写后自然可见。事件归属
	// 进事务前解析（R08-M8：解析失败整单拒绝）。
	fact, err := svc.s.factOf(ctx, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventDatabaseCreated, Aggregate: "database", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "database.create", Resource: "database/" + row.ID, AfterFP: row.Engine + " " + row.Name,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.databaseQuota(req.GetProjectId()),
			svc.s.projectHasNetwork(req.GetProjectId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Databases.Create(ctx, row); err != nil {
				return err
			}
			url, err := engine.DatabaseConnectionURL(req.GetEngine(), row.Version, row.ID, mintDatabasePassword())
			if err != nil {
				return err
			}
			sealed, err := svc.s.Cipher.Produce([]byte(url))
			if err != nil {
				return err
			}
			if err := st.Secrets.Upsert(ctx, &secret.Secret{
				ProjectID: req.GetProjectId(), Name: secretName,
				Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
			}); err != nil {
				return err
			}
			if restoreSrc != nil {
				if err := st.Databases.SetRestorePending(ctx, row.ID, restoreSrc.ID); err != nil {
					return err
				}
				// 回显与行一致（响应行镜像落库终态——挂起位是行的一部分）。
				row.RestoreFromBackup = restoreSrc.ID
			}
			return nil
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	svc.s.Engine.KickDatabases()
	if restoreSrc != nil {
		svc.s.Engine.KickBackups()
	}
	return &structurev1.CreateDatabaseResponse{Database: databaseMsg(row), RestoreVersionNote: restoreNote}, nil
}

func (svc *DatabasesService) GetDatabase(ctx context.Context, req *structurev1.GetDatabaseRequest) (*structurev1.GetDatabaseResponse, error) {
	row, err := svc.s.Databases.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &structurev1.GetDatabaseResponse{Database: databaseMsg(row)}, nil
}

// ResolveDatabase 按 (project id|name, database name) 解析（ADR-0092 name
// 边界解析单源；授权随 project 解析内嵌）。
func (svc *DatabasesService) ResolveDatabase(ctx context.Context, req *structurev1.ResolveDatabaseRequest) (*structurev1.ResolveDatabaseResponse, error) {
	p, err := svc.s.resolveProjectRef(ctx, req.GetProject())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Databases.GetByName(ctx, p.ID, req.GetName())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	return &structurev1.ResolveDatabaseResponse{Database: databaseMsg(row)}, nil
}

// ListDatabases 新→旧分页（ADR-0026 after_* + limit）。
func (svc *DatabasesService) ListDatabases(ctx context.Context, req *structurev1.ListDatabasesRequest) (*structurev1.ListDatabasesResponse, error) {
	// 空 project_id 先拒（E_INVALID_ARGUMENT）——与 ListApps 同口径；此前
	// 落到行级授权的 E_NOT_FOUND（404），调用方把"缺参"误判为"项目不存在"
	//（N3 交接 O1 收口，2026-10-06）。（该检查已退役为 ListDatabasesRequest
	// 的 protovalidate CEL 注解——P4-1 批 1 Run B1，拦截器在 handler 之前
	// 执法。）
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Databases.ListByProject(ctx,
		req.GetProjectId(), req.GetAfterDatabaseSeq(), int(req.GetLimit()))
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
	// id 必填检查已退役为 DeleteDatabaseRequest 的 protovalidate CEL 注解
	//（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	row, err := svc.s.Databases.Get(ctx, req.GetId())
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
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventDatabaseDeleted, Aggregate: "database", ID: req.GetId(),
			Payload: mustJSON(structureEventPayload{ID: req.GetId(), ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "database.delete", Resource: "database/" + req.GetId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Databases.SoftDelete(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	svc.s.Engine.KickDatabases()
	return &structurev1.DeleteDatabaseResponse{}, nil
}

// RotateDatabasePassword 凭证轮换受理（IA v3 二期⑤b）：授权 + 在服检查
// 前置 → engine 方言改密 + Secret 重写（轮换真源在 databases 卫星 rotate.go，P5 wave-4 拆包）→
// 审计落账。事件由 engine 发射（database.credentials_rotated，紧贴 Secret
// 重写事实）；级联披露由调用方承担（Console 确认页/CLI 回执明示"引用库
// 的 App 须重新部署取新值"）。新连接串只进 Secret，响应永不回显。
func (svc *DatabasesService) RotateDatabasePassword(ctx context.Context, req *structurev1.RotateDatabasePasswordRequest) (*structurev1.RotateDatabasePasswordResponse, error) {
	// database_id 必填检查已退役为 RotateDatabasePasswordRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法。
	row, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if row.Status != database.StatusRunning && row.Status != database.StatusDegraded {
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
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventDatabaseRotated, Aggregate: "database", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "database.rotate_password", Resource: "database/" + row.ID, AfterFP: row.CredentialsRef,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	if err := svc.s.commit(ctx, writeFact{
		Facts: []func() writes.FactRow{fact},
	}); err != nil {
		return nil, mapStateError(err, "database")
	}
	updated, err := svc.s.Databases.Get(ctx, row.ID)
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
		fact, ferr := s.factOf(ctx, db.ProjectID, func(team string) writes.FactRow {
			return writes.FactRow{
				Name: eventDatabaseDeleted, Aggregate: "database", ID: db.ID,
				Payload: mustJSON(structureEventPayload{ID: db.ID, ProjectID: db.ProjectID}),
				TeamID:  team,
				Action:  "database.delete", Resource: "database/" + db.ID,
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			}
		})
		if ferr != nil {
			return ferr
		}
		err = s.commit(ctx, writeFact{
			Write: func(ctx context.Context, st transact.Stores) error {
				return st.Databases.SoftDelete(ctx, db.ID)
			},
			Facts: []func() writes.FactRow{fact},
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
func (s *Services) listActiveDatabases(ctx context.Context, projectID string) ([]database.Database, error) {
	var out []database.Database
	const page = 200 // 与 repo maxListLimit 对齐（超出值被钳回）
	var after int64
	for {
		rows, err := s.Databases.ListByProject(ctx, projectID, after, page)
		if err != nil {
			return nil, mapStateError(err, "database")
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, nil
		}
		after = rows[len(rows)-1].Seq
	}
}

// TriggerBackup 手动触发（ADR-0039）：铸一行 pending 台账 + 审计，Kick
// 备份环；执行完成事实由事件与 ListBackups 观测（触发不等执行——
// 备份时长无上界承诺，同步面不成立）。B4a 起受理位过能力位门（红队 A9，
// ADR-0096 决策 6）：无数据备份面的引擎拒 E_INVALID_ARGUMENT（database
// 类型全员具备——本门 B6a kafka 类型接入即活；与 schedulePass 的排程过滤
// 同判据源 dataservicetemplate.KindSupportsBackup）。
func (svc *DatabasesService) TriggerBackup(ctx context.Context, req *structurev1.TriggerBackupRequest) (*structurev1.TriggerBackupResponse, error) {
	// database_id 必填检查已退役为 TriggerBackupRequest 的 protovalidate
	// CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	db, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	if !dataservicetemplate.KindSupportsBackup(dataservicetemplate.KindDatabase) {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine %q has no data backup face (the engine catalog lists capabilities.backup=false for this kind)", db.Engine)
	}
	row := &backup.Backup{
		ProjectID: db.ProjectID,
		Subject: backup.Subject{
			Kind: dataservicetemplate.KindDatabase,
			ID:   db.ID,
		},
		Engine: db.Engine, EngineVersion: db.Version, RetentionSecs: db.BackupRetentionSecs,
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Backups.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：触发动词未入事件流（执行完成事实由
			// backup.succeeded/backup.failed 事件承载——ADR-0039"触发不等
			// 执行"；A 化前亦无事件行，行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "database.backup_trigger", Resource: "database/" + db.ID, AfterFP: row.ID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	svc.s.Engine.KickBackups()
	return &structurev1.TriggerBackupResponse{Backup: backupMsg(row)}, nil
}

// ListBackups 新→旧分页（ADR-0026 after_* + limit）。
func (svc *DatabasesService) ListBackups(ctx context.Context, req *structurev1.ListBackupsRequest) (*structurev1.ListBackupsResponse, error) {
	// database_id 必填检查已退役为 ListBackupsRequest 的 protovalidate CEL
	// 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	db, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	list, err := svc.s.Backups.ListBySubject(ctx,
		req.GetDatabaseId(), req.GetAfterBackupSeq(), int(req.GetLimit()))
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
	// backup_id 必填检查已退役为 VerifyBackupRequest 的 protovalidate CEL
	// 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	// 成功行才可验的台账检查（动态文案）留在服务内。
	row, err := svc.s.Backups.Get(ctx, req.GetBackupId())
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	if row.Subject.Kind != dataservicetemplate.KindDatabase {
		return nil, mapStateError(state.ErrNotFound, "backup")
	}
	db, err := svc.s.Databases.Get(ctx, row.Subject.ID)
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

// databaseMsg 行 → proto 映射（B2c 回显换源：version 行值直读——退役
// 「InfoFor(engine).Version 动态取」双源，ADR-0094 决策 3；port 是模板
// 知识不落行，按行版本绑版取。host 是网内 DNS 名——诚实连接面，值/密码
// 永不出现）。
func databaseMsg(row *database.Database) *structurev1.Database {
	msg := &structurev1.Database{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, Name: row.Name,
		Engine: row.Engine, Version: row.Version, CredentialsRef: row.CredentialsRef, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Host:              engine.DatabaseDNSName(row.ID),
		LastBackupAt:      row.LastBackupAt,
		RestoreFromBackup: row.RestoreFromBackup,
		RestoreError:      row.RestoreError,
		Settings:          row.Settings,
	}
	if row.CPUMillis != nil || row.MemoryMB != nil {
		msg.Resources = &structurev1.DatabaseResources{
			CpuMillis: derefInt32(row.CPUMillis), MemoryMb: derefInt32(row.MemoryMB),
		}
	}
	if tpl, err := databasetemplate.For(row.Engine, row.Version); err == nil {
		msg.Port = tpl.Meta().Port
	} else if info, ok := databasetemplate.InfoFor(row.Engine); ok {
		// 防御回落：行版本已离册（移除序的存量行）时按 Default 端口回显
		//——端口不随版本分叉，回显连续性优先。
		msg.Port = info.Port
	}
	return msg
}

// derefInt32 解引用资源指针（nil = 0 = proto 缺省；调用方已保证消息非 nil
// 当且仅当任一资源在场）。
func derefInt32(p *int32) int32 {
	if p == nil {
		return 0
	}
	return *p
}

// resourcesCPU/resourcesMemory 把 proto 资源面折为行指针（字段缺省 0 =
// 未设；nil 消息 = 双缺省）。
func resourcesCPU(r *structurev1.DatabaseResources) *int32 {
	if r == nil || r.GetCpuMillis() == 0 {
		return nil
	}
	v := r.GetCpuMillis()
	return &v
}

func resourcesMemory(r *structurev1.DatabaseResources) *int32 {
	if r == nil || r.GetMemoryMb() == 0 {
		return nil
	}
	v := r.GetMemoryMb()
	return &v
}

// 资源受理上限（ADR-0095 决策 7 / design §5）：自定合理界——64 vCPU /
// 256 GiB。单库载体逼近控制面总量容量的形态在受理位拒绝（明确报错）优于
// 静默接受后节点拒绝/驱逐。
const (
	maxDatabaseCPUMillis = int32(64000)  // 64 vCPU
	maxDatabaseMemoryMB  = int32(262144) // 256 GiB
)

// validateDatabaseResources 是资源受理门（正整数 + 上限；0 = 缺省语义由
// 折叠面承载，负值/超限在此精确拒绝）。
func validateDatabaseResources(r *structurev1.DatabaseResources) error {
	if r == nil {
		return nil
	}
	if v := r.GetCpuMillis(); v < 0 || v > maxDatabaseCPUMillis {
		return apperr.New("E_INVALID_ARGUMENT",
			"resources.cpu_millis: %d is outside the allowed range (0, %d] millis (0 = unlimited)", v, maxDatabaseCPUMillis)
	}
	if v := r.GetMemoryMb(); v < 0 || v > maxDatabaseMemoryMB {
		return apperr.New("E_INVALID_ARGUMENT",
			"resources.memory_mb: %d is outside the allowed range (0, %d] MiB (0 = unlimited)", v, maxDatabaseMemoryMB)
	}
	return nil
}

// backupMsg 台账行 → proto 映射（engine_version 是 RestoreCompat 的源
// 版本锚回显；” = 旧行「版本未记录」，ADR-0094 决策 5）。
func backupMsg(row *backup.Backup) *structurev1.Backup {
	return &structurev1.Backup{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID,
		Kind: row.Subject.Kind, SubjectId: row.Subject.ID, Engine: row.Engine,
		EngineVersion: row.EngineVersion,
		ObjectKey:     row.ObjectKey, Digest: row.Digest, SizeBytes: row.SizeBytes,
		Status: row.Status, Error: row.Error,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, CreatedAt: row.CreatedAt,
	}
}

// mintDatabasePassword 铸随机密码——公式单源已收敛到 databasetemplate.MintPassword
// （创建面与轮换面共用；IA v3 二期⑤b 收编）。
func mintDatabasePassword() string { return databasetemplate.MintPassword() }

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
	// database_id 必填检查已退役为 BrowseDatabaseRequest 的 protovalidate
	// CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	// 配置缺席/浏览器映射/动态提权门（各自动态文案）留在服务内。
	if !svc.s.Engine.BrowseConfigured() {
		return nil, apperr.New("E_BROWSE_DISABLED",
			"the database browse face is not configured on this platform (browse.host_suffix is empty)")
	}
	db, err := svc.s.Databases.Get(ctx, req.GetDatabaseId())
	if err != nil {
		return nil, mapStateError(err, "database")
	}
	if err := svc.s.authorizeProjectID(ctx, db.ProjectID); err != nil {
		return nil, err
	}
	if db.Status != database.StatusRunning && db.Status != database.StatusDegraded {
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
		if id, ok := authn.FromContext(ctx); !ok || !id.HasScope("databases", identity.OpWrite) {
			return nil, apperr.New("E_FORBIDDEN",
				"this browse form requires the databases:write scope (read_write, or engines with no read-only enforcement)").
				WithSuggestion("Open the session read-only (the default), or use a token carrying databases:write.")
		}
	}
	team, err := svc.s.Anchor.TeamOfProjectID(ctx, db.ProjectID)
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
	// 会话 id 由 repo 行装配处铸造（ADR-0070）；detail/注册/票据均用事务
	// 内铸出的 row.ID。
	now := svc.s.DB.Clock().Now()
	row := &browse.Session{
		ProjectID: db.ProjectID, DatabaseID: db.ID,
		Engine: db.Engine, ReadOnly: readOnly,
		CreatedAt: state.FormatTime(now), ExpiresAt: state.FormatTime(now.Add(engine.BrowseHardTTL)),
	}
	fact, err := svc.s.factOf(ctx, db.ProjectID, func(team string) writes.FactRow {
		detail := mustJSON(map[string]any{
			"session_id": row.ID, "browser": browser.Name(),
			"read_only": readOnly, "enforcement": string(browser.ReadOnlyEnforcement()),
		})
		return writes.FactRow{
			Name: eventDatabaseBrowserOpened, Aggregate: "database", ID: db.ID,
			Payload: mustJSON(structureEventPayload{ID: db.ID, ProjectID: db.ProjectID}),
			TeamID:  team,
			Action:  "database.browse", Resource: "database/" + db.ID, Detail: string(detail),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Browse.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "browse session")
	}
	sessionID := row.ID
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

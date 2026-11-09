package fleetlygrpc

// PubSubsService 实现（B4b，ADR-0096 决策 1/4/8）：PubSub 类型托管服务聚合
// 面——DatabasesService 的同构复制（kind 词差异；通用 handler core 以
// 同形代码承载，descriptor 参数化〔配额/能力位〕）。凭证单真源 = Project
// Secret pubsub:<name>（值 = 连接材料方言串，永不回显）；创建受理四检查
// （父项目存活/配额 8/engine 值域/项目有活跃网络）；删除收口走 engine 拆
// 载体后 tombstone 一事务。Browse 面不设（proto 无面，ADR-0051 既有拒）。

import (
	"context"
	"errors"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// pubsub 面事件名（usage 反扫的字面量锚点；ADR-0096 决策 8 四件）。
const (
	eventPubSubCreated = "pubsub.created"
	eventPubSubUpdated = "pubsub.updated"
	eventPubSubDeleted = "pubsub.deleted"
	eventPubSubRotated = "pubsub.credentials_rotated"
)

type PubSubsService struct {
	structurev1.UnimplementedPubSubsServiceServer
	s *Services
}

// CreatePubSub：铸造凭证（随机密码 → 连接材料方言串 → age 信封落 Secret）
// + 聚合行，一事务落账后 Kick 收敛环。受理四门（version/settings/
// resources/恢复源版本轴）与 CreateDatabase 同律。
func (svc *PubSubsService) CreatePubSub(ctx context.Context, req *structurev1.CreatePubSubRequest) (*structurev1.CreatePubSubResponse, error) {
	if _, ok := pubsubtemplate.InfoFor(req.GetEngine()); !ok {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine: %q is not a registered pubsub template (available: %v)", req.GetEngine(), pubsubtemplate.Engines())
	}
	spec, err := pubsubtemplate.ResolveVersion(req.GetEngine(), req.GetVersion())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	legalVersions, _ := pubsubtemplate.VersionsFor(req.GetEngine())
	if err := pubsubtemplate.AdmitCreateVersion(req.GetEngine(), spec,
		pubsubtemplate.VersionIDs(legalVersions), req.GetRestoreFromBackup() != ""); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	tpl, err := pubsubtemplate.For(req.GetEngine(), spec.ID)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	if err := dataservicetemplate.ValidateSettings(tpl.Knobs(), req.GetSettings()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
	}
	if err := validateDatabaseResources(req.GetResources()); err != nil {
		return nil, err
	}
	// 跨面联动受理闸（B6a，红队 F-13：kafka heap_mb ≤ memory×0.7——
	// ValidateSettings 只有 settings 面，资源面在此复核；无联动面的引擎
	// 不实现该可选面）。
	if lv, ok := tpl.(pubsubtemplate.LinkageValidator); ok {
		memory := int64(0)
		if m := resourcesMemory(req.GetResources()); m != nil {
			memory = int64(*m)
		}
		if err := lv.ValidateLinkage(memory, req.GetSettings()); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
		}
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 卷异源前置读（B4c，ADR-0097 决策 4 实体中性化）：同名重建形态（挂靠
	// 卷行已在）下读运行侧卷的实例标记——在场（含跨类型：旧数据库卷重建
	// pubsub 同样是异源事实）= 受理即拒；ensure 链标记门兜底其余路径。
	if err := svc.s.Data.CheckVolumeOrigin(ctx, dataservicetemplate.KindPubSub, req.GetProjectId(), req.GetName(), req.GetEngine()); err != nil {
		if errors.Is(err, engine.ErrVolumeForeignOrigin) {
			return nil, apperr.New("E_CONFLICT",
				"pubsub name %q reuses a volume that still holds data from a previous instance (of this or another kind)", req.GetName()).
				WithSuggestion("The retained runtime-side volume is not reusable (credentials are minted fresh on create). Remove the runtime-side volume if the data is disposable, or recreate the pubsub under a new name (optionally restoring from a backup).").
				WithCause(err)
		}
		return nil, mapStateError(err, "volume")
	}
	secretName := contract.PubCredentialSecretName(req.GetName())
	row := &pubsub.PubSub{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		Engine: req.GetEngine(), Version: spec.ID, CredentialsRef: secretName,
		Settings:           req.GetSettings(),
		CPUMillis:          resourcesCPU(req.GetResources()),
		MemoryMB:           resourcesMemory(req.GetResources()),
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
	var restoreSrc *backup.Backup
	restoreNote := ""
	if req.GetRestoreFromBackup() != "" {
		// 引擎级能力位门（B6a）：无备份面的引擎无恢复源可受（kafka——
		// 连锁拒绝先于台账查找，错误面与 TriggerBackup 同判据源）。
		if !tpl.DataCapabilities().Backup {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: engine %q carries no data backup face, so there are no backup artifacts to restore for this engine", req.GetEngine())
		}
		src, err := svc.s.Backups.Get(ctx, req.GetRestoreFromBackup())
		if err != nil {
			return nil, mapStateError(err, "backup")
		}
		if src.ProjectID != req.GetProjectId() {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s belongs to another project", src.ID)
		}
		if src.Subject.Kind != dataservicetemplate.KindPubSub {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"restore_from_backup: backup %s is a %q kind artifact, not a pubsub backup", src.ID, src.Subject.Kind)
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
			restoreNote = "restore source backup predates engine version tracking; compatibility is unverified - downgrade risk is yours (red team A10)"
		} else if err := pubsubtemplate.RestoreCompat(req.GetEngine(), src.EngineVersion, spec.ID); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "restore_from_backup: %s", err)
		}
		restoreSrc = src
	}
	fact, err := svc.s.factOf(ctx, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventPubSubCreated, Aggregate: "pubsub", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "pubsub.create", Resource: "pubsub/" + row.ID, AfterFP: row.Engine + " " + row.Name,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.pubsubQuota(req.GetProjectId()),
			svc.s.projectHasNetwork(req.GetProjectId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.PubSubs.Create(ctx, row); err != nil {
				return err
			}
			material, err := engine.PubSubConnMaterial(req.GetEngine(), row.Version, row.ID, pubsubtemplate.MintPassword())
			if err != nil {
				return err
			}
			sealed, err := svc.s.Cipher.Produce([]byte(material))
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
				if err := st.PubSubs.SetRestorePending(ctx, row.ID, restoreSrc.ID); err != nil {
					return err
				}
				row.RestoreFromBackup = restoreSrc.ID
				row.RestoreState = pubsub.RestoreStatePending
			}
			return nil
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	svc.s.Data.KickPubSubs()
	if restoreSrc != nil {
		svc.s.Backup.KickBackups()
	}
	return &structurev1.CreatePubSubResponse{Pubsub: pubsubMsg(row), RestoreVersionNote: restoreNote}, nil
}

func (svc *PubSubsService) GetPubSub(ctx context.Context, req *structurev1.GetPubSubRequest) (*structurev1.GetPubSubResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &structurev1.GetPubSubResponse{Pubsub: pubsubMsg(row)}, nil
}

// ResolvePubSub 按 (project id|name, pubsub name) 解析（ADR-0092）。
func (svc *PubSubsService) ResolvePubSub(ctx context.Context, req *structurev1.ResolvePubSubRequest) (*structurev1.ResolvePubSubResponse, error) {
	p, err := svc.s.resolveProjectRef(ctx, req.GetProject())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.PubSubs.GetByName(ctx, p.ID, req.GetName())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	return &structurev1.ResolvePubSubResponse{Pubsub: pubsubMsg(row)}, nil
}

// ListPubSubs 新→旧分页。
func (svc *PubSubsService) ListPubSubs(ctx context.Context, req *structurev1.ListPubSubsRequest) (*structurev1.ListPubSubsResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.PubSubs.ListByProject(ctx, req.GetProjectId(), req.GetAfterPubsubSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	out := &structurev1.ListPubSubsResponse{}
	for i := range list {
		out.Pubsubs = append(out.Pubsubs, pubsubMsg(&list[i]))
	}
	return out, nil
}

// DeletePubSub 收口删除：engine 拆载体（幂等）→ tombstone + 事件 + 审计
// 一事务。数据卷与凭证 Secret 不随删。
func (svc *PubSubsService) DeletePubSub(ctx context.Context, req *structurev1.DeletePubSubRequest) (*structurev1.DeletePubSubResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.s.Data.TeardownPubSub(ctx, req.GetId()); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "pubsub %s not found", req.GetId())
		}
		return nil, apperr.New("E_INTERNAL", "pubsub teardown failed").WithCause(err)
	}
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventPubSubDeleted, Aggregate: "pubsub", ID: req.GetId(),
			Payload: mustJSON(structureEventPayload{ID: req.GetId(), ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "pubsub.delete", Resource: "pubsub/" + req.GetId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.PubSubs.SoftDelete(ctx, req.GetId()); err != nil {
				return err
			}
			// 子资源行级联清理（B8：父载体拆除即物理面消亡——Topic/Stream 行
			// 硬删；不落子资源事件，事实面由 pubsub.deleted 承载）。
			if err := st.Topics.DeleteByPubSub(ctx, req.GetId()); err != nil {
				return err
			}
			return st.Streams.DeleteByPubSub(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	svc.s.Data.KickPubSubs()
	return &structurev1.DeletePubSubResponse{}, nil
}

// RotatePubSubPassword 凭证轮换受理：授权 + 在服检查 → engine 声明式轮换
// （Secret 重写 + 收敛环重下发）→ 事件 + 审计落账。级联披露由调用方承担。
func (svc *PubSubsService) RotatePubSubPassword(ctx context.Context, req *structurev1.RotatePubSubPasswordRequest) (*structurev1.RotatePubSubPasswordResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if row.Status != pubsub.StatusRunning && row.Status != pubsub.StatusDegraded {
		return nil, apperr.New("E_DATABASE_NOT_READY",
			"pubsub %s is %s; rotation requires a running pubsub (the credential change needs a live target)", row.Name, row.Status)
	}
	if err := svc.s.Data.RotatePubSubPassword(ctx, row.ID); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "pubsub %s not found", req.GetPubsubId())
		}
		if errors.Is(err, engine.ErrRotateRejected) {
			return nil, apperr.New("E_DATABASE_ROTATE_FAILED", "%s", err.Error())
		}
		return nil, apperr.New("E_INTERNAL", "credential rotation failed").WithCause(err)
	}
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventPubSubRotated, Aggregate: "pubsub", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "pubsub.rotate_password", Resource: "pubsub/" + row.ID, AfterFP: row.CredentialsRef,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	if err := svc.s.commit(ctx, writeFact{Facts: []func() writes.FactRow{fact}}); err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	updated, err := svc.s.PubSubs.Get(ctx, row.ID)
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	return &structurev1.RotatePubSubPasswordResponse{Pubsub: pubsubMsg(updated)}, nil
}

// UpdatePubSub / PreviewUpdatePubSub 受理面在 pubsub_update.go（B4c 确认面
// 三种数据服务类型对齐——UpdateDatabase/PreviewUpdateDatabase 同构）。

// TriggerPubSubBackup 手动触发（能力位门控——B6a 起按**引擎级**
// Template.DataCapabilities 判据，类型门缺省的行级下沉）：铸一行 pending
// 台账 + 审计，Kick 备份环。台账主体中性：kind=pubsub、subject_id=实例
// 行 id（backups 表跨类型共用）。无备份面引擎（kafka——挂账：无官方转储
// 工具）E_INVALID_ARGUMENT 拒。
func (svc *PubSubsService) TriggerPubSubBackup(ctx context.Context, req *structurev1.TriggerPubSubBackupRequest) (*structurev1.TriggerPubSubBackupResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "engine %q left the template registry: %s", row.Engine, err)
	}
	if !tpl.DataCapabilities().Backup {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine %q carries no data backup face (the engine catalog lists capabilities.backup=false; deferred until official dump tooling or multi-replica data recovery lands — rebuild the data plane from stream re-delivery or carry it with external tooling)", row.Engine)
	}
	b := &backup.Backup{
		ProjectID: row.ProjectID,
		Subject: backup.Subject{
			Kind: dataservicetemplate.KindPubSub,
			ID:   row.ID,
		},
		Engine: row.Engine, EngineVersion: row.Version, RetentionSecs: row.BackupRetentionSecs,
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Backups.Create(ctx, b)
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：触发动词未入事件流（执行完成事实由
			// backup.succeeded/backup.failed 事件承载——触发不等执行，
			// TriggerBackup 同款先例）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "pubsub.backup_trigger", Resource: "pubsub/" + row.ID, AfterFP: b.ID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	svc.s.Backup.KickBackups()
	return &structurev1.TriggerPubSubBackupResponse{Backup: backupMsg(b)}, nil
}

// ListPubSubBackups 新→旧分页。
func (svc *PubSubsService) ListPubSubBackups(ctx context.Context, req *structurev1.ListPubSubBackupsRequest) (*structurev1.ListPubSubBackupsResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	list, err := svc.s.Backups.ListBySubject(ctx, row.ID, req.GetAfterBackupSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "backup")
	}
	out := &structurev1.ListPubSubBackupsResponse{}
	for i := range list {
		out.Backups = append(out.Backups, backupMsg(&list[i]))
	}
	return out, nil
}

// VerifyPubSubBackup manifest 驱动逐成员重算（归属校验：台账行须属本实例）。
func (svc *PubSubsService) VerifyPubSubBackup(ctx context.Context, req *structurev1.VerifyPubSubBackupRequest) (*structurev1.VerifyPubSubBackupResponse, error) {
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	b, err := svc.s.Backups.Get(ctx, req.GetBackupId())
	if err != nil || b.Subject.Kind != dataservicetemplate.KindPubSub || b.Subject.ID != row.ID {
		return nil, mapStateError(state.ErrNotFound, "backup")
	}
	if b.Status != backup.StatusSucceeded || b.ObjectKey == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"backup %s is not a succeeded backup with an object (status %s)", b.ID, b.Status)
	}
	ok, detail, err := svc.s.Backup.VerifyBackup(ctx, req.GetBackupId())
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "backup verification failed").WithCause(err)
	}
	return &structurev1.VerifyPubSubBackupResponse{Ok: ok, Digest: b.Digest, Error: detail}, nil
}

// DownloadPubSubBackup 流式下载（成员序拼接；DownloadBackup 同形）。
func (svc *PubSubsService) DownloadPubSubBackup(req *structurev1.DownloadPubSubBackupRequest, stream structurev1.PubSubsService_DownloadPubSubBackupServer) error {
	ctx := stream.Context()
	row, err := svc.s.PubSubs.Get(ctx, req.GetPubsubId())
	if err != nil {
		return mapStateError(err, "pubsub")
	}
	proj, err := svc.s.Projects.Get(ctx, row.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if err := svc.s.authorizeTeamForProject(ctx, proj); err != nil {
		return err
	}
	_, reader, err := svc.s.Backup.DownloadBackup(ctx, row.ID, req.GetBackupId())
	if err != nil {
		return mapStateError(err, "backup")
	}
	defer func() { _ = reader.Close() }()
	chunk := make([]byte, 256*1024)
	for {
		n, rerr := reader.Read(chunk)
		if n > 0 {
			if serr := stream.Send(&structurev1.DownloadPubSubBackupResponse{Data: append([]byte(nil), chunk[:n]...)}); serr != nil {
				return serr
			}
		}
		if rerr != nil {
			break
		}
	}
	return nil
}

// cascadeDeletePubSubs 级联收口项目下全部活跃 PubSub（DeleteProject 受理
// 消费；cascadeDeleteDatabases 同款序与失败语义）。
func (s *Services) cascadeDeletePubSubs(ctx context.Context, projectID string) error {
	rows, err := s.listActivePubSubs(ctx, projectID)
	if err != nil {
		return err
	}
	for i := range rows {
		ps := &rows[i]
		if err := s.Data.TeardownPubSub(ctx, ps.ID); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue
			}
			return apperr.New("E_INTERNAL",
				"project delete: pubsub %s (%s) teardown failed; pubsubs torn down so far stay deleted - retry the project delete to converge",
				ps.ID, ps.Name).WithCause(err)
		}
		fact, ferr := s.factOf(ctx, ps.ProjectID, func(team string) writes.FactRow {
			return writes.FactRow{
				Name: eventPubSubDeleted, Aggregate: "pubsub", ID: ps.ID,
				Payload: mustJSON(structureEventPayload{ID: ps.ID, ProjectID: ps.ProjectID}),
				TeamID:  team,
				Action:  "pubsub.delete", Resource: "pubsub/" + ps.ID,
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			}
		})
		if ferr != nil {
			return ferr
		}
		err = s.commit(ctx, writeFact{
			Write: func(ctx context.Context, st transact.Stores) error {
				if err := st.PubSubs.SoftDelete(ctx, ps.ID); err != nil {
					return err
				}
				// 子资源行级联清理（同 DeletePubSub——物理面随父载体消亡）。
				if err := st.Topics.DeleteByPubSub(ctx, ps.ID); err != nil {
					return err
				}
				return st.Streams.DeleteByPubSub(ctx, ps.ID)
			},
			Facts: []func() writes.FactRow{fact},
		})
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue
			}
			return apperr.New("E_INTERNAL",
				"project delete: pubsub %s (%s) could not be tombstoned", ps.ID, ps.Name).WithCause(err)
		}
	}
	if len(rows) > 0 {
		s.Data.KickPubSubs()
	}
	return nil
}

// listActivePubSubs 枚举项目下全部活跃 PubSub（级联枚举面；分页循环）。
func (s *Services) listActivePubSubs(ctx context.Context, projectID string) ([]pubsub.PubSub, error) {
	var out []pubsub.PubSub
	const page = 200
	var after int64
	for {
		rows, err := s.PubSubs.ListByProject(ctx, projectID, after, page)
		if err != nil {
			return nil, mapStateError(err, "pubsub")
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, nil
		}
		after = rows[len(rows)-1].Seq
	}
}

// pubsubMsg 行 → proto 映射（host = pub-<id> 网内 DNS 名；port 按行版本绑
// 版取；值/密码永不出现）。
func pubsubMsg(row *pubsub.PubSub) *structurev1.PubSub {
	msg := &structurev1.PubSub{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, Name: row.Name,
		Engine: row.Engine, Version: row.Version, CredentialsRef: row.CredentialsRef, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Host:              contract.PubSubDNSName(row.ID),
		LastBackupAt:      row.LastBackupAt,
		RestoreFromBackup: row.RestoreFromBackup,
		RestoreState:      row.RestoreState,
		RestoreError:      row.RestoreError,
		Settings:          row.Settings,
	}
	if row.CPUMillis != nil || row.MemoryMB != nil {
		msg.Resources = &structurev1.DatabaseResources{
			CpuMillis: derefInt32(row.CPUMillis), MemoryMb: derefInt32(row.MemoryMB),
		}
	}
	if tpl, err := pubsubtemplate.For(row.Engine, row.Version); err == nil {
		msg.Port = tpl.Meta().Port
	} else if info, ok := pubsubtemplate.InfoFor(row.Engine); ok {
		msg.Port = info.Port
	}
	return msg
}

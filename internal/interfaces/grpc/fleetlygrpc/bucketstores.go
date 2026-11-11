package fleetlygrpc

// BucketStoresService 实现（B5，ADR-0096 决策 1/4/8）：Bucket Store 类型托管
// 服务聚合面——PubSubsService 的同构复制（kind 词差异；通用 handler core
// 以同形代码承载，descriptor 参数化〔配额/能力位〕）。凭证单真源 = Project
// Secret bucketstore:<name>（值 = endpoint/access_key/secret_key k=v 方言串，
// 永不回显）；创建受理四检查（父项目存活/配额 4/engine 值域/项目有活跃
// 网络）；删除收口走 engine 拆载体后 tombstone 一事务。**无备份动词族**
// （silo 转储挂账——DataCapabilities.Backup=false，能力位裁剪：proto 不设面
// 而非拒绝文案占位）。Browse 面不设（ADR-0051 既有拒）。

import (
	"context"
	"errors"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
)

// bucket store 面事件名（usage 反扫的字面量锚点；ADR-0096 决策 8 四件——
// EventPrefix bucket_store 与类型词 bucketstore 不同形）。
const (
	eventBucketStoreCreated = "bucket_store.created"
	eventBucketStoreUpdated = "bucket_store.updated"
	eventBucketStoreDeleted = "bucket_store.deleted"
	eventBucketStoreRotated = "bucket_store.credentials_rotated"
)

type BucketStoresService struct {
	structurev1.UnimplementedBucketStoresServiceServer
	s *Services
}

// CreateBucketStore：铸造凭证（随机 secret key → k=v 连接材料方言串 → age
// 信封落 Secret）+ 聚合行，一事务落账后 Kick 收敛环。受理三门（version/
// settings/resources）与 CreatePubSub 同律；无恢复源面（无备份族）。
func (svc *BucketStoresService) CreateBucketStore(ctx context.Context, req *structurev1.CreateBucketStoreRequest) (*structurev1.CreateBucketStoreResponse, error) {
	if _, ok := bucketstoretemplate.InfoFor(req.GetEngine()); !ok {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine: %q is not a registered bucket store template (available: %v)", req.GetEngine(), bucketstoretemplate.Engines())
	}
	spec, err := bucketstoretemplate.ResolveVersion(req.GetEngine(), req.GetVersion())
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	legalVersions, _ := bucketstoretemplate.VersionsFor(req.GetEngine())
	if err := bucketstoretemplate.AdmitCreateVersion(req.GetEngine(), spec, bucketstoretemplate.VersionIDs(legalVersions)); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	tpl, err := bucketstoretemplate.For(req.GetEngine(), spec.ID)
	if err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "version: %s", err)
	}
	if err := dataservicetemplate.ValidateSettings(tpl.Knobs(), req.GetSettings()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "settings: %s", err)
	}
	if err := validateDatabaseResources(req.GetResources()); err != nil {
		return nil, err
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_MASTER_KEY_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	// 卷异源前置读（ADR-0097 决策 4 实体中性化）：同名重建形态（挂靠卷行
	// 已在）下读运行侧卷的实例标记——在场（含跨类型）= 受理即拒；ensure 链
	// 标记门兜底其余路径。
	if err := svc.s.Data.CheckVolumeOrigin(ctx, dataservicetemplate.KindBucketStore, req.GetProjectId(), req.GetName(), req.GetEngine()); err != nil {
		if errors.Is(err, api.ErrVolumeForeignOrigin) {
			return nil, apperr.New("E_CONFLICT",
				"bucket store name %q reuses a volume that still holds data from a previous instance (of this or another kind)", req.GetName()).
				WithSuggestion("The retained runtime-side volume is not reusable (credentials are minted fresh on create). Remove the runtime-side volume if the data is disposable, or recreate the bucket store under a new name.").
				WithCause(err)
		}
		return nil, mapStateError(err, "volume")
	}
	secretName := api.BKTCredentialSecretName(req.GetName())
	row := &bucketstore.BucketStore{
		ProjectID: req.GetProjectId(), Name: req.GetName(),
		Engine: req.GetEngine(), Version: spec.ID, CredentialsRef: secretName,
		Settings:           req.GetSettings(),
		CPUMillis:          resourcesCPU(req.GetResources()),
		MemoryMB:           resourcesMemory(req.GetResources()),
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}
	fact, err := svc.s.factOf(ctx, req.GetProjectId(), func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketStoreCreated, Aggregate: "bucket_store", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "bucket_store.create", Resource: "bucket_store/" + row.ID, AfterFP: row.Engine + " " + row.Name,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{
			svc.s.parentProjectAlive(req.GetProjectId()),
			svc.s.bucketStoreQuota(req.GetProjectId()),
			svc.s.projectHasNetwork(req.GetProjectId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.BucketStores.Create(ctx, row); err != nil {
				return err
			}
			material, err := api.BucketStoreConnMaterial(req.GetEngine(), row.Version, row.ID, bucketstoretemplate.MintPassword())
			if err != nil {
				return err
			}
			sealed, err := svc.s.Cipher.Produce([]byte(material))
			if err != nil {
				return err
			}
			return st.Secrets.Upsert(ctx, &secret.Secret{
				ProjectID: req.GetProjectId(), Name: secretName,
				Ciphertext: sealed.Ciphertext, Fingerprint: sealed.Fingerprint,
			})
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	svc.s.Data.KickBucketStores()
	return &structurev1.CreateBucketStoreResponse{BucketStore: bucketStoreMsg(row)}, nil
}

func (svc *BucketStoresService) GetBucketStore(ctx context.Context, req *structurev1.GetBucketStoreRequest) (*structurev1.GetBucketStoreResponse, error) {
	row, err := svc.s.BucketStores.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	return &structurev1.GetBucketStoreResponse{BucketStore: bucketStoreMsg(row)}, nil
}

// ResolveBucketStore 按 (project id|name, bucket store name) 解析（ADR-0092）。
func (svc *BucketStoresService) ResolveBucketStore(ctx context.Context, req *structurev1.ResolveBucketStoreRequest) (*structurev1.ResolveBucketStoreResponse, error) {
	p, err := svc.s.resolveProjectRef(ctx, req.GetProject())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.BucketStores.GetByName(ctx, p.ID, req.GetName())
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	return &structurev1.ResolveBucketStoreResponse{BucketStore: bucketStoreMsg(row)}, nil
}

// ListBucketStores 新→旧分页。
func (svc *BucketStoresService) ListBucketStores(ctx context.Context, req *structurev1.ListBucketStoresRequest) (*structurev1.ListBucketStoresResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.BucketStores.ListByProject(ctx, req.GetProjectId(), req.GetAfterBucketStoreSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	out := &structurev1.ListBucketStoresResponse{}
	for i := range list {
		out.BucketStores = append(out.BucketStores, bucketStoreMsg(&list[i]))
	}
	return out, nil
}

// DeleteBucketStore 收口删除：engine 拆载体（幂等）→ tombstone + 事件 + 审计
// 一事务。数据卷与凭证 Secret 不随删。
func (svc *BucketStoresService) DeleteBucketStore(ctx context.Context, req *structurev1.DeleteBucketStoreRequest) (*structurev1.DeleteBucketStoreResponse, error) {
	row, err := svc.s.BucketStores.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if err := svc.s.Data.TeardownBucketStore(ctx, req.GetId()); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "bucket store %s not found", req.GetId())
		}
		return nil, apperr.New("E_INTERNAL", "bucket store teardown failed").WithCause(err)
	}
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketStoreDeleted, Aggregate: "bucket_store", ID: req.GetId(),
			Payload: mustJSON(structureEventPayload{ID: req.GetId(), ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "bucket_store.delete", Resource: "bucket_store/" + req.GetId(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.BucketStores.SoftDelete(ctx, req.GetId()); err != nil {
				return err
			}
			// 子资源行级联清理（B8：父载体拆除即物理面消亡——Bucket 行硬删；
			// 不落子资源事件，事实面由 bucket_store.deleted 承载）。
			return st.Buckets.DeleteByBucketStore(ctx, req.GetId())
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	svc.s.Data.KickBucketStores()
	return &structurev1.DeleteBucketStoreResponse{}, nil
}

// RotateBucketStorePassword 凭证轮换受理：授权 + 在服检查 → engine 声明式
// 轮换（Secret 重写 + 收敛环以新 SK 重投影滚动）→ 事件 + 审计落账。级联
// 披露由调用方承担。
func (svc *BucketStoresService) RotateBucketStorePassword(ctx context.Context, req *structurev1.RotateBucketStorePasswordRequest) (*structurev1.RotateBucketStorePasswordResponse, error) {
	row, err := svc.s.BucketStores.Get(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, err
	}
	if row.Status != bucketstore.StatusRunning && row.Status != bucketstore.StatusDegraded {
		return nil, apperr.New("E_DATABASE_NOT_READY",
			"bucket store %s is %s; rotation requires a running bucket store (the credential change needs a live target)", row.Name, row.Status)
	}
	if err := svc.s.Data.RotateBucketStorePassword(ctx, row.ID); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "bucket store %s not found", req.GetBucketStoreId())
		}
		if errors.Is(err, api.ErrRotateRejected) {
			return nil, apperr.New("E_DATABASE_ROTATE_FAILED", "%s", err.Error())
		}
		return nil, apperr.New("E_INTERNAL", "credential rotation failed").WithCause(err)
	}
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketStoreRotated, Aggregate: "bucket_store", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "bucket_store.rotate_password", Resource: "bucket_store/" + row.ID, AfterFP: row.CredentialsRef,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	if err := svc.s.commit(ctx, writeFact{Facts: []func() writes.FactRow{fact}}); err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	updated, err := svc.s.BucketStores.Get(ctx, row.ID)
	if err != nil {
		return nil, mapStateError(err, "bucket_store")
	}
	return &structurev1.RotateBucketStorePasswordResponse{BucketStore: bucketStoreMsg(updated)}, nil
}

// UpdateBucketStore / PreviewUpdateBucketStore 受理面在 bucketstore_update.go
// （三种数据服务类型确认面同构——UpdatePubSub/PreviewUpdatePubSub 同构）。

// cascadeDeleteBucketStores 级联收口项目下全部活跃 Bucket Store（DeleteProject
// 受理消费；cascadeDeletePubSubs 同款序与失败语义）。
func (s *Services) cascadeDeleteBucketStores(ctx context.Context, projectID string) error {
	rows, err := s.listActiveBucketStores(ctx, projectID)
	if err != nil {
		return err
	}
	for i := range rows {
		bs := &rows[i]
		if err := s.Data.TeardownBucketStore(ctx, bs.ID); err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue
			}
			return apperr.New("E_INTERNAL",
				"project delete: bucket store %s (%s) teardown failed; bucket stores torn down so far stay deleted - retry the project delete to converge",
				bs.ID, bs.Name).WithCause(err)
		}
		fact, ferr := s.factOf(ctx, bs.ProjectID, func(team string) writes.FactRow {
			return writes.FactRow{
				Name: eventBucketStoreDeleted, Aggregate: "bucket_store", ID: bs.ID,
				Payload: mustJSON(structureEventPayload{ID: bs.ID, ProjectID: bs.ProjectID}),
				TeamID:  team,
				Action:  "bucket_store.delete", Resource: "bucket_store/" + bs.ID,
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			}
		})
		if ferr != nil {
			return ferr
		}
		err = s.commit(ctx, writeFact{
			Write: func(ctx context.Context, st transact.Stores) error {
				if err := st.BucketStores.SoftDelete(ctx, bs.ID); err != nil {
					return err
				}
				// 子资源行级联清理（同 DeleteBucketStore）。
				return st.Buckets.DeleteByBucketStore(ctx, bs.ID)
			},
			Facts: []func() writes.FactRow{fact},
		})
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				continue
			}
			return apperr.New("E_INTERNAL",
				"project delete: bucket store %s (%s) could not be tombstoned", bs.ID, bs.Name).WithCause(err)
		}
	}
	if len(rows) > 0 {
		s.Data.KickBucketStores()
	}
	return nil
}

// listActiveBucketStores 枚举项目下全部活跃 Bucket Store（级联枚举面；分页
// 循环）。
func (s *Services) listActiveBucketStores(ctx context.Context, projectID string) ([]bucketstore.BucketStore, error) {
	var out []bucketstore.BucketStore
	const page = 200
	var after int64
	for {
		rows, err := s.BucketStores.ListByProject(ctx, projectID, after, page)
		if err != nil {
			return nil, mapStateError(err, "bucket_store")
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, nil
		}
		after = rows[len(rows)-1].Seq
	}
}

// bucketStoreMsg 行 → proto 映射（host = bkt-<id> 网内 DNS 名；port 按行
// 版本绑版取；值/凭证永不出现）。
func bucketStoreMsg(row *bucketstore.BucketStore) *structurev1.BucketStore {
	msg := &structurev1.BucketStore{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, Name: row.Name,
		Engine: row.Engine, Version: row.Version, CredentialsRef: row.CredentialsRef, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Host:     api.BucketStoreDNSName(row.ID),
		Settings: row.Settings,
	}
	if row.CPUMillis != nil || row.MemoryMB != nil {
		msg.Resources = &structurev1.DatabaseResources{
			CpuMillis: derefInt32(row.CPUMillis), MemoryMb: derefInt32(row.MemoryMB),
		}
	}
	if tpl, err := bucketstoretemplate.For(row.Engine, row.Version); err == nil {
		msg.Port = tpl.Meta().Port
	} else if info, ok := bucketstoretemplate.InfoFor(row.Engine); ok {
		msg.Port = info.Port
	}
	return msg
}

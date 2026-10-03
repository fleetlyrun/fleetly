package fleetlygrpc

// DatabasesService 实现（F1.12，ADR-0029）：托管数据服务聚合面。凭证
// 单真源 = Project Secret database:<name>（值 = 完整连接 URL，永不在响应
// 出现）；创建受理位四检查（父项目存活/配额/engine 值域/项目有活跃网络
// ——零网项目的库不可达，拒绝优于静默孤岛）；删除收口走 engine 拆载体
// 后 tombstone 一事务（先变更后留痕，ADR-0023 同款序）。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
)

// 数据库面事件名（usage 反扫的字面量锚点）。
const (
	eventDatabaseCreated = "database.created"
	eventDatabaseDeleted = "database.deleted"
)

// dbPasswordRandBytes 是随机密码字节数（hex 后 48 字符；zot 平台凭证
// 同款强度）。
const dbPasswordRandBytes = 24

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
	if _, ok := engine.DatabaseEngineInfoFor(req.GetEngine()); !ok {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"engine: %q is not a registered template (available: %v)", req.GetEngine(), engine.DBEngines())
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
	ciphertext, err := svc.s.Cipher.Seal([]byte(url))
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "database credential could not be sealed").WithCause(err)
	}
	secretName := engine.DBCredentialSecretName(req.GetName())
	secretRow := &secret.Secret{
		ID: newID(), ProjectID: req.GetProjectId(), Name: secretName,
		Ciphertext: ciphertext, Fingerprint: material.Fingerprint([]byte(url)),
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
			return svc.s.Databases.Create(ctx, tx, row)
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

// databaseMsg 行 → proto 映射（version/port 来自模板注册表单源；host 是
// 网内 DNS 名——诚实连接面，值/密码永不出现）。
func databaseMsg(row *dbrepo.Database) *structurev1.Database {
	msg := &structurev1.Database{
		Id: row.ID, ProjectId: row.ProjectID, Name: row.Name,
		Engine: row.Engine, CredentialsRef: row.CredentialsRef, Status: row.Status,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Host: engine.DatabaseDNSName(row.ID),
	}
	if info, ok := engine.DatabaseEngineInfoFor(row.Engine); ok {
		msg.Version, msg.Port = info.Version, info.Port
	}
	return msg
}

// mintDatabasePassword 铸随机密码（hex 48 字符；crypto/rand 失败即内部
// 错误——退化为弱密码不可接受）。
func mintDatabasePassword() string {
	raw := make([]byte, dbPasswordRandBytes)
	if _, err := rand.Read(raw); err != nil {
		panic("fleetlygrpc: entropy source unavailable for database credentials: " + err.Error())
	}
	return hex.EncodeToString(raw)
}

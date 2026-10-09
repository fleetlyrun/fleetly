// Package idem 承载通用幂等执法（ADR-0024）：Idempotency-Key 头 + 单表
// （key → 方法 → 请求体指纹 → 响应引用 → 24h 保留）+ 拦截器一份实现覆盖
// 全部创建型 RPC。三层语义：
//
//   - 同键同体重放 → 返回同一响应（不执行 handler）；
//   - 同键异体 / 跨方法 / 与 body 自带幂等字段不一致 → E_IDEMPOTENCY_KEY_CONFLICT；
//   - 同键在途（未过期认领）→ 冲突（in progress；完成后再重试即得重放）。
//
// 认领 TTL 与请求超时同量级（崩溃自愈：inflight 行过期即可重新认领）；
// handler 失败即释放（无响应可重放，同键干净重试）。响应体按确定性序列化
// 存行（创建型响应均为小消息，不设上限，24h 由 janitor 收口）。
package idem

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/idempotency"
)

// HeaderKey 是幂等键的传输面名（gRPC metadata；REST 经 gateway 同名头
// 映射）。CLI 侧 sdk.WithIdempotencyKey 同键。
const HeaderKey = "idempotency-key"

// Retention 是完成态响应的保留窗（ADR-0024：24h）。
const Retention = 24 * time.Hour

// ClaimTTL 是 inflight 认领的存活窗：取请求超时（30s）的 3 倍——崩溃/
// 挂死留下的 inflight 行在此窗口后可被同键重新认领（重试拿到重放或干净
// 重跑，而不是永久 409）。
const ClaimTTL = 90 * time.Second

// EnforcedMethods 是幂等执法面（ADR-0024：全部创建型 RPC）。守卫
// TestIdempotencyCoversCreateVerbs 以 proto 为源反向对账：创建型动词
// （Create/Deploy/Submit/Put/Set/Rollback）落网或豁免带理由，死条目红。
var EnforcedMethods = map[string]bool{
	"/fleetly.structure.v1.ProjectsService/CreateProject": true,
	"/fleetly.structure.v1.AppsService/CreateApp":         true,
	"/fleetly.structure.v1.SecretsService/PutSecret":      true,
	"/fleetly.structure.v1.ConfigsService/PutConfig":      true,
	// 共享变量 upsert（F2.9，ADR-0043）：同名 put 是覆盖，重放安全。
	"/fleetly.structure.v1.SharedVariablesService/PutSharedVariable": true,
	"/fleetly.structure.v1.VolumesService/CreateVolume":              true,
	"/fleetly.structure.v1.NetworksService/CreateNetwork":            true,
	"/fleetly.structure.v1.DatabasesService/CreateDatabase":          true,
	"/fleetly.proxy.v1.RoutesService/CreateRoute":                    true,
	"/fleetly.delivery.v1.DeploymentsService/Deploy":                 true,
	"/fleetly.delivery.v1.DeploymentsService/Rollback":               true,
	"/fleetly.delivery.v1.HooksService/SetGitHook":                   true,
	"/fleetly.automation.v1.TasksService/CreateTask":                 true,
	"/fleetly.automation.v1.SchedulesService/CreateSchedule":         true,
	// peer 声明是创建型动词（落 pending 行；动词名不在创建型前缀集，显式
	// 纳入——与 ReceiveWebhook 同款先例）。
	"/fleetly.structure.v1.NetworksService/DeclareNetworkPeer": true,
	"/fleetly.identity.v1.UsersService/CreateUser":             true,
	// 告警面创建动词（F2.5，ADR-0041：通道名唯一/规则行落库——重放安全）。
	"/fleetly.telemetry.v1.AlertingService/CreateNotificationChannel": true,
	"/fleetly.telemetry.v1.AlertingService/CreateAlertRule":           true,
	"/fleetly.identity.v1.TeamsService/CreateTeam":                    true,
	"/fleetly.identity.v1.RolesService/CreateRole":                    true,
	"/fleetly.identity.v1.TokensService/CreateToken":                  true,
	"/fleetly.identity.v1.InvitationsService/CreateInvitation":        true,
	// webhook 接收面（Q-21 收口）：gateway 原生入口按 X-GitHub-Delivery 派生
	// 键（webhook:<delivery>）——at-least-once 重投重放首次响应，去重锚与
	// 副作用不再两步分立。动词不在创建型前缀集，由本表显式纳入。
	"/fleetly.delivery.v1.HooksService/ReceiveWebhook": true,
	// change freeze 落行是创建型动词（Set 前缀；ADR-0017 附录 A.3）。
	"/fleetly.system.v1.GovernanceService/SetChangeFreeze": true,
	// 密码设置/重置是设值动词（Set 前缀；C6）——同 key 重放返回首次结果，
	// 不重复下发新密码（与 SetGitHook 同款先例）。
	"/fleetly.identity.v1.UsersService/SetUserPassword": true,
	// Backup 触发族进 idem 面（ADR-0039 决策 10；动词不在创建型前缀集，
	// 显式纳入——DeclareNetworkPeer 同款先例）：platform 面随 F2.3 落地，
	// databases 面随批补录（决策 10 承诺的兑现注）。
	"/fleetly.structure.v1.DatabasesService/TriggerBackup":     true,
	"/fleetly.system.v1.PlatformService/TriggerPlatformBackup": true,
	// 网络重建（ADR-0046，N2 评审批 P1-4）：创建型前缀集外的维护动词，
	// 显式纳入——重放安全（已 attachable 即快速路径零扰动；ClaimTTL 内
	// 的重放拿回首次响应）。
	"/fleetly.structure.v1.NetworksService/RebuildNetwork": true,
}

// dualSourceBearing 是自带幂等键 body 字段的请求（DeployRequest.
// idempotency_key——ADR-0024 降级为部署专锚/commit 去重，非通用幂等）。
// 双源并存且不一致 → 拒绝（客户端契约错误，静默任选其一都会让两种客户端
// 写出互相矛盾的语义）。
type dualSourceBearing interface{ GetIdempotencyKey() string }

// Enforcer 是幂等执法器（拦截器 + janitor sweep 面）。
type Enforcer struct {
	db   *state.DB
	repo *idempotency.Repo
	log  *slog.Logger
}

// NewEnforcer 构造执法器。
func NewEnforcer(db *state.DB, log *slog.Logger) *Enforcer {
	return &Enforcer{db: db, repo: idempotency.New(db.Clock()), log: log}
}

// Unary 返回幂等拦截器：非执法面 / 无键请求原样放行（幂等是可选承诺，
// 不带键的客户端按普通语义调用）。
func (e *Enforcer) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !EnforcedMethods[info.FullMethod] {
			return handler(ctx, req)
		}
		key := keyFromContext(ctx)
		if key == "" {
			return handler(ctx, req)
		}
		if b, ok := req.(dualSourceBearing); ok {
			if bk := b.GetIdempotencyKey(); bk != "" && bk != key {
				return nil, apperr.New("E_IDEMPOTENCY_KEY_CONFLICT",
					"the Idempotency-Key header and the request's idempotency_key field carry different values; send the same value in both, or omit the field").
					WithSuggestion("The header is the generic idempotency mechanism; the body field is reserved as the deployment commit anchor. Align them or drop one.")
			}
		}

		fp := fingerprint(info.FullMethod, req)
		now := e.db.Clock().Now()

		rec, err := e.repo.Get(ctx, e.db.Runner(), key)
		switch {
		case err == nil && rec.ExpiresAt.After(now):
			return e.dispatchRecorded(ctx, rec, info.FullMethod, fp)
		case err == nil:
			// 过期（含崩溃遗留 inflight）：惰性清理后重新认领。
			if rerr := e.repo.Release(ctx, e.db.Runner(), key); rerr != nil {
				e.log.Error("idem: release expired record", "key", key, "err", rerr)
			}
		case !errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_INTERNAL", "idempotency lookup failed").WithCause(err)
		}

		if err := e.repo.Claim(ctx, e.db.Runner(), key, info.FullMethod, fp, ClaimTTL); err != nil {
			if errors.Is(err, state.ErrAlreadyExists) {
				// 并发同键：另一请求刚认领（Get 未及见）。诚实报在途冲突。
				return nil, conflictErr("a request with this Idempotency-Key is already in progress; retry after it completes to get the replayed response")
			}
			return nil, apperr.New("E_INTERNAL", "idempotency claim failed").WithCause(err)
		}

		resp, err := handler(ctx, req)
		if err != nil {
			// 失败无响应可重放：释放认领，同键干净重试。
			if rerr := e.repo.Release(ctx, e.db.Runner(), key); rerr != nil {
				e.log.Error("idem: release after handler failure", "key", key, "err", rerr)
			}
			return nil, err
		}
		e.complete(ctx, key, resp)
		return resp, nil
	}
}

// dispatchRecorded 按在册行分诊：异体/异方法 → 冲突；完成 → 重放；在途 →
// 冲突（in progress）。
func (e *Enforcer) dispatchRecorded(ctx context.Context, rec *idempotency.Record, method, fp string) (any, error) {
	if rec.Method != method || rec.Fingerprint != fp {
		return nil, conflictErr("this Idempotency-Key was already used by a different request; keys are single-purpose and retained for 24h")
	}
	switch rec.State {
	case idempotency.StateCompleted:
		return replay(rec)
	default: // inflight
		return nil, conflictErr("a request with this Idempotency-Key is already in progress; retry after it completes to get the replayed response")
	}
}

// complete 落完成态（响应引用 + 保留窗）。记录失败不吞已发生的业务效果：
// 诚实记日志，该键的幂等面退化为"可重跑"（业务层自身的唯一约束/admission
// 去重兜底）。
func (e *Enforcer) complete(ctx context.Context, key string, resp any) {
	msg, ok := resp.(proto.Message)
	if !ok {
		e.log.Warn("idem: response is not a proto message; idempotency record not completed", "key", key)
		return
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		e.log.Error("idem: marshal response", "key", key, "err", err)
		return
	}
	typeURL := "type.googleapis.com/" + string(msg.ProtoReflect().Descriptor().FullName())
	if err := e.repo.Complete(ctx, e.db.Runner(), key, typeURL, body, Retention); err != nil {
		e.log.Error("idem: complete record", "key", key, "err", err)
	}
}

// Sweep 清理保留窗外的一切行（janitor 周期执行；返回清理行数供观测日志）。
func (e *Enforcer) Sweep(ctx context.Context) (int64, error) {
	return e.repo.Sweep(ctx, e.db.Runner())
}

// replay 从在册响应引用重建响应消息（proto 全局注册表构造零值实例后反
// 序列化——无需逐方法接线）。
func replay(rec *idempotency.Record) (any, error) {
	mt, err := protoregistry.GlobalTypes.FindMessageByURL(rec.ResponseType)
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "the stored idempotent response type can no longer be resolved; the record is stale").
			WithCause(err)
	}
	msg := mt.New().Interface()
	if err := (proto.UnmarshalOptions{}).Unmarshal(rec.ResponseBody, msg); err != nil {
		return nil, apperr.New("E_INTERNAL", "the stored idempotent response body failed to decode; the record is stale").WithCause(err)
	}
	return msg, nil
}

// fingerprint = sha256(method + 确定性请求序列化)。marshal 失败仅在类型
// 违约时可达（受控面）：保守退化（空体参与哈希）保证同键异体仍必撞 409。
func fingerprint(method string, req any) string {
	var body []byte
	if msg, ok := req.(proto.Message); ok {
		opts := proto.MarshalOptions{Deterministic: true}
		if b, err := opts.Marshal(msg); err == nil {
			body = b
		}
	}
	sum := sha256.Sum256(append([]byte(method+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

// keyFromContext 提取幂等键 metadata（缺失返回空串）。
func keyFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vs := md.Get(HeaderKey)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

// conflictErr 构造幂等冲突错误（REST 409 / gRPC AlreadyExists）。
func conflictErr(msg string) error {
	return apperr.New("E_IDEMPOTENCY_KEY_CONFLICT", "%s", msg).
		WithSuggestion(fmt.Sprintf("Use a fresh key for a new request; completed responses replay for %s under the same key and body.", Retention))
}

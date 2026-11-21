// Package function 是 Function 聚合的领域面（ADR-0077 一等实体；设计真源
// docs/design/2026-10-16-functions-api.md v3；实施 ADR-0102）。行类型与
// Repo 接口在此单源（ADR-0075 决策 3：domain 限界上下文持行 + 接口，
// persistence 实现）。聚合根 Function + 冻结体 Revision（function_revisions
// 子表——聚合内子实体，同包单源）；SQLite 实现住
// infrastructure/persistence/function。
package function

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// Function 是聚合根行。生命周期两态（active|deleted 幂等 tombstone）沿仓
// 内既定编码 = DeletedAt 列（” = 活跃），不设第二状态列；Status 是观测
// 缓存（pending|running|degraded|stopped，wire-gen 匹配才采信——零状态类
// 事件，设计 §4.1）。容量三列与接入两列是行面旋钮（三平面分立：不进冻结
// Revision，scale/exposure 翻转零重部署，设计 v3 §4.2）。
type Function struct {
	ID        string
	Seq       int64
	ProjectID string
	Name      string
	Status    string
	// CurrentRevisionID 是在服 Revision 锚（软引用——保留窗清理/聚合终局
	// 全清后悬空是设计内终态，ADR-0101）。
	CurrentRevisionID string
	Generation        uint64
	SpecFingerprint   string
	Replicas          int64
	MinReplicas       int64
	MaxReplicas       int64
	// IdleTimeoutSecs 是缩零/缩容防抖窗（P2-2 决策环行旋钮；0 = 平台缺省
	// 300s——「容量 = 行面旋钮」平面律的行面可调口径）。
	IdleTimeoutSecs int64
	// Exposure 是可扩展枚举（private|public；缺省 private，设计 v3 §6 三律③）。
	Exposure   string
	PublicHost string
	CreatedAt  string
	UpdatedAt  string
	DeletedAt  string
}

// Revision 是冻结体行（不可变：无 UPDATE 路径；spec 列存 FunctionSpec 的
// protojson 规范序列化，digest 存其 sha256——与 revisions 表同口径）。
// 保留窗（ADR-0101）：每函数最近 100、current 指向行不可删、新行落账
// 同事务清最旧非活跃；聚合终局全清。
type Revision struct {
	ID         string
	FunctionID string
	Seq        int64
	Digest     string
	Spec       []byte
	CreatedAt  string
}

// Digest 由 spec 规范序列化派生冻结指纹（sha256 hex；与 delivery/revision
// 同算法——两表各自持列，算法单源可对照）。
func Digest(spec []byte) string {
	sum := sha256.Sum256(spec)
	return hex.EncodeToString(sum[:])
}

// Repo 是 Function 聚合根存取（活跃档读面：tombstone 行 Get/GetByName/
// ListByProject 皆不可见——「tombstone 是事实不是秘密」的写面例外：行级
// 授权/冻结沿活跃行口径，历史可观测性由事件/审计承载）。
type Repo interface {
	// Create 落一行（ID 空 = ULID 铸造，ADR-0092；Seq 零值 = 计数器铸造，
	// ADR-0070）。同项目活跃同名 → state.ErrAlreadyExists（部分唯一索引
	// 执法；tombstone 后同名重建立即可用）。
	Create(ctx context.Context, f *Function) error
	// Get 按 ID 直读（活跃档；tombstone/不存在 → state.ErrNotFound）。
	Get(ctx context.Context, id string) (*Function, error)
	// GetByName 按项目 + 名字读活跃行（name 寻址在边界解析 id 在内核，
	// ADR-0092 设计律）。
	GetByName(ctx context.Context, projectID, name string) (*Function, error)
	// ListByProject 按项目列活跃行（seq 升序 + after 游标，ADR-0026 惯例；
	// exposure/state 空 = 不过滤）。limit<=0 或 >200 回落/钳制缺省 50。
	ListByProject(ctx context.Context, projectID string, afterSeq int64, limit int, exposure, state string) ([]Function, error)
	// List 列全部活跃行（收敛环扫描面；seq 升序）。
	List(ctx context.Context) ([]Function, error)
	// StatsByProject 返回项目活跃函数数与 replicas 之和（配额读面，
	// CheckFunctionQuota 单源消费）。
	StatsByProject(ctx context.Context, projectID string) (active int, replicasSum int64, err error)
	// SetCapacity 落容量期望（行 CAS 语义——受理位已钳 [min,max]，repo
	// 不二次仲裁，ADR-0068 单源纪律）。
	SetCapacity(ctx context.Context, id string, replicas int64) error
	// SetIdleTimeout 落缩零/缩容防抖窗行旋钮（P2-2 决策环；0 = 平台缺省）。
	SetIdleTimeout(ctx context.Context, id string, secs int64) error
	// DecideCapacity 是决策环的条件容量 CAS（P2-2）：仅当行未被他写变时
	// 落目标副本（乐观并发锚 = 调用方所见 (replicas, updated_at) 对——
	// RFC3339 秒精度下 replicas 对位补强同秒写窗），返回是否落账。唤醒
	//（RequestWake）/用户 scale 与决策环的竞态在行上串行——落败方下拍
	// 重算，无撕列。出界钳 [min,max] 由调用方计算（repo 不二次仲裁——
	// ADR-0068 单源纪律同 SetCapacity）。
	DecideCapacity(ctx context.Context, id string, replicas, sawReplicas int64, sawUpdatedAt string) (bool, error)
	// SetExposure 落接入两列（public→private 时 public_host 清位）。
	SetExposure(ctx context.Context, id, exposure, publicHost string) error
	// SwapCurrent 是原子换轨（current 指针 + 指纹同拍——Deploy/Rollback 的
	// 写半边；复用既有冻结行不新增，内容寻址断言）。
	SwapCurrent(ctx context.Context, id, revisionID, fingerprint string) error
	// AdvanceStatus 推进观测缓存（收敛环按 wire-gen 匹配才调用）。
	AdvanceStatus(ctx context.Context, id, status string) error
	// EnsureGeneration 是指纹 CAS 的行代数口（ADR-0064/Databases 同族）：
	// 指纹未变 → 返回现行（零推进——纯伸缩/重放不换代）；指纹变化 →
	// generation+1 与新指纹同拍落行并返回新值。重启安全（行上持久）。
	EnsureGeneration(ctx context.Context, id, fingerprint string) (uint64, error)
	// SoftDelete 落 tombstone（幂等；聚合终局全清 revisions 由调用方同
	// 事务经 RevisionRepo.DeleteAllByFunction 承载，ADR-0101 决策 4）。
	SoftDelete(ctx context.Context, id string) error
	// RequestWake 是唤醒条件容量 CAS（P2-1a，设计 v2 §1.2 裁决——容量列
	// 即唤醒标记）：仅当 replicas=0 时落 rung（= max(1, min_replicas)），
	// 返回是否落账。幂等（N 并发请求恰一 true）、防踩踏（用户已 scale>0
	// 则 CAS 不命中）。唤醒方（activator）由此把收敛环踢向 scale-only
	// Ensure——activator 永不触 Runtime。
	RequestWake(ctx context.Context, id string) (woken bool, err error)
}

// RevisionRepo 是冻结体存取（Prune 语义与 delivery/revision 同律——
// ADR-0101 全域统一律，Function 锚集 = current 指针指向行）。
type RevisionRepo interface {
	// Create 落一行（ID = entityid.Revision 派生确定性 <fn id>-r<n>；
	// 同函数同内容唯一 → state.ErrAlreadyExists，复用走 FindByDigest）。
	Create(ctx context.Context, rev *Revision) error
	// Get 按 ID 直读。
	Get(ctx context.Context, id string) (*Revision, error)
	// GetBySeq 按函数内序号读（r1..rn 对外形态；窗外 → ErrNotFound）。
	GetBySeq(ctx context.Context, functionID string, seq int64) (*Revision, error)
	// FindByDigest 按函数 + 内容指纹找既有冻结体（内容寻址复用）。
	FindByDigest(ctx context.Context, functionID, digest string) (*Revision, error)
	// NextSeq 返回函数下一序号（首条 = 1；单调）。
	NextSeq(ctx context.Context, functionID string) (int64, error)
	// ListByFunction 返回函数全部保留窗内 Revision（升序 + after 游标）。
	ListByFunction(ctx context.Context, functionID string, afterSeq int64, limit int) ([]Revision, error)
	// Prune 是保留窗清理（ADR-0101）：保留最新 keep 行与 protect 名单
	//（current 指针行由调用方传入），删除其余最旧行，返回删除数。与新行
	// 落账同事务调用。
	Prune(ctx context.Context, functionID string, protect []string, keep int) (int64, error)
	// DeleteAllByFunction 是聚合终局全清（ADR-0101 决策 4：与 SoftDelete
	// 同事务；current 指针悬空为设计内终态）。
	DeleteAllByFunction(ctx context.Context, functionID string) error
}

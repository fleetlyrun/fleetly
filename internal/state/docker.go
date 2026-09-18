package state

import (
	"context"
	"errors"
	"time"
)

// DockerClient 是底座（Docker/Swarm）访问的小端口（架构 §2.8 Runtime 端口
// 的观测与身份子集，v0.1 切面）。核心（本包）只认识本文件定义的核心类型，
// 第三方类型只存在于适配器内部（internal/substrate，moby/client 实现）。
//
// 纪律：
//   - 方法返回的核心类型字段命名用平台语义，不透传底座结构；
//   - 底座对象版本（ObjectVersion）是写前直读的乐观令牌（state-model §2.2
//     读契约），适配器必须逐字取自底座对象版本，不得自造。
type DockerClient interface {
	// Ping 探测底座可达性（观测缓存刷新的先决条件）。
	Ping(ctx context.Context) error

	// ListNodeObservations 返回全量节点观测快照（观测缓存 30s 全量 resync
	// 的数据源，state-model §2.2）。底座不可达时返回错误，绝不返回部分
	// 快照冒充全量。
	ListNodeObservations(ctx context.Context) ([]SubstrateNode, error)

	// SelfNodeID 返回本机 Swarm node ID；引擎未启用 Swarm 时返回
	// ErrNotSwarmManager。
	SelfNodeID(ctx context.Context) (string, error)

	// UpdateNodeLabel 以乐观令牌更新节点 label（v0.1 唯一的 label 写入
	// 接口，承载 node-id 身份锚；§2.4 最小 label 集，业务对象 label 下发
	// 随 T2.14）。expected 与底座当前版本不符时返回 ErrVersionConflict；
	// label 已是目标值时为幂等 no-op 并返回 nil。
	UpdateNodeLabel(ctx context.Context, swarmNodeID, key, value string, expected ObjectVersion) error

	// ResolveObjectVersion 直读底座对象版本（写前直读；对象缺失返回
	// ErrObjectNotFound）。
	ResolveObjectVersion(ctx context.Context, kind ObjectKind, id string) (ObjectVersion, error)

	// SubscribeEvents 订阅底座事件流（node/service/task 变更作观测缓存的
	// 失效信号，state-model §2.2；只作缓存失效信号，不作产品事件来源）。
	// 返回的 channel 在 ctx 取消或流结束时关闭。
	SubscribeEvents(ctx context.Context) (<-chan SubstrateEvent, error)

	// Close 释放底层连接资源。
	Close() error
}

// ObjectKind 是写前直读的底座对象类别。
type ObjectKind string

const (
	// ObjectKindNode 是 Swarm 节点（id = swarm node ID）。
	ObjectKindNode ObjectKind = "node"
	// ObjectKindService 是 Swarm 服务（id = swarm 服务名；平台名到底座名
	// 的映射随 T2.14 适配器落地，本阶段令牌纪律先立）。
	ObjectKindService ObjectKind = "service"
)

// Valid 报告 kind 是否为已定义类别。
func (k ObjectKind) Valid() bool {
	switch k {
	case ObjectKindNode, ObjectKindService:
		return true
	}
	return false
}

// ObjectVersion 是底座对象版本（乐观令牌）。Index 取自底座对象自身的
// 单调版本号，语义逐字镜像底座。
type ObjectVersion struct {
	Index uint64
}

// SubstrateNode 是一次节点观测快照（nodes 表行来源）。State/Availability
// 逐字镜像底座（平台不制造节点健康语义，state-model §2.3）。
type SubstrateNode struct {
	// SwarmNodeID 是底座节点 ID（runtime_node_refs 映射的键）。
	SwarmNodeID string
	// Hostname 是底座节点主机名（显示名）。
	Hostname string
	// State 是底座节点状态（ready/down/disconnected/unknown 逐字镜像）。
	State string
	// Availability 是底座节点可用性（active/pause/drain 逐字镜像）。
	Availability string
	// IsManager 报告该节点是否承担 manager 角色。
	IsManager bool
	// Version 是底座节点对象版本（节点 label 写入的乐观令牌）。
	Version ObjectVersion
	// Labels 是节点全部 label（含 fleetly.node-id 身份锚）。
	Labels map[string]string
}

// SubstrateEvent 是一次底座事件（仅作观测缓存失效信号）。
type SubstrateEvent struct {
	Type   string
	Action string
	At     time.Time
}

// 观测信号相关的事件类别（Docker events 的 Type 取值子集；变更 1s 内
// 触发 resync，state-model §2.2）。
const (
	EventTypeNode    = "node"
	EventTypeService = "service"
	EventTypeTask    = "task"
)

// RelevantForObservation 报告事件是否应触发观测缓存失效。
func RelevantForObservation(ev SubstrateEvent) bool {
	switch ev.Type {
	case EventTypeNode, EventTypeService, EventTypeTask:
		return true
	}
	return false
}

// 底座端口哨兵错误：适配器将第三方错误归类为以下哨兵（errors.Is 判定），
// 核心据此映射契约错误码，适配器自身不出现契约码。
var (
	// ErrObjectNotFound 表示直读的底座对象不存在（可能已被并发删除）。
	ErrObjectNotFound = errors.New("substrate object not found")
	// ErrVersionConflict 表示乐观令牌与底座当前版本不符（并发修改）。
	ErrVersionConflict = errors.New("substrate object version conflict")
	// ErrNotSwarmManager 表示引擎未启用 Swarm（或本机非 manager），
	// 节点身份锚定与节点观测不可用。
	ErrNotSwarmManager = errors.New("docker engine is not an active swarm manager")
)

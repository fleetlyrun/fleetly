package dataservicetemplate

// kind descriptor 注册表（ADR-0096 决策 1，design §4.1「kind descriptor」
// 段；B2e 应用模块归一批落地）：托管数据服务三种类型（database/pubsub/
// bucketstore）各以一枚 descriptor 自述，机制层（application/dataservices
// 单应用模块包的分类型收敛步、backups 应用模块的跨类型备份排程、gRPC 受理面的配额门）
// 经本注册表按类型取面——受管域「一个通用 reconciler + N 个 Describe()」
//（ADR-0004）的用户域对称形态。
//
// 注册表本体下沉 domain 的理由（红队 B-AT-01）：三方消费者里有两颗应用模块
//（dataservices/backups）——应用模块两两禁 import（ADR-0088 决策 7 层内互引
// 矩阵），注册表若住任一应用模块包即构成模块间硬边；dataservicetemplate 是三种类型模板
// 包的共享内核（stdlib-only 近叶子），是 descriptor 的自然伴生位。
//
// 形状裁剪（B2e 最小集，YAGNI + B4a 首扩）：只装 database 类型当下真正被
// 消费的面——Kind（分类型步分发键 + 循环表每类型一循环的绑定词）/Quota（gRPC
// 受理配额门，ADR-0096 决策 8：databases 16 / pubsubs 8 / bucket_stores 4）/
// EventPrefix（事件码前缀；bucket_store 与类型词 bucketstore 不同形，
// 故独立成员）/DataCapabilities（数据面能力位，B4a 首个跨类型消费者 =
// backups 应用模块排程门 + TriggerBackup 受理门 + 目录 capabilities 真值——
// 红队 A9「无备份引擎不铸行」的判据源；per-engine 分叉值〔kafka false /
// nats true〕随 B6a 模板契约面 DataCapabilities() 落地时再下沉引擎轴）。
// 设计稿列举的其余成员（域聚合 Repo 访问面、模板包、凭证/连接方言）随
// B4b/B5 首个跨类型消费者落地时再扩——本包不 import internal/，聚合 Repo
// 形态届时以本包中性接口承载并经组合根注入。
//
// 登记时点：各类型模板包（L2 类型声明层——databasetemplate/pubsubtemplate/
// bucketstoretemplate）在包初始化期以 var 初始化式登记自家 descriptor（databasetemplate
// 静态 registry 表同款出生序：Go 依赖初始化序保证消费者首次读取前已在册），
// 此后进程内只读；同类型重复登记须逐字节等值（幂等——测试多次构造引擎不致
// 冲突），不等值即 panic（双真源是编程错误，不是运行时状态）。

import (
	"fmt"
	"sort"
	"sync"
)

// 类型词面常量（ADR-0093 词汇；卷实例标记 kind 轴、循环表名、descriptor
// 键三处同源——validKind 判据的三枚在册值）。
const (
	// KindDatabase 是数据库类型（ADR-0029 第三形态；B2e 唯一在册类型）。
	KindDatabase = "database"
	// KindPubSub 是 PubSub 类型（ADR-0096；descriptor 随 B4b 登记——登记
	// 前循环表占位循环对本类型恒 no-op）。
	KindPubSub = "pubsub"
	// KindBucketStore 是 Bucket Store 类型（ADR-0096；descriptor 随 B5
	// 登记——同上）。
	KindBucketStore = "bucketstore"
)

// DataCapabilities 是一个托管数据服务类型的数据面能力位（ADR-0096 决策 6
// 契约面 DataCapabilities 的 descriptor 半边；B4a）。能力位诚实推论：
// backup=false ⇒ migrate=false（无备份面即无「转储→再注入」基版本迁移
// ——目录/文案如实披露，kafka v1 先例）。
type DataCapabilities struct {
	// Backup：本类型是否具备数据备份面（Backup/Restore 渲染面在册）。消费
	// 面：backups 应用模块 schedulePass 过滤（无备份能力的行不铸台账行——
	// 红队 A9「33h 11.4 万行」事故防复发）+ TriggerBackup 受理拒
	//（E_INVALID_ARGUMENT）+ 目录 capabilities.backup 真值。
	Backup bool
}

// KindDescriptor 是一个托管数据服务类型的自述面（进程内只读值；成员集
// 见文件头「形状裁剪」）。
type KindDescriptor struct {
	// Kind 是类型词面（KindDatabase/KindPubSub/KindBucketStore；
	// 注册表键 + 分类型步分发键 + 循环表名）。
	Kind string
	// Quota 是 per-Project 活跃实例上限（受理配额门的数据源；必须为正）。
	Quota int
	// EventPrefix 是事件码前缀（`<EventPrefix>.created/.updated/.deleted/
	// .credentials_rotated` 四件为类型内事件最小集，ADR-0096 决策 8）；形态
	// 对齐 eventcode 名左段 `[a-z][a-z0-9_]*`。
	EventPrefix string
	// DataCapabilities 是数据面能力位（B4a；database 类型 = Backup:true）。
	DataCapabilities DataCapabilities
}

// kindRegistry 是注册表本体（实例化形态供白盒测试独立构造；包级默认
// 实例经 RegisterKind/LookupKind/Kinds 暴露）。
type kindRegistry struct {
	mu     sync.RWMutex
	byName map[string]KindDescriptor
}

// register 登记一类型并回传 descriptor（var 初始化式友好）。形状坏 = panic
// （登记发生在包初始化期，是编程错误面——与 eventcode/errcode MustRegister
// 同口径）；同类型重复登记等值幂等、不等值 panic。
func (r *kindRegistry) register(d KindDescriptor) KindDescriptor {
	if err := d.validate(); err != nil {
		panic("dataservicetemplate: register kind descriptor: " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = map[string]KindDescriptor{}
	}
	if prev, ok := r.byName[d.Kind]; ok {
		if prev != d {
			panic(fmt.Sprintf("dataservicetemplate: kind %q registered twice with different descriptors (%+v vs %+v) — a kind has exactly one descriptor", d.Kind, prev, d))
		}
		return prev
	}
	r.byName[d.Kind] = d
	return d
}

// lookup 按类型词面取 descriptor（缺席 = 未登记——机制层据此 no-op）。
func (r *kindRegistry) lookup(kind string) (KindDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.byName[kind]
	return d, ok
}

// kinds 返回在册 descriptor 全集（按 Kind 排序——确定性序列化纪律）。
func (r *kindRegistry) kinds() []KindDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]KindDescriptor, 0, len(r.byName))
	for _, d := range r.byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// validate 是 descriptor 形状闸：Kind 走 validKind（卷实例标记同判据）、
// Quota 为正、EventPrefix 走 eventcode 名左段形态。
func (d KindDescriptor) validate() error {
	if !validKind(d.Kind) {
		return fmt.Errorf("kind %q is not a valid kind word", d.Kind)
	}
	if d.Quota <= 0 {
		return fmt.Errorf("kind %q declares a non-positive quota %d", d.Kind, d.Quota)
	}
	if !validEventPrefix(d.EventPrefix) {
		return fmt.Errorf("kind %q declares event prefix %q outside the [a-z][a-z0-9_]* form", d.Kind, d.EventPrefix)
	}
	return nil
}

// validEventPrefix 是事件码前缀判据（eventcode namePattern 左段
// `[a-z][a-z0-9_]*` 的本地等价；界长 32）。
func validEventPrefix(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'z':
		case c == '_':
		default:
			return false
		}
	}
	return true
}

// defaultKinds 是进程内注册表（各类型模板包初始化期登记）。
var defaultKinds kindRegistry

// RegisterKind 登记一类型 descriptor 并回传（供 `var Kind = RegisterKind(
// …)` 初始化式——登记与声明一体）。形状坏/同类型不等值重登 = panic。
func RegisterKind(d KindDescriptor) KindDescriptor { return defaultKinds.register(d) }

// LookupKind 按类型词面取在册 descriptor；缺席返回 false（分类型步据此
// no-op 立返——机制先行占位的判据）。
func LookupKind(kind string) (KindDescriptor, bool) { return defaultKinds.lookup(kind) }

// Kinds 返回在册 descriptor 全集（按 Kind 排序）。
func Kinds() []KindDescriptor { return defaultKinds.kinds() }

// KindSupportsBackup 报告类型是否具备数据备份面（DataCapabilities.Backup 的
// 门判据单源——红队 A9：无备份能力的类型不铸台账行、TriggerBackup 受理拒；
// B4a 消费面 = backups 应用模块 schedulePass 与 gRPC 受理位）。缺席注册表的
// 类型恒 false（机制先行占位类型在登记 descriptor 前本就无行源，判据保守）。
func KindSupportsBackup(kind string) bool { return defaultKinds.backsUp(kind) }

// backsUp 是 KindSupportsBackup 的实例面（白盒测试经独立注册表构造，不碰包级
// 默认实例）。
func (r *kindRegistry) backsUp(kind string) bool {
	d, ok := r.lookup(kind)
	return ok && d.DataCapabilities.Backup
}

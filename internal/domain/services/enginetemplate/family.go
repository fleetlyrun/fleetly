package enginetemplate

// family descriptor 注册表（ADR-0096 决策 1，design §4.1「family descriptor」
// 段；B2e 卫星归一批落地）：托管数据服务三家族（database/pubsub/
// bucketstore）各以一枚 descriptor 自述，机制层（application/dataservices
// 单卫星包的分族收敛步、backups 卫星的跨族备份排程、gRPC 受理面的配额门）
// 经本注册表按族取面——受管域「一个通用 reconciler + N 个 Describe()」
//（ADR-0004）的用户域对称形态。
//
// 注册表本体下沉 domain 的理由（红队 B-AT-01）：三方消费者里有两颗卫星
//（dataservices/backups）——卫星两两禁 import（ADR-0088 决策 7 环内互引
// 矩阵），注册表若住任一卫星包即构成星间硬边；enginetemplate 是三家族模板
// 包的共享内核（stdlib-only 近叶子），是 descriptor 的自然伴生位。
//
// 形状裁剪（B2e 最小集，YAGNI）：只装 database 家族当下真正被消费的面——
// Family（分族步分发键 + 环表每族一环的绑定词）/Quota（gRPC 受理配额门，
// ADR-0096 决策 8：databases 16 / pubsubs 8 / bucket_stores 4）/EventFamily
//（事件码族前缀；bucket_store 与家族词 bucketstore 不同形，故独立成员）。
// 设计稿列举的其余成员（域聚合 Repo 访问面、模板包、凭证/连接方言、备份
// 能力位 DataCapabilities）随 B4/B4b/B5 首个跨族消费者落地时再扩——本包
// 不 import internal/，聚合 Repo 形态届时以本包中性接口承载并经组合根注入。
//
// 登记时点：各家族模板包（L2 家族声明层——dbtemplate/pubtemplate/
// buckettemplate）在包初始化期以 var 初始化式登记自家 descriptor（dbtemplate
// 静态 registry 表同款出生序：Go 依赖初始化序保证消费者首次读取前已在册），
// 此后进程内只读；同族重复登记须逐字节等值（幂等——测试多次构造引擎不致
// 冲突），不等值即 panic（双真源是编程错误，不是运行时状态）。

import (
	"fmt"
	"sort"
	"sync"
)

// 家族词面常量（ADR-0093 词汇；卷实例标记 family 轴、环表环名、descriptor
// 键三处同源——validFamily 判据的三枚在册值）。
const (
	// FamilyDatabase 是数据库家族（ADR-0029 第三形态；B2e 唯一在册族）。
	FamilyDatabase = "database"
	// FamilyPubSub 是 PubSub 家族（ADR-0096；descriptor 随 B4b 登记——登记
	// 前环表占位环对本族恒 no-op）。
	FamilyPubSub = "pubsub"
	// FamilyBucketStore 是 Bucket Store 家族（ADR-0096；descriptor 随 B5
	// 登记——同上）。
	FamilyBucketStore = "bucketstore"
)

// FamilyDescriptor 是一个托管数据服务家族的自述面（进程内只读值；成员集
// 见文件头「形状裁剪」）。
type FamilyDescriptor struct {
	// Family 是家族词面（FamilyDatabase/FamilyPubSub/FamilyBucketStore；
	// 注册表键 + 分族步分发键 + 环表环名）。
	Family string
	// Quota 是 per-Project 活跃实例上限（受理配额门的数据源；必须为正）。
	Quota int
	// EventFamily 是事件码族前缀（`<EventFamily>.created/.updated/.deleted/
	// .credentials_rotated` 四件为族内事件最小集，ADR-0096 决策 8）；形态
	// 对齐 eventcode 名左段 `[a-z][a-z0-9_]*`。
	EventFamily string
}

// familyRegistry 是注册表本体（实例化形态供白盒测试独立构造；包级默认
// 实例经 RegisterFamily/LookupFamily/Families 暴露）。
type familyRegistry struct {
	mu     sync.RWMutex
	byName map[string]FamilyDescriptor
}

// register 登记一族并回传 descriptor（var 初始化式友好）。形状坏 = panic
// （登记发生在包初始化期，是编程错误面——与 eventcode/errcode MustRegister
// 同口径）；同族重复登记等值幂等、不等值 panic。
func (r *familyRegistry) register(d FamilyDescriptor) FamilyDescriptor {
	if err := d.validate(); err != nil {
		panic("enginetemplate: register family descriptor: " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = map[string]FamilyDescriptor{}
	}
	if prev, ok := r.byName[d.Family]; ok {
		if prev != d {
			panic(fmt.Sprintf("enginetemplate: family %q registered twice with different descriptors (%+v vs %+v) — a family has exactly one descriptor", d.Family, prev, d))
		}
		return prev
	}
	r.byName[d.Family] = d
	return d
}

// lookup 按家族词面取 descriptor（缺席 = 未登记——机制层据此 no-op）。
func (r *familyRegistry) lookup(family string) (FamilyDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.byName[family]
	return d, ok
}

// families 返回在册 descriptor 全集（按 Family 排序——确定性序列化纪律）。
func (r *familyRegistry) families() []FamilyDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FamilyDescriptor, 0, len(r.byName))
	for _, d := range r.byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Family < out[j].Family })
	return out
}

// validate 是 descriptor 形状闸：Family 走 validFamily（卷实例标记同判据）、
// Quota 为正、EventFamily 走 eventcode 名左段形态。
func (d FamilyDescriptor) validate() error {
	if !validFamily(d.Family) {
		return fmt.Errorf("family %q is not a valid family word", d.Family)
	}
	if d.Quota <= 0 {
		return fmt.Errorf("family %q declares a non-positive quota %d", d.Family, d.Quota)
	}
	if !validEventFamily(d.EventFamily) {
		return fmt.Errorf("family %q declares event family %q outside the [a-z][a-z0-9_]* form", d.Family, d.EventFamily)
	}
	return nil
}

// validEventFamily 是事件码族前缀判据（eventcode namePattern 左段
// `[a-z][a-z0-9_]*` 的本地等价；界长 32）。
func validEventFamily(s string) bool {
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

// defaultFamilies 是进程内注册表（各家族模板包初始化期登记）。
var defaultFamilies familyRegistry

// RegisterFamily 登记一族 descriptor 并回传（供 `var Family = RegisterFamily(
// …)` 初始化式——登记与声明一体）。形状坏/同族不等值重登 = panic。
func RegisterFamily(d FamilyDescriptor) FamilyDescriptor { return defaultFamilies.register(d) }

// LookupFamily 按家族词面取在册 descriptor；缺席返回 false（分族步据此
// no-op 立返——机制先行占位的判据）。
func LookupFamily(family string) (FamilyDescriptor, bool) { return defaultFamilies.lookup(family) }

// Families 返回在册 descriptor 全集（按 Family 排序）。
func Families() []FamilyDescriptor { return defaultFamilies.families() }

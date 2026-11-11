// engine.go 是引擎壳：Engine 汇聚结构 + 收敛循环注册项（loop）+ 包级哨兵
// 错误 + 驱动暴露面（P3 收官批 8 壳收口）。参数面（options.go）、构造面
// （construct.go）、生命周期（lifecycle.go）、ensure 确保缓存协议与互斥底座
// （ensure.go）与各域状态 struct（随域文件）自本文件归位——函数体逐字
// 迁移，零行为变更（ADR-0075 application 层内聚形态）。
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/services/diagnosis"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/alertrule"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/channel"
)

// 内部装配哨兵（仅 engine 核内使用；输入面哨兵真源在 engine/api）。
var (
	// errIntakeUnconfigured 是 intake 应用模块缺席（手动测试装配未注入
	// Deps.IntakeFactory）时的精确失败；生产装配恒注入工厂。
	errIntakeUnconfigured = errors.New("engine: intake module is not configured")
	// errBackupUnconfigured 是 backups 应用模块缺席（手动测试装配未注入
	// Deps.BackupFactory）时的精确失败；生产装配恒注入工厂。
	errBackupUnconfigured = errors.New("engine: backup module is not configured")
	// errDataServiceUnconfigured 是 dataservices 应用模块缺席（手动测试装配
	// 未注入 Deps.DataServiceFactory）时的精确失败；生产装配恒注入工厂。
	errDataServiceUnconfigured = errors.New("engine: data service module is not configured")
)

// loop（Go 类型名 ring）是收敛循环注册项（Start/DriveOnce 表驱动：goroutine 挂载、启动
// Kick、手动驱动序走同一张表——新循环 = 表加一行 + 一个 Loop 字段；表序
// 即 DriveOnce 手动驱动序（schedule 先于 task：同一轮内铸出的 Task 即刻
// 进补足链））。
type ring struct {
	name string
	loop *Loop
	step func(context.Context)
}

// Engine 是部署收敛引擎：Deployment 状态机单写者 + admission + Runtime
// Watch 消费。写路径全部四件一拍（CAS + Outbox + 审计；部署记录无
// tombstone）；决策读平台权威表，观测缓存仅作信号（架构 §6）。
type Engine struct {
	db      state.DB
	log     *slog.Logger
	runtime capability.Runtime
	clock   state.Clock
	opts    Options

	// repos 是本引擎的聚合目录（db 绑定形态——组合根经 Binder 以
	// db.Runner() 造出；读径直调，CQRS-lite）。事务作用域经 transact.Tx.Do
	// 的闭包 s 走同一类型的 tx 绑定形态（ADR-0080）。
	repos transact.Stores
	tx    transact.Tx

	// backups 是 Backup 域应用模块的核消费面（F2.2，ADR-0039：台账 repo +
	// 备份循环 + Platform Backup 执行链）。P5 wave-3 拆包形态（ADR-0088 决策
	// 2 P5）+ engine 去应用模块 import 第八批（2026-11-10）：本字段只持
	// contract.Backup 接口，具体装配器经 Deps.BackupFactory 由组合根注入
	//（construct.go 绑定端口袋）；核不再 import backups 应用模块包。循环表
	// step 经 contract.StepOf(e.backups, "backup")、gRPC Backup/platform 面与
	// lifecycle 启动清扫经核门面委托（消费点零改）；componentrestart 的
	// providers 读经接口 ObjectStore 访问器；值类型/事件名/哨兵在
	// internal/application/engine/contract（engine 类型别名原可达）。
	// KickBackups 留核（Loop 挂载核持有，KickTasks 同款）。
	backups contract.Backup
	// execs 是 exec 会话域应用模块的核消费面（受理/路由/生命周期；进程内
	// 活体，零持久化）。P5 wave-2 拆包形态（ADR-0088 决策 2 P5）+ engine 去
	// 应用模块 import 第六批（2026-11-10）：本字段只持 contract.Exec 接口，
	// 具体装配器经 Deps.ExecFactory 由组合根注入（construct.go 绑定端口袋）；
	// 核不再 import exec 会话应用模块包。gRPC/assembly 消费面经核门面委托
	//（消费点零改）；Start 的回环中继挂载（组合根调用）留核、经应用模块
	// ExecFace/RelayInternalCredential 访问器读；值类型/哨兵在
	// internal/application/engine/contract（engine 类型别名原可达）。
	execs contract.Exec

	// browses 是 Browse 会话域应用模块的核消费面（F3.6，ADR-0051：会话
	// 注册表进程内活体，行是台账）。P5 wave-3 拆包形态（ADR-0088 决策 2 P5）
	// + engine 去应用模块 import 第四批（2026-11-10）：本字段只持
	// contract.Browse 接口，具体装配器经 Deps.BrowseFactory 由组合根注入
	//（construct.go 绑定端口袋）；核不再 import 浏览会话应用模块包。循环表
	// step 经 contract.StepOf(e.browses, "browse")、gRPC Browse 受理/grant
	// 面与 routepublish 的 ephemeral 路由合并面经核门面/接口委托（消费点
	// 零改）；值类型/常量/哨兵在 internal/application/engine/contract（engine
	// 类型别名原可达）；观测窄接口窄化为会话轴签名（browseOwner 铸造留核
	// 绑定闭包）。KickBrowse 留核（Loop 挂载核持有，KickTasks 同款）。
	browses contract.Browse

	// Logging 域（F2.4，ADR-0040）：受管日志存储 Provider（核消费面：build
	// 日志回读/managed 面/组件重启；采集循环域经 logpipes 应用模块自持）。
	logging capability.Logging
	// logpipes 是采集循环应用模块的核消费面。P5 wave-1 拆包形态（ADR-0088
	// 决策 2 P5）：应用模块独立成包。
	// engine 去应用模块 import 第二批（2026-11-10）：本字段只持
	// contract.Logging 接口，具体装配器经 Deps.LoggingFactory 由组合根注入
	// （construct.go 绑定端口袋）；核不再 import 采集应用模块包。循环表
	// step 经 contract.StepOf(e.logpipes, "logging")、gRPC 装配面消费经核
	// 门面 LoggingProvider 委托；内核契约共享面（SleepCtx 等）在
	// internal/application/engine/contract。
	logpipes contract.Logging

	// metrics 是指标采集+告警域应用模块的核消费面（F2.5/ADR-0041 采集循环 +
	// 告警评估/通道派发 + crash-loop 通知，五文件 = metrics/alerting/
	// channelnotify/exposition/crashnotify）。P5 wave-2 拆包形态（ADR-0088
	// 决策 2 P5）+ engine 去应用模块 import 第五批（2026-11-10）：本字段只持
	// contract.Metrics 接口，具体装配器经 Deps.MetricsFactory 由组合根注入
	//（construct.go 绑定端口袋）；核不再 import 指标采集+告警应用模块包。
	// 循环表 step 经 contract.StepOf(e.metrics, "metrics")、AlertRule/Channel
	// API 面与 capability/诊断面的核内消费经核门面委托（AlertRuleRepo/
	// MetricsProvider 族）；observ 的 crash-loop 评估点经接口
	// EvaluateCrashLoopNotify，converge 的系统告警沿经 DispatchSystemAlert；
	// 值类型/事件常量在 internal/application/engine/contract（engine 类型
	// 别名原可达）；观测窄接口闭包族（ownerOf/ownerTeam → appOwnerOf 基础
	// 类型化）与事实契约（ContainerExitFact/DiagnoseWindow）的裁定见应用
	// 模块包注与 ADR-0088。
	metrics contract.Metrics

	// manageds 是受管域应用模块的核消费面（ADR-0004 通用受管 reconciler：
	// Proxy → Registry → Logging → Metrics 注册序）。P5 wave-4 拆包形态
	// （ADR-0088 决策 2 P5）+ engine 去应用模块 import 第三批（2026-11-10）：
	// 本字段只持 contract.Managed 接口，具体装配器经 Deps.ManagedFactory 由
	// 组合根注入（construct.go 绑定端口袋）；核不再 import 受管应用模块包。
	// 循环表 step 经 contract.StepOf(e.manageds, "managed")、networkrebuild 的
	// 受管网集判定经 ManagedProviders；gensFor 与 gens map 留核（ensureMu 锁
	// 与 map 的配对裁定，应用模块经端口读写）；内核契约共享面（ensure cache 协议/
	// 指纹公式/ManagedGen）在 internal/application/engine/contract；观测窄接口
	// 窄化为 system 轴签名（systemOwner 铸造留核绑定闭包）。
	manageds contract.Managed

	// dataservices 是托管数据服务单应用模块的核消费面（ADR-0096 决策 1；
	// B2e 自 databases 应用模块〔ADR-0029 用户域收敛循环：第三条部署轨，不进
	// Deployment 状态机；P5 wave-4 拆包形态，ADR-0088 决策 2 P5〕整包更名
	// 归一；engine 去应用模块 import 第九批，2026-11-10）：本字段只持
	// contract.DataService 接口，具体装配器经 Deps.DataServiceFactory 由
	// 组合根注入（construct.go 绑定端口袋）；核不再 import 应用模块包。
	// 循环表三条目（database/pubsub/bucketstore）各经 Module.Loops 的
	// 分类型步挂载、gRPC Database 受理/轮换/Update 面经核门面委托、
	// materialize 的 env 注入面（InjectDatabaseEnv）与 render/task/bluegreen
	// 的投影事实面（EgressNetworkMap）经接口（消费点零改）；kind descriptor
	// 注册表住 domain（dataservicetemplate）；rotateMu 随域；观测窄接口窄化为
	// database 轴签名（databaseOwner 铸造留核绑定闭包）。
	dataservices contract.DataService

	loop         *Loop
	buildLoop    *Loop
	managedLoop  *Loop
	databaseLoop *Loop
	// pubsubLoop/bucketstoreLoop 是数据服务另两类型的环（ADR-0096 决策 1
	// 循环表三条目：每类型一循环=每类型一写者+故障域隔离；descriptor 登记前〔B4b/
	// B5〕step 为 no-op 占位，Kick 面随各自批次落）。
	pubsubLoop      *Loop
	bucketstoreLoop *Loop
	backupLoop      *Loop // Backup 环（调度/执行/恢复/保留，ADR-0039）
	loggingLoop     *Loop // 采集循环（域流对账，ADR-0040；流本体在 goroutine）
	metricsLoop     *Loop // 指标采集循环（scrape/评估，ADR-0041；粗节拍锚在 step 内）
	taskLoop        *Loop // Task/Run 收敛循环（janitor/补足/Ensure/收口）
	scheduleLoop    *Loop // Schedule 到期拍环（F1.7）
	browseLoop      *Loop // Browse 会话收敛循环（F3.6，ADR-0051）
	driftLoop       *Loop // ADR-0022 漂移扫描循环（spec 对照 + 稳态看门狗）
	rings           []ring
	// peerIsolationMu/peerIsolations 是撤销 peer 后的待剥离标记集（R16-M4
	// 异步语义：请求路径只标记 + Kick，剥离由漂移扫描拍执行——不再在请求
	// goroutine 内同步 replay/Ensure）。map 在 New 初始化，读写都在
	// peerIsolationMu 内。
	peerIsolationMu sync.Mutex
	peerIsolations  map[peerIsolationRequest]bool
	// ringMu/ringDeaths 是循环终态失败记账（runLoopWithRestart 在 panic
	// 重启预算耗尽后落账；瞬时重启不记死）。HealthComponents/DeadRings 是
	// health/doctor 查询面（R02-M7）。map 在 New 初始化，读写都在 ringMu 内。
	ringMu     sync.Mutex
	ringDeaths map[string]bool
	// ringRestart 是循环 panic 后的有界重启策略（生产缺省 5 次、1s→30s
	// 指数退避；测试可收紧）。
	ringRestart ringRestartPolicy
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// intake 是构建摄取域应用模块（设计稿 §3 六文件：builder/buildsource/
	// buildlog/gitmaterial/materials/registrycontent）。P5 wave-5 拆包形态
	//（ADR-0088 决策 2 P5）：P4 wave-2b 的裸嵌入已解开——应用模块独立成包。
	// engine 去应用模块 import 第七批（2026-11-10）：本字段只持
	// contract.Intake 接口，具体装配器经 Deps.IntakeFactory 由组合根注入
	//（construct.go 绑定端口袋）；核不再 import 构建摄取应用模块包。循环表
	// step 经 contract.StepOf(e.intake, "build")、gRPC Registry/Clone/git-head
	// 与 build 日志面经核门面委托（消费点零改）、drive 的 DriveBuilding 与
	// materialize/bluegreen/render/preview 的材料与卷钉住面、taskverbs 的
	// MaterialsForProcess、lifecycle 的 ResetOrphanBuilds 经接口委托；
	// builders/buildOpts/ghClient/builds 状态单一真源随应用模块（e.build
	// 选择子的二级提升面消解——transition.go 冻结边随 transitBuild 纯化同批
	// 消解，见 transition.go 注）；Build 四件一经 transitBuild 端口注入、终态
	// 副作用随应用模块（transitBuildRelease）；github/upload 分层豁免随应用
	// 模块平移（dep_matrix 豁免行 subtree 改写，ADR-0088 决策 8）。
	// buildLoop/Kick 挂载留核（KickTasks 同款）。
	intake contract.Intake

	// diagnosis 是诊断域应用模块（B2a W2 facts 装配器；规则层在 domain/services/
	// diagnosis）。P4 收官「半步驻核（P5 前专议）」于 P5 wave-6 裁决出包
	//（ADR-0088 决策 1 终态第 9 个应用模块，九个应用模块对账闭合 9/9）：应用模块独立成包
	//（诊断应用模块包）。engine 去应用模块 import 试点
	//（2026-11-10）：本字段只持 contract.Diagnosis 接口，具体装配器经
	// Deps.DiagnosisFactory 由组合根注入（construct.go 绑定端口袋）；核不再
	// import 诊断应用模块包。gRPC Diagnosis 面经核门面委托
	//（消费点零改）；观测导出面（ObservationSnapshot/WorkloadProcess）与告警
	// 行读（核门面 AlertRuleRepo——repo 持有权随 metrics 应用模块）绑定为
	// 端口，模块间零 import；无环无写面（纯读时装配器）。
	diagnosis contract.Diagnosis

	// 域状态（行为住各域文件，状态随域绑定——加环/加域不再横穿 Engine
	// 结构体；域类型定义随域文件，P3 收官批 8 壳收口归位。锁语义零漂移：
	// ensure 确保缓存族仍共用 ensureMu 单锁（ensure cache map 分住各域子结构/应用模块）；
	// 观测/漂移/期望/收敛各域锁照旧）。P4 wave-2a→P5 wave-4：manageds/
	// databases 两域经裸嵌入形态完成接收者下域后拆包迁出（ADR-0088 决策
	// 2 P5——循环表 step 经应用模块方法值、核消费经应用模块指针/门面零改）；
	// managedGens 留核（gensFor 与 ensureMu 锁的配对裁定，应用模块经端口
	// 读写——ManagedGen 类型随应用模块包）。P4 wave-2b→P5 wave-5：intake
	// 应用模块经裸嵌入形态完成接收者下域后拆包迁出（末模块）。
	task     tasks
	obs      observations
	drift    drifts
	expect   expects
	converge converges // ADR-0082 缺省收敛域（双通道 ensure cache/签名生命周期/冷却记账/缺席投影缓存）
	// managedGens 是 per-受管域 Generation 计数器表（留核：与 ensureMu
	// 锁配对——P3 批 5/P4 wave-2a 裁定维持，manageds 应用模块经 gensFor 端口
	// 惰性建读写；ManagedGen 类型随契约叶子 contract（engine 去应用模块
	// import 第三批），键 = ManagedNamespace().String()）。
	managedGens map[string]*contract.ManagedGen
	delivery    deploys
	proxyMemo   proxies

	// ensureMu 是收敛域签名短路确保缓存族的共用锁（五张 ensure cache map 分住各域
	// 子结构，锁保持单点——拆锁随域分化需要时再裁，本批锁语义零漂移）。
	ensureMu sync.Mutex

	// maintenanceMu 是载体网络维护互斥（ADR-0046 网络重建动词的串行化
	// 锚）：RebuildNetwork 持写锁贯穿 detach→rm→create→attach 全序；
	// 全部 Ensure 族调用点（materialize/databaseStep/managedStep/
	// driveEnsure/backup 环的 utility 附着）与 TeardownDatabase（拆载体
	// 是载体写动词，2026-10-05 级联批入册）持读锁——读锁间照旧并发，
	// 只有重建排他（受管 Proxy 单次 Ensure 引用全部活跃项目网络，全局
	// 粒度是对该耦合的诚实取舍，ADR-0046 决策 3）。锁等待不占步预算
	// （排队语义，见 lockMaintenance）。P4 wave-1b：backups 应用模块经
	// lockMaintenance 内核端口消费（gate 注入，锁语义零漂移）；
	// platformBackupMu 已随域迁入 backups 应用模块（ADR-0088 决策 4）。
	maintenanceMu sync.RWMutex

	// lifecycleMu 同步引擎生命周期（B12 P3-6）：Start 全程持锁（cancel
	// 判空/赋值 + wg.Add）与 Stop 全程持锁（cancel 置 nil + wg.Wait 排水）
	// 互斥——裸读写 cancel 是数据竞争，Add/Wait 重叠是 WaitGroup 竞争，
	// 并发双 Start 会双发全套收敛循环 goroutine。
	lifecycleMu sync.Mutex

	// runCtx 是 Start 物化的进程生命期 ctx（Q-7）：构建 goroutine 的派生
	// 根——Stop 取消它即有界排水（executeBuild 的优雅退出路径接管）。
	runCtxMu sync.Mutex
	runCtx   context.Context

	// nodeLeftSeen 是 node.left 去抖实例态（节点回归即清签名）。
	nodeLeftSeen map[string]bool

	// nodeAbsentSince 是 node.left 宽限窗计时（节点 ID → 首次缺失的引擎时
	// 刻；S11b）：连续缺失达 opts.Node.LeaveGrace 才执行离场清理，窗内回
	// 归即清条目（不删凭证/不断连/不发事件）。
	nodeAbsentSince map[string]time.Time

	// appLocks 是 App 级互斥（admission/基线重放/收口共享，N0.1 P1-3）：
	// 重放持有锁期间 Submit 排队——admission 落行与重放的 ActiveByApp
	// 复查被串行化，不存在"复查后落行、重放再 Ensure 旧 Generation 与
	// 驱动器对翻载体标签"的窗口（健康部署被 L1 误判回滚的根因）。
	//
	// 终态清条目裁决（E30）：不做。理由：①条目量级 = 进程生命期内曾被
	// 锁过的 App/Task 数（1 锁 ≈ 8B + sync.Map 槽位），趋势随受理速率缓
	// 增、无爆发面，v1 规模（N0 小团队）距关切量级几个数量级；②天真的
	// 终态删键不安全——他方 goroutine（API 动词/驱动环/观测路径并发）
	// 正持锁时删 key，新 lockTask 会铸出第二把锁，同 ID 两个临界区并行
	// = 互斥破环，比有界泄漏有害得多；正确的清退需要引头计数/single-
	// flight 复杂度，收益配不上风险。
	appLocks sync.Map // appID → *sync.Mutex

	// 构建面（F0.9/F1.14）：Builder 家族/构建上限/GitHub App 客户端与构建
	// 状态已随 intake 应用模块拆包迁出（P4 wave-2b 下域 → P5 wave-5 拆包，
	// application module 包注）；受管仓库与 Logging 面的核消费（drive 前置门/
	// componentrestart providers）留核副本。

	// Proxy 面（F0.15）：受管 Provider reconciler（Route 全量发布真源在
	// repos.Routes——读写同源，ADR-0080 目录化）。
	proxy capability.Proxy

	// routeReady 是 Route 全量发布就绪门（H7）：Start 在启动循环前置 false
	//（基线重放窗），重放返回后置 true 并即时发布一次；重放失败同样置
	// true（否则拉取端点永久 503，失败由既有 drift/受管循环自愈）。构造
	// 默认 true——未 Start 的手动驱动形态（测试/apitest 手动）行为不变。
	routeReady atomic.Bool

	// Registry 面（F1.11，ADR-0019 附录 B）：构建推送目标 + from_build
	// 引用组合 + 平台拉取凭证 + 受管 zot 自宿。
	registry capability.Registry

	// 材料面（F0.17/18，ADR-0014）：age 信封（Secret/Config/Volume repo
	// 在 repos——读写同源，ADR-0080 目录化；networks 同）。intake 应用模块
	// 持同名副本（P4 wave-2b，通则二 P5 过渡形态）。
	cipher *material.Cipher

	// confirmTickets 是 B1 Confirm Ticket 的消费面（nil = 强制确认引擎
	// 半边缺席——装配缺口由组合根兜底注入，旧测试直构形态按无票豁免；
	// 拦截器半边独立执法 E_CONFIRMATION_REQUIRED，见 confirm.go）。
	confirmTickets governance.ConfirmTicketStore
}

// Loop 暴露收敛循环（apitest 与装配层 Kick 用；只读面）。
func (e *Engine) Loop() *Loop { return e.loop }

// LoggingProvider 返回受管日志存储（nil = Logging 面停用；API 双径路由
// 与 build 日志回读消费，ADR-0040）。P5 wave-1 拆包形态 + engine 去应用
// 模块 import 第二批（2026-11-10）：真源在采集应用模块，本门面委托
// contract.Logging 接口；工厂缺席（手动测试装配）时 nil-safe 返回 nil，
// gRPC 装配面（assembly NewAPIDeps）消费点零改。
func (e *Engine) LoggingProvider() capability.Logging {
	if e.logpipes == nil {
		return nil
	}
	return e.logpipes.LoggingProvider()
}

// ---- execs 应用模块门面与类型别名（P5 wave-2 拆包形态 + engine 去应用模块
// import 第六批 2026-11-10：gRPC/assembly 消费面消费点零改——方法经
// contract.Exec 接口委托，类型/哨兵经别名原可达；真源在 engine/contract，
// 具体装配器在 exec 会话应用模块包内、由组合根注入） ----

// execs 域类型别名（fleetlygrpc 流适配器与 assembly WS 适配器的既名引用
// 零改；真源在 engine/contract）。
type (
	ExecSessionInfo = contract.ExecSessionInfo
	CreateExecInput = contract.CreateExecInput
	ExecClientFrame = contract.ExecClientFrame
	ExecServerFrame = contract.ExecServerFrame
	ExecMetaFrame   = contract.ExecMetaFrame
	ExecErrFrame    = contract.ExecErrFrame
	ExecClientPipe  = contract.ExecClientPipe
	RelayWriter     = contract.RelayWriter
	RelayStatus     = contract.RelayStatus
)

// execs 域哨兵别名（api/assembly 层 errors.Is 判定零改；真源在 engine/
// contract）。
var (
	ErrExecUnsupported       = contract.ErrExecUnsupported
	ErrExecNoInstance        = contract.ErrExecNoInstance
	ErrExecInstanceGone      = contract.ErrExecInstanceGone
	ErrExecNodeUnanchored    = contract.ErrExecNodeUnanchored
	ErrExecRelayOffline      = contract.ErrExecRelayOffline
	ErrExecTeamLimit         = contract.ErrExecTeamLimit
	ErrExecRelayUnauthorized = contract.ErrExecRelayUnauthorized
	ErrExecRelayStore        = contract.ErrExecRelayStore
)

// execs 应用模块门面方法（受理/路由/查询面——gRPC exec 服务、assembly
// gateway_exec 与 apitest 消费点零改；工厂缺席（手动测试装配）时门面
// nil-safe：受理族返回 ErrExecUnsupported，读族返回零值）。
func (e *Engine) ExecUnsupported() bool {
	if e.execs == nil {
		return true
	}
	return e.execs.ExecUnsupported()
}

func (e *Engine) CreateSession(ctx context.Context, in CreateExecInput) (*ExecSessionInfo, error) {
	if e.execs == nil {
		return nil, ErrExecUnsupported
	}
	return e.execs.CreateSession(ctx, in)
}

func (e *Engine) AbandonSession(id string) {
	if e.execs == nil {
		return
	}
	e.execs.AbandonSession(id)
}

func (e *Engine) AttachClient(ctx context.Context, sessionID string, pipe ExecClientPipe) error {
	if e.execs == nil {
		return ErrExecUnsupported
	}
	return e.execs.AttachClient(ctx, sessionID, pipe)
}

func (e *Engine) ExecInstances(ctx context.Context, appID, process string) ([]capability.ExecTargetInstance, error) {
	if e.execs == nil {
		return nil, ErrExecUnsupported
	}
	return e.execs.ExecInstances(ctx, appID, process)
}

func (e *Engine) NodePlatformIDByCarrier(ctx context.Context, carrierNodeID string) (string, error) {
	if e.execs == nil {
		return "", ErrExecUnsupported
	}
	return e.execs.NodePlatformIDByCarrier(ctx, carrierNodeID)
}

func (e *Engine) ExecSessionByID(id string) (*ExecSessionInfo, bool) {
	if e.execs == nil {
		return nil, false
	}
	return e.execs.ExecSessionByID(id)
}

func (e *Engine) ValidateRelayCredential(ctx context.Context, bearer string) error {
	if e.execs == nil {
		return ErrExecUnsupported
	}
	return e.execs.ValidateRelayCredential(ctx, bearer)
}

func (e *Engine) RelayAttach(ctx context.Context, token, carrierNodeID, relayVersion string, w RelayWriter) (feed func(capability.RelayFrame), detach func(), err error) {
	if e.execs == nil {
		return nil, nil, ErrExecUnsupported
	}
	return e.execs.RelayAttach(ctx, token, carrierNodeID, relayVersion, w)
}

func (e *Engine) DropRelay(platformID string) {
	if e.execs == nil {
		return
	}
	e.execs.DropRelay(platformID)
}

func (e *Engine) DropAllRelays() {
	if e.execs == nil {
		return
	}
	e.execs.DropAllRelays()
}

func (e *Engine) RelayStatuses() map[string]RelayStatus {
	if e.execs == nil {
		return nil
	}
	return e.execs.RelayStatuses()
}

// ---- metrics 应用模块门面与类型别名（P5 wave-2 拆包形态 + engine 去应用
// 模块 import 第五批 2026-11-10：gRPC 消费面与 converge/schemareg 消费点
// 零改——方法经 contract.Metrics 接口委托，类型/常量经别名原可达；真源在
// engine/contract，具体装配器在指标采集+告警应用模块包内、由组合根注入） ----

// SystemAlertState 别名（converge 缺省收敛循环的 SystemConvergeAlertState
// 与 gRPC ListAlertStates 组装消费零改；真源在 engine/contract）。
type SystemAlertState = contract.SystemAlertState

// alertEventPayload 别名（converge 系统告警事件的载荷形态零改）。
type alertEventPayload = contract.AlertEventPayload

// metrics 域事件载荷别名（schemareg 的事件载荷注册面零改）。
type (
	alertChannelFailedPayload = contract.AlertChannelFailedPayload
	crashLoopEventPayload     = contract.CrashLoopEventPayload
)

// 告警/crash-loop 事件名别名（schemareg 注册面与 converge 事件发射面
// 零改；eventcode 注册表同源——真源在 engine/contract）。
const (
	eventAlertFired       = contract.EventAlertFired
	eventAlertResolved    = contract.EventAlertResolved
	eventAlertChannelFail = contract.EventAlertChannelFail
	eventCrashLoopEntered = contract.EventCrashLoopEntered
	eventCrashLoopCleared = contract.EventCrashLoopCleared
)

// metrics 应用模块门面方法（gRPC Alerting/Diagnosis 面、capability 核内消费
// （componentrestart providers/managed 绑定）与 diagnose 域 repo 读面——
// 消费点零改；工厂缺席（手动测试装配）时门面 nil-safe）。
func (e *Engine) MetricsProvider() capability.Metrics {
	if e.metrics == nil {
		return nil
	}
	return e.metrics.MetricsProvider()
}

func (e *Engine) AlertRuleRepo() alertrule.Repo {
	if e.metrics == nil {
		return nil
	}
	return e.metrics.AlertRuleRepo()
}

func (e *Engine) NotificationChannelRepo() channel.Repo {
	if e.metrics == nil {
		return nil
	}
	return e.metrics.NotificationChannelRepo()
}

func (e *Engine) SystemOffsiteAlertState() SystemAlertState {
	if e.metrics == nil {
		return SystemAlertState{}
	}
	return e.metrics.SystemOffsiteAlertState()
}

func (e *Engine) TestNotificationChannel(ctx context.Context, channelID string) error {
	if e.metrics == nil {
		return fmt.Errorf("engine: metrics module is not configured")
	}
	return e.metrics.TestNotificationChannel(ctx, channelID)
}

// ---- backups 应用模块门面与类型别名（P5 wave-3 拆包形态 + engine 去应用
// 模块 import 第八批 2026-11-10：gRPC/assembly/lifecycle 消费面消费点零改
// ——方法经 contract.Backup 接口委托，类型/哨兵经别名原可达；真源在
// engine/contract，具体装配器在 backups 应用模块包内、由组合根注入） ----

// backups 域类型别名（assembly 的 Options 构造面与 gRPC platform 组装
// 面的既名引用零改；真源在 engine/contract）。
type (
	PlatformBackupConfig = contract.PlatformBackupConfig
	S3RepoConfig         = contract.S3RepoConfig
	PlatformSnapshot     = contract.PlatformSnapshot
)

// ErrPlatformBackupInProgress backups 域哨兵别名（api 层 errors.Is 判定零改；
// 真源在契约叶子）。
var ErrPlatformBackupInProgress = contract.ErrPlatformBackupInProgress

// VerifyBackup backups 应用模块门面方法（gRPC Backup 面（structure/databases）与 platform
// 面（platform.go 的 Trigger/List 组装）——消费点零改；工厂缺席时 nil-safe）。
func (e *Engine) VerifyBackup(ctx context.Context, backupID string) (bool, string, error) {
	if e.backups == nil {
		return false, "", errBackupUnconfigured
	}
	return e.backups.VerifyBackup(ctx, backupID)
}

func (e *Engine) DownloadBackup(ctx context.Context, databaseID, backupID string) (*backup.Backup, io.ReadCloser, error) {
	if e.backups == nil {
		return nil, nil, errBackupUnconfigured
	}
	return e.backups.DownloadBackup(ctx, databaseID, backupID)
}

func (e *Engine) TriggerPlatformBackup(ctx context.Context) (*PlatformSnapshot, error) {
	if e.backups == nil {
		return nil, errBackupUnconfigured
	}
	return e.backups.TriggerPlatformBackup(ctx)
}

func (e *Engine) ListPlatformSnapshots(ctx context.Context) ([]PlatformSnapshot, error) {
	if e.backups == nil {
		return nil, errBackupUnconfigured
	}
	return e.backups.ListPlatformSnapshots(ctx)
}

// KickBackups 唤醒 Backup 环（API 手动触发面消费）。环与 Kick 挂载留核
// （KickTasks/KickSchedules 同款；应用模块不持 Loop 面）。
func (e *Engine) KickBackups() { e.backupLoop.Kick() }

// ---- browses 应用模块门面与类型别名（P5 wave-3 拆包形态，通则③；engine
// 去应用模块 import 第四批，2026-11-10——值类型/常量/哨兵随契约叶子） ----

// browses 域类型别名（gRPC Browse 受理面与 assembly gateway/Options 构造
// 面的既名引用零改；真源在 engine/contract 契约叶子）。
type (
	BrowseConfig      = contract.BrowseConfig
	BrowseInput       = contract.BrowseInput
	BrowseSessionInfo = contract.BrowseSessionInfo
)

// browses 域哨兵别名（api 层 errors.Is 判定零改；真源在契约叶子）。
var (
	ErrBrowseQuota           = contract.ErrBrowseQuota
	ErrBrowseSessionNotFound = contract.ErrBrowseSessionNotFound
)

// browses 域常量别名（api 受理面的 quota 文案与台账行 expires_at 铸造
// 零改；真源在契约叶子）。
const (
	BrowseMaxSessionsPerTeam = contract.BrowseMaxSessionsPerTeam
	BrowseHardTTL            = contract.BrowseHardTTL
)

// browses 应用模块门面方法（gRPC Browse 受理面与 assembly gateway_browse 的
// cookie 校验面——消费点零改；工厂缺席时 nil-safe）。
func (e *Engine) BrowseConfigured() bool {
	if e.browses == nil {
		return false
	}
	return e.browses.BrowseConfigured()
}

func (e *Engine) BrowseCheckQuota(teamID string) error {
	if e.browses == nil {
		return nil
	}
	return e.browses.BrowseCheckQuota(teamID)
}

func (e *Engine) RegisterBrowseSession(in BrowseInput) (BrowseSessionInfo, error) {
	if e.browses == nil {
		return BrowseSessionInfo{}, fmt.Errorf("engine: browse module is not configured")
	}
	return e.browses.RegisterBrowseSession(in)
}

func (e *Engine) BrowseEntryURL(sessionID string) string {
	if e.browses == nil {
		return ""
	}
	return e.browses.BrowseEntryURL(sessionID)
}

func (e *Engine) BrowseSessionGrant(sessionID string) (string, int, bool) {
	if e.browses == nil {
		return "", 0, false
	}
	return e.browses.BrowseSessionGrant(sessionID)
}

func (e *Engine) BrowseValidateGrant(sessionID, grant string) bool {
	if e.browses == nil {
		return false
	}
	return e.browses.BrowseValidateGrant(sessionID, grant)
}

// KickBrowse 唤醒 browse 收敛循环（api 受理面消费）。环与 Kick 挂载留核
// （KickTasks 同款；应用模块不持 Loop 面）。
func (e *Engine) KickBrowse() { e.browseLoop.Kick() }

// ---- dataservices 应用模块门面与声明委托（P5 wave-4 拆包形态通则③，B2e 随
// 包名归一改道；engine 去应用模块 import 第九批，2026-11-10：真源在
// engine/contract，具体装配器由组合根经 Deps.DataServiceFactory 注入——
// gRPC/assembly 消费面消费点零改：方法经核门面委托，哨兵经 var 别名、公式
// 经 FuncDecl 声明委托原可达（resolve.go 铸名两函数同款，语义零漂移）） ----

// 数据服务族共享值类型别名（gRPC/assembly 既名引用零改；定义真源在
// engine/contract/dataservice.go）。
type (
	ResourceUpdate           = contract.ResourceUpdate
	UpdateRequest            = contract.UpdateRequest
	UpdateResult             = contract.UpdateResult
	PubSubUpdateRequest      = contract.PubSubUpdateRequest
	BucketStoreUpdateRequest = contract.BucketStoreUpdateRequest
	TopicLive                = contract.TopicLive
	StreamLive               = contract.StreamLive
	BucketLive               = contract.BucketLive
)

// database 类型哨兵别名（api 层 errors.Is 判定零改；真源在 engine/contract）。
var ErrRotateRejected = contract.ErrRotateRejected

// IsDatabaseCredentialSecret 声明委托（gRPC PutSecret 受理契约测试的既名引用
// 零改；真源在 engine/contract）。
func IsDatabaseCredentialSecret(name string) bool {
	return contract.IsDatabaseCredentialSecret(name)
}

// DatabaseConnectionURL 声明委托（gRPC 创建面的既名引用零改；真源在
// engine/contract）。
func DatabaseConnectionURL(engineName, version, databaseID, password string) (string, error) {
	return contract.DatabaseConnectionURL(engineName, version, databaseID, password)
}

// dataservices 应用模块门面方法（gRPC Database 受理/轮换面——消费点零改；
// 工厂缺席时门面 nil-safe 返回精确失败）。
func (e *Engine) TeardownDatabase(ctx context.Context, id string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.TeardownDatabase(ctx, id)
}

func (e *Engine) RotateDatabasePassword(ctx context.Context, databaseID string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.RotateDatabasePassword(ctx, databaseID)
}

// UpdateDatabase 声明委托（B2d，ADR-0095 决策 5：gRPC Update 受理面的执行
// 真源在 dataservices 应用模块 update.go——双车道分流 + utilityMu 单写边界）。
func (e *Engine) UpdateDatabase(ctx context.Context, databaseID string, req UpdateRequest) (UpdateResult, error) {
	if e.dataservices == nil {
		return UpdateResult{}, errDataServiceUnconfigured
	}
	return e.dataservices.UpdateDatabase(ctx, databaseID, req)
}

// ErrSettingsApplyRejected 声明委托（api 层 errors.Is 判定；真源在
// engine/contract——E_SETTINGS_APPLY_FAILED 的哨兵映射锚）。
var ErrSettingsApplyRejected = contract.ErrSettingsApplyRejected

// CheckVolumeOrigin 是 Create* 受理面的卷异源前置读（ADR-0097 决策 4：同名
// 重建形态下读运行侧卷的实例标记——在场即异源 E_CONFLICT；ensure 链标记
// 门是权威执法位，本前置读给受理面同步精确拒绝）。B4c/B5 实体中性化：
// kind 词面分派（dataservicetemplate.KindDatabase / KindPubSub /
// KindBucketStore），一枚门面三种数据服务类型通吃（核 FuncDecl 零增长——门面纯
// 委托，分派住应用模块 origin.go）。
func (e *Engine) CheckVolumeOrigin(ctx context.Context, kind, projectID, name, engineName string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.CheckVolumeOriginForCreate(ctx, kind, projectID, name, engineName)
}

// ErrVolumeForeignOrigin 声明委托（api 层 errors.Is 判定；真源在
// engine/contract）。
var ErrVolumeForeignOrigin = contract.ErrVolumeForeignOrigin

// KickDatabases 唤醒 database 类型收敛循环（API 受理面消费：创建/删除后立即
// 驱动）。环与 Kick 挂载留核（KickTasks 同款；应用模块不持 Loop 面）；kick 面
// 分类型——pubsub/bucketstore 的 Kick 随 B4b/B5 各自环落（ADR-0096 决策 1）。
func (e *Engine) KickDatabases() { e.databaseLoop.Kick() }

// KickPubSubs 唤醒 pubsub 类型收敛循环（B4b；API 受理面消费：创建/删除/轮换/
// Update 后立即驱动——kick 面分类型）。
func (e *Engine) KickPubSubs() { e.pubsubLoop.Kick() }

// ---- pubsub 类型门面与声明委托（B4b，ADR-0096 决策 1：database 类型同款
// 形态——gRPC 消费面经核门面委托，真源在 dataservices 应用模块 pubsub.go） ----

// IsPubSubCredentialSecret 声明委托（gRPC PutSecret 受理契约测试）。
func IsPubSubCredentialSecret(name string) bool {
	return contract.IsPubSubCredentialSecret(name)
}

// PubSubConnMaterial 声明委托（gRPC 创建面——B6a 起双方言：nats URL 形/
// kafka k=v 串，ConnMaterial 结构面单源）。
func PubSubConnMaterial(engineName, version, pubsubID, password string) (string, error) {
	return contract.PubSubConnMaterial(engineName, version, pubsubID, password)
}

func (e *Engine) TeardownPubSub(ctx context.Context, id string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.TeardownPubSub(ctx, id)
}

func (e *Engine) RotatePubSubPassword(ctx context.Context, pubsubID string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.RotatePubSubPassword(ctx, pubsubID)
}

func (e *Engine) UpdatePubSub(ctx context.Context, pubsubID string, req PubSubUpdateRequest) (UpdateResult, error) {
	if e.dataservices == nil {
		return UpdateResult{}, errDataServiceUnconfigured
	}
	return e.dataservices.UpdatePubSub(ctx, pubsubID, req)
}

// KickBucketStores 唤醒 bucketstore 类型收敛循环（B5；API 受理面消费：创建/
// 删除/轮换/Update 后立即驱动——kick 面分类型）。
func (e *Engine) KickBucketStores() { e.bucketstoreLoop.Kick() }

// ---- bucketstore 类型门面与声明委托（B5，ADR-0096 决策 1：pubsub 类型同款
// 形态——gRPC 消费面经核门面委托，真源在 dataservices 应用模块 bucketstore.go） ----

// IsBucketStoreCredentialSecret 声明委托（gRPC PutSecret 受理契约测试）。
func IsBucketStoreCredentialSecret(name string) bool {
	return contract.IsBucketStoreCredentialSecret(name)
}

// BucketStoreConnMaterial 声明委托（gRPC 创建面）。
func BucketStoreConnMaterial(engineName, version, bucketStoreID, secretKey string) (string, error) {
	return contract.BucketStoreConnMaterial(engineName, version, bucketStoreID, secretKey)
}

func (e *Engine) TeardownBucketStore(ctx context.Context, id string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.TeardownBucketStore(ctx, id)
}

func (e *Engine) RotateBucketStorePassword(ctx context.Context, bucketStoreID string) error {
	if e.dataservices == nil {
		return errDataServiceUnconfigured
	}
	return e.dataservices.RotateBucketStorePassword(ctx, bucketStoreID)
}

func (e *Engine) UpdateBucketStore(ctx context.Context, bucketStoreID string, req BucketStoreUpdateRequest) (UpdateResult, error) {
	if e.dataservices == nil {
		return UpdateResult{}, errDataServiceUnconfigured
	}
	return e.dataservices.UpdateBucketStore(ctx, bucketStoreID, req)
}

// ---- 子资源管理面门面（B8，ADR-0096 决策 10 / design §4.7：删除披露的
// 实况探测——PreviewDelete* 受理面消费，真源在 dataservices 应用模块
// subresources.go；收敛段经分类型步尾段内联，不经门面） ----

// TopicLiveInspect 探测单 Topic 实况（kafka 协议族 admin 面）。
func (e *Engine) TopicLiveInspect(ctx context.Context, pubsubID, topicID string) (TopicLive, error) {
	if e.dataservices == nil {
		return TopicLive{}, errDataServiceUnconfigured
	}
	return e.dataservices.TopicLiveInspect(ctx, pubsubID, topicID)
}

// StreamLiveInspect 探测单 Stream 实况（nats admin 面；state 面承载删除
// 披露的数据量行）。
func (e *Engine) StreamLiveInspect(ctx context.Context, pubsubID, streamID string) (StreamLive, error) {
	if e.dataservices == nil {
		return StreamLive{}, errDataServiceUnconfigured
	}
	return e.dataservices.StreamLiveInspect(ctx, pubsubID, streamID)
}

// BucketLiveInspect 探测单 Bucket 实况（mcli admin 面；du 数据量随探测）。
func (e *Engine) BucketLiveInspect(ctx context.Context, bucketStoreID, bucketID string) (BucketLive, error) {
	if e.dataservices == nil {
		return BucketLive{}, errDataServiceUnconfigured
	}
	return e.dataservices.BucketLiveInspect(ctx, bucketStoreID, bucketID)
}

// ---- intake 应用模块门面、哨兵别名与声明委托（P5 wave-5 拆包形态，通则③：
// gRPC/assembly 消费面消费点零改——方法经 contract.Intake 接口委托，哨兵
// 经 var 别名、受理契约测试经 FuncDecl 声明委托原可达（databases 声明委托
// 同款，语义零漂移）；engine 去应用模块 import 第七批 2026-11-10：真源在
// engine/contract，具体装配器在构建摄取应用模块包内、由组合根注入） ----

// intake 域哨兵别名（api 层 errors.Is 判定零改；真源在 engine/contract）。
var (
	ErrInvalidRegistryRepository = contract.ErrInvalidRegistryRepository
	ErrRegistryUnavailable       = contract.ErrRegistryUnavailable
)

// ValidateRegistryRepository 声明委托（gRPC ListRegistryTags 受理契约测试的
// 既名引用零改；真源在 engine/contract）。
func ValidateRegistryRepository(projectID, repository string) error {
	return contract.ValidateRegistryRepository(projectID, repository)
}

// intake 应用模块门面方法（gRPC Registry 面（catalog/tags）、webhook 的
// git-head/clone 拉取通道与 delivery 的 build 日志 follow 面——消费点
// 零改；工厂缺席（手动测试装配）时门面 nil-safe：读族返回零值，受理族
// 返回精确的未装配失败）。
func (e *Engine) RegistryCatalog(ctx context.Context, projectID string) ([]string, error) {
	if e.intake == nil {
		return nil, errIntakeUnconfigured
	}
	return e.intake.RegistryCatalog(ctx, projectID)
}

func (e *Engine) RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error) {
	if e.intake == nil {
		return nil, errIntakeUnconfigured
	}
	return e.intake.RegistryTags(ctx, projectID, repository)
}

func (e *Engine) ResolveGitHead(ctx context.Context, projectID, repoURL, branch string) (string, error) {
	if e.intake == nil {
		return "", errIntakeUnconfigured
	}
	return e.intake.ResolveGitHead(ctx, projectID, repoURL, branch)
}

func (e *Engine) CloneAt(ctx context.Context, projectID, repoURL, ref, dir string) error {
	if e.intake == nil {
		return errIntakeUnconfigured
	}
	return e.intake.CloneAt(ctx, projectID, repoURL, ref, dir)
}

func (e *Engine) RecentBuildLogs(buildID string) []capability.LogFrame {
	if e.intake == nil {
		return nil
	}
	return e.intake.RecentBuildLogs(buildID)
}

func (e *Engine) RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64) {
	if e.intake == nil {
		return nil, 0
	}
	return e.intake.RecentBuildLogsAfter(buildID, after)
}

// ---- diagnosis 应用模块门面与常量别名（P5 wave-6 拆包形态 + engine 去
// 应用模块 import 试点 2026-11-10：gRPC 消费面与 engine 白盒测试消费点零改
// ——方法经 contract.Diagnosis 接口委托，常量/类型经别名原可达；真源在
// domain/services/diagnosis，具体装配器在诊断应用模块包内、由组合根注入） ----

// DiagnoseWindow 常量别名（engine 白盒测试的既名引用零改；单源在
// contract.DiagnoseWindow——P5 wave-2 随 metrics 下沉，应用模块侧同锚）。
const DiagnoseWindow = contract.DiagnoseWindow

// diagnosis 应用模块门面方法（gRPC Diagnosis 面与「需要你」页先行筛——
// 消费点零改；字段为 contract.Diagnosis 接口，手动测试装配缺席时 nil-safe
// 返回零 Facts/false）。
func (e *Engine) DiagnoseFacts(ctx context.Context, appID string) diagnosis.Facts {
	if e.diagnosis == nil {
		return diagnosis.Facts{}
	}
	return e.diagnosis.Facts(ctx, appID)
}

func (e *Engine) DiagnoseAppCheapHit(appID string) bool {
	if e.diagnosis == nil {
		return false
	}
	return e.diagnosis.CheapHit(appID)
}

// ---- manageds 应用模块门面（P5 wave-4 拆包形态，通则③） ----

// KickManagedLoop 触发一次即时受管收敛（API 写路径在 Project 创建/删除后
// Kick；测试直调）。zot per-Project 材料随活跃集再生成（ADR-0036 N2 兑现
// 节 2）——Kick 把项目变更的滚动窗从"下一节拍"缩到即时（staging 实录：
// 新项目首构建可先于节拍到达，推送 401 一次失败）。环与 Kick 挂载留核
// （KickTasks 同款；应用模块不持 Loop 面）。
func (e *Engine) KickManagedLoop() { e.managedLoop.Kick() }

// KickTasks 唤醒 Task 收敛循环（API 受理面消费：创建/缩放/停止后立即驱动）。
func (e *Engine) KickTasks() { e.taskLoop.Kick() }

// KickSchedules 唤醒 Schedule 到期拍环（API 受理面消费：创建后立即判定）。
func (e *Engine) KickSchedules() { e.scheduleLoop.Kick() }

// DriveOnce 手动驱动一轮收敛（循环表序逐步——schedule 先于 task：同一轮
// Drive 内铸出的 Task 即刻进补足链；apitest 手动形态消费，golden 确定性
// 不依赖真实节拍）。
func (e *Engine) DriveOnce(ctx context.Context) {
	for _, r := range e.rings {
		r.step(ctx)
	}
}

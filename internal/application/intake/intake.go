// Package intake 是构建摄取域卫星（设计稿 §3 六文件：builder/buildsource/
// buildlog/gitmaterial/materials/registrycontent——git 材料解析/上传解包/
// 构建循环/Registry 内容只读代理）。P3 批 6 收编 engine → P4 wave-2b 接收者
// 下域（嵌入形态）→ P5 wave-5 拆包（ADR-0088 决策 2 P5 形态，2026-11-01）：
// 嵌入已解开——卫星独立成包（internal/application/intake），核经构造注入
// 持有（engine construct.go 装配卫星 New）；环表 step 经卫星方法值
// （e.intake.Step）、gRPC Registry/Clone/git-head/build 日志面经核门面委托、
// drive 的 DriveBuilding 与 materialize/bluegreen/render/preview 的材料与
// 卷钉住面、taskverbs 的 MaterialsForProcess、lifecycle 的 ResetOrphanBuilds
// 经卫星指针（消费点改道见各文件）；github/upload 分层豁免随本包平移
// （dep_matrix 豁免行 subtree 改写，条数不增——ADR-0088 决策 8 平移条款）。
// 机制契约共享面（写路径类型/事件铸名）在 internal/application/engine/contract。
// 禁回引 *engine.Engine（enginebackref 跨包执法）。
package intake

import (
	"context"
	"log/slog"
	"sync"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/github"
)

// builds 是构建面状态（输入登记 + 最近日志缓冲）。P3 批 6 收编：
// buildDomain → builds（复数普通名词零后缀，ADR-0075 命名法则）；P3 收官
// 批 8 收编：类型体自 engine.go 归位 builder.go（壳收口）。P4 wave-2b 起
// 作为卫星嵌套状态 struct 逐字保留（名与字段零改）；P5 wave-5 拆包后包内
// 私有（核侧 e.build 选择子的二级提升面消解——他包不可直达，经方法面
// 消费）。
type builds struct {
	inputsMu sync.Mutex
	inputs   map[string]capability.BuildRequest // buildID → 登记输入（重启丢失即回 queued 重放）
	logs     *logBuffer
}

// 构建循环参数（引擎 Options 派生快照；BuildConcurrency 上限可配，F0.9）。
// P3 收官批 8 更名：buildOptions → buildLimits（剥与导出切片类型
// BuildOptions 的近撞名）；字段名保持 buildOpts。
type buildLimits struct {
	Concurrency int           // 并发构建上限（默认 2）
	Timeout     time.Duration // 单次构建硬超时（默认 15m；超时=expired）
}

// Intake 是构建摄取域卫星实例态。P4 wave-2b 接收者下域（域内短名 in）→
// P5 wave-5 拆包导出（intake → Intake，平级包通则①）；域自有事实
// （builders/buildOpts/ghClient——单一真源随域）与状态（build.builds 嵌套
// 保留，包内私有）随卫星持有；依赖显式逐字段 + 机制端口注入（engine
// construct.go 装配）。禁挂 *Engine 回引（enginebackref 守卫执法）。
type Intake struct {
	build builds // 既有构建面状态（逐字保留，包内私有——经方法面消费）

	// 域自有事实（自 Engine 迁入，单一真源随域）：builders 是 Builder 家族
	//（名→Provider，spec 路由；lifecycle 的 ResetOrphanBuilds 与本域消费）；
	// buildOpts 是并发/超时上限（Options 派生快照）；ghClient 是 GitHub App
	// 最小客户端（installation token 现铸，gitmaterial 消费——
	// infrastructure/github 分层豁免随卫星平移，ADR-0088 决策 8）。
	builders  map[string]capability.Builder
	buildOpts buildLimits
	ghClient  *github.Client

	// 显式依赖（实测消费面，逐字段注入）：registry 是受管仓库端点/内容面
	// 与 per-Project 凭证面（核副本双持——drive 的前置门与 componentrestart
	// 的 providers 消费留核，P5 过渡形态通则二）；logging 是 build 日志持久
	// 化面（核副本双持，同通则二）；cipher 是 Secret/Git 材料解封（核副本
	// 双持——resolve.go 凭证解析族留核）；runtime 是卷钉住的首节点探测
	//（DescribeCluster 读动词，非载体写——maintenanceguard 不执法读半边）；
	// repos 是行读写聚合目录；tx 是 Build 行铸造事务入口；log 是域内日志器。
	registry capability.Registry
	logging  capability.Logging
	cipher   *material.Cipher
	runtime  capability.Runtime
	repos    transact.Stores
	tx       transact.Tx
	log      *slog.Logger

	// dataRoot 是数据根配置快照端口（通则五标准子形态：闭包逐拍读核
	// opts——测试经 opts 写驱动上下文目录行为不冻结）。
	dataRoot func() string

	// 机制端口（ADR-0088 决策 3 / 设计稿 §5，函数字段、名对齐核方法）：
	// rootCtx 是构建 goroutine 派生根（六条例外边之一——lifecycle 派生根
	// 机制，logpipes/metrics 同款端口）；spawn 是 wg Add/Done 配对排水
	//（单源留核，卫星结构性无法破坏排水纪律）；transitBuild/transit-
	// AndReload/commitWrite 是 writer 端口（Build 四件一与部署状态机受控
	// 暴露，前缀判据零漂移）；failDeployment/loadSpec 是受理线例外边的
	// 端口归位（设计稿 §4 六条之二，真源留核）；eventTeamOfProject 是
	// emitter 端口（事实 Team 盖戳）；projectTeam 与 registry 端点四函数
	//（registryEndpoint/registryProjectEndpoints/registryEndpointFor/
	// managedEndpoint）是 facts 端口（解析面六族住核 resolve.go，
	// resolutionface 锚不动——wave-5 维持留核+卫星消费经 facts 端口）；
	// kickBuild 是拾取唤醒（Loop 挂载留核）。
	rootCtx                  func() context.Context
	spawn                    func(fn func())
	transitBuild             func(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error)
	transitAndReload         func(ctx context.Context, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) (*deployment.Deployment, error)
	failDeployment           func(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error)
	loadSpec                 func(ctx context.Context, revID string) (*specv1.AppSpec, error)
	commitWrite              contract.CommitWrite // 签名单源（P5 wave-3 下沉 engine/contract）
	eventTeamOfProject       func(ctx context.Context, s transact.Stores, projectID string) string
	projectTeam              func(ctx context.Context, projectID string) (string, error)
	registryEndpoint         func(ctx context.Context) (capability.RegistryEndpoint, error)
	registryProjectEndpoints func() capability.ProjectEndpoints
	registryEndpointFor      func(ctx context.Context, projectID string) (capability.RegistryEndpoint, error)
	managedEndpoint          func(ctx context.Context) (capability.RegistryEndpoint, error)
	kickBuild                func()
}

// Deps 是卫星依赖与机制端口的注入面（组合根 = engine construct.go 装配，
// ADR-0088 决策 2 P5 形态：嵌入已解开，核经构造注入持有卫星；依赖显式
// 逐字段注入 + 机制端口绑定为核方法值/闭包）。
type Deps struct {
	Builders map[string]capability.Builder
	Registry capability.Registry
	Logging  capability.Logging
	Cipher   *material.Cipher
	Runtime  capability.Runtime
	Repos    transact.Stores
	Tx       transact.Tx
	Log      *slog.Logger
	// BuildConcurrency/BuildTimeout 是构建循环上限（引擎 Options 派生快照）。
	BuildConcurrency int
	BuildTimeout     time.Duration
	// DataRoot 是数据根配置快照端口（闭包逐拍读核 opts，通则五）。
	DataRoot func() string

	// 机制端口（名对齐核方法，engine construct.go 绑定核方法值/闭包）。
	RootCtx                  func() context.Context
	Spawn                    func(fn func())
	TransitBuild             func(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error)
	TransitAndReload         func(ctx context.Context, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) (*deployment.Deployment, error)
	FailDeployment           func(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error)
	LoadSpec                 func(ctx context.Context, revID string) (*specv1.AppSpec, error)
	CommitWrite              contract.CommitWrite
	EventTeamOfProject       func(ctx context.Context, s transact.Stores, projectID string) string
	ProjectTeam              func(ctx context.Context, projectID string) (string, error)
	RegistryEndpoint         func(ctx context.Context) (capability.RegistryEndpoint, error)
	RegistryProjectEndpoints func() capability.ProjectEndpoints
	RegistryEndpointFor      func(ctx context.Context, projectID string) (capability.RegistryEndpoint, error)
	ManagedEndpoint          func(ctx context.Context) (capability.RegistryEndpoint, error)
	KickBuild                func()
}

// New 构造卫星（不启动；环挂载与 Kick 留核）。日志缓冲容量 500 与 P4
// wave-2b 前的核内构造形态逐字一致（行为零漂移）。
func New(deps Deps) *Intake {
	return &Intake{
		build: builds{
			logs:   newLogBuffer(500),
			inputs: make(map[string]capability.BuildRequest),
		},
		builders:                 deps.Builders,
		buildOpts:                buildLimits{Concurrency: deps.BuildConcurrency, Timeout: deps.BuildTimeout},
		ghClient:                 github.NewClient(nil, nil),
		registry:                 deps.Registry,
		logging:                  deps.Logging,
		cipher:                   deps.Cipher,
		runtime:                  deps.Runtime,
		repos:                    deps.Repos,
		tx:                       deps.Tx,
		log:                      deps.Log,
		dataRoot:                 deps.DataRoot,
		rootCtx:                  deps.RootCtx,
		spawn:                    deps.Spawn,
		transitBuild:             deps.TransitBuild,
		transitAndReload:         deps.TransitAndReload,
		failDeployment:           deps.FailDeployment,
		loadSpec:                 deps.LoadSpec,
		commitWrite:              deps.CommitWrite,
		eventTeamOfProject:       deps.EventTeamOfProject,
		projectTeam:              deps.ProjectTeam,
		registryEndpoint:         deps.RegistryEndpoint,
		registryProjectEndpoints: deps.RegistryProjectEndpoints,
		registryEndpointFor:      deps.RegistryEndpointFor,
		managedEndpoint:          deps.ManagedEndpoint,
		kickBuild:                deps.KickBuild,
	}
}

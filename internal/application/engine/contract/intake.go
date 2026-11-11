// intake.go 是构建摄取域应用模块的核/模块契约面（engine 去应用模块 import
// 第七批，2026-11-10）：engine 核只持 Intake 接口与构造端口袋，具体装配器
// 由组合根经 engine.Deps.IntakeFactory 注入——核不再 import 构建摄取应用
// 模块包。共享纯助手/哨兵随契约面下沉（gRPC Registry 受理面经 engine 别名/
// 声明委托零改）；端口形态全部落在 domain 面（capability/build/deployment/
// material 签名），契约叶子保持零应用模块类型（ADR-0088 决策 7 裁定二）。
package contract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/githubapp"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// registryRepoSegmentRe / registryRepoNameRe 是平台仓名双段白名单
// （R03-M13）：项目段 = lower(projectID)，形态对齐 entityid
// （`^[a-z0-9]+(-[a-z0-9]+)+$`：项目 ID 是 `<name>-<seq>`，如 `regcat-1`，
// 不能只收 `[a-z0-9]+`；此处用等价的"段间单连字符"形态）；仓名段单段
// [a-z0-9._-]+。大小写混杂一律拒绝（与 registry 双段小写纪律一致）；
// `..`/`.` 段单列拒绝（白名单字符集本身允许点号，但 dot-segment 是路径
// 归一面的注入入口）。归属判定仍由 BuildRepo 等值比较兜底——白名单只
// 负责词法安全，不替代归属。
var (
	registryRepoSegmentRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	registryRepoNameRe    = regexp.MustCompile(`^[a-z0-9._-]+$`)
)

// ErrInvalidRegistryRepository 是 Registry 读代理的仓名形态非法哨兵
// （R03-M13）：受理面映射 E_INVALID_ARGUMENT（400），与"形态合法但越纲"
// 的 404（state.ErrNotFound）分层——避免把用户输入错误伪装成运行期 500。
var ErrInvalidRegistryRepository = errors.New("engine: repository is not a valid platform repository name")

// ErrRegistryUnavailable 是 Registry 读代理的"面缺席"哨兵族（P0-7.2）：
// Registry 未装配（registry.addr 未配）或 provider 无 content 扩展接口——配置
// 级显式 off 态而非内部故障，经 API 映射 E_REGISTRY_UNAVAILABLE（503 档，
// E_BROWSE_DISABLED"显式 off 态"同款先例；此前裸 fmt.Errorf 落 E_INTERNAL
// 500，把配置事实伪装成内部故障）。
var ErrRegistryUnavailable = errors.New("the managed registry read proxy is unavailable")

// BuildRepo 组装构建产物所在的仓名（不含 registry 地址；<projectID>/<appID>
// 双段小写——docker 引用要求仓库名小写，ULID 大写形态无损小写化）。契约叶子
// 是本地镜像命名的唯一真源（勿散落副本）；前纲布局是 per-Project 凭证域
// 隔离的地基（ADR-0036 N2 兑现节 2）。
func BuildRepo(projectID, appID string) string {
	return strings.ToLower(projectID) + "/" + strings.ToLower(appID)
}

// LocalImageDigestRef 组装 from_build 的下发引用（digest 形态：绕开 tag
// 语义的全部 docker29 真机坑——digest-pull 不落 tag、save/load 丢 tag）。
// repo 是产物所在仓名（Build 行创建时定型的 BuildRepo 前纲，恒非空——
// 旧扁平兼容面已删，digest 引用一律走 repo 前纲）。
func LocalImageDigestRef(registryAddr, repo, digest string) string {
	return fmt.Sprintf("%s/%s@%s", registryAddr, repo, digest)
}

// ValidateRegistryRepository 把 API 传入的 repository 收敛为平台仓名：
// 恰两段、全小写白名单、无 `.`/`..` 段、无 `%`/`?`/`#`/空白/多级路径。
// 形态非法 = ErrInvalidRegistryRepository（受理面 E_INVALID_ARGUMENT；
// 用户输入错不是运行期故障）；形态合法但不属于该项目 = state.ErrNotFound
// （跨项目探测不可达，不泄漏存在性）。
func ValidateRegistryRepository(projectID, repository string) error {
	segments := strings.Split(repository, "/")
	if len(segments) != 2 ||
		!registryRepoSegmentRe.MatchString(segments[0]) ||
		!registryRepoNameRe.MatchString(segments[1]) ||
		segments[1] == "." || segments[1] == ".." {
		return ErrInvalidRegistryRepository
	}
	if segments[0]+"/" != BuildRepo(projectID, "") {
		return state.ErrNotFound
	}
	return nil
}

// Intake 是构建摄取域应用模块的核消费面：循环表经 Module.Loops 取 build
// 步，drive 的 building 态、materialize/bluegreen/render/preview 的材料与
// 卷钉住面、taskverbs 的 MaterialsForProcess、gRPC Registry/Clone/git-head
// 与 build 日志面、lifecycle 的 ResetOrphanBuilds 经本接口委托（消费点
// 零改）。
type Intake interface {
	Module
	// DriveBuilding 是 Deployment 的 building 态驱动（Revision 级构建一次、
	// digest 复用；无构建则直通 releasing）。
	DriveBuilding(ctx context.Context, d *deployment.Deployment) (*deployment.Deployment, error)
	// ResolveMaterials 解析 AppSpec 的 Secret/Config/镜像凭证材料。
	ResolveMaterials(ctx context.Context, spec *specv1.AppSpec, projectID string) (capability.Materials, error)
	// MaterialsForProcess 是单进程材料装配面（Task 域 Ensure 消费）。
	MaterialsForProcess(ctx context.Context, p *specv1.ProcessSpec, projectID string) (capability.Materials, error)
	// PinVolumes 为卷附件挑选并固化首节点（写库面）。
	PinVolumes(ctx context.Context, ws []capability.Workload, projectID string) error
	// ApplyVolumePinning 是卷钉住的只读并入面（不 pinVolumes、零写库）。
	ApplyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error
	// RegistryCatalog 列项目前纲下的仓名（升序）。
	RegistryCatalog(ctx context.Context, projectID string) ([]string, error)
	// RegistryTags 列项目前纲下仓名的 tag 事实。
	RegistryTags(ctx context.Context, projectID, repository string) ([]capability.RegistryTag, error)
	// ResolveGitHead 解析远端分支当前 head（git 触发链受理面）。
	ResolveGitHead(ctx context.Context, projectID, repoURL, branch string) (string, error)
	// CloneAt 在指定目录检出 ref（webhook 拉取通道）。
	CloneAt(ctx context.Context, projectID, repoURL, ref, dir string) error
	// RecentBuildLogs 返回构建日志环形缓冲快照（build 日志 follow 面）。
	RecentBuildLogs(buildID string) []capability.LogFrame
	// RecentBuildLogsAfter 返回 after 之后的增量帧与下一游标。
	RecentBuildLogsAfter(buildID string, after int64) ([]capability.LogFrame, int64)
	// ResetOrphanBuilds 是启动清扫（在途构建行回 queued 重放）。
	ResetOrphanBuilds(ctx context.Context)
}

// IntakePorts 是构建摄取装配器的构造端口袋（组合根在 engine construct.go
// 绑定核方法值/闭包；形态与构建摄取应用模块的 Deps 逐字段同形，契约叶子
// 只依赖 domain 类型——应用模块包类型不得穿过本叶子）。
type IntakePorts struct {
	Builders map[string]capability.Builder
	Registry capability.Registry
	Logging  capability.Logging
	Cipher   *material.Cipher
	Runtime  capability.Runtime
	Repos    transact.Stores
	Tx       transact.Tx
	Log      *slog.Logger
	// GitHubClient 是 GitHub App 最小客户端（installation token 现铸；
	// 组合根注入，应用模块不构造基础设施实现）。
	GitHubClient githubapp.Client
	// BuildConcurrency/BuildTimeout 是构建循环上限（引擎 Options 派生快照）。
	BuildConcurrency int
	BuildTimeout     time.Duration
	// DataRoot 是数据根配置快照端口（闭包逐拍读核 opts，通则五）。
	DataRoot func() string

	// 内核端口（名对齐核方法，engine construct.go 绑定核方法值/闭包）。
	RootCtx                  func() context.Context
	Spawn                    func(fn func())
	TransitBuild             func(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error)
	TransitAndReload         func(ctx context.Context, d *deployment.Deployment, from []deployment.State, to deployment.State, mut func(*deployment.Deployment)) (*deployment.Deployment, error)
	FailDeployment           func(ctx context.Context, d *deployment.Deployment, reason string) (*deployment.Deployment, error)
	LoadSpec                 func(ctx context.Context, revID string) (*specv1.AppSpec, error)
	CommitWrite              CommitWrite
	EventTeamOfProject       func(ctx context.Context, s transact.Stores, projectID string) string
	ProjectTeam              func(ctx context.Context, projectID string) (string, error)
	RegistryEndpoint         func(ctx context.Context) (capability.RegistryEndpoint, error)
	RegistryProjectEndpoints func() capability.ProjectEndpoints
	RegistryEndpointFor      func(ctx context.Context, projectID string) (capability.RegistryEndpoint, error)
	ManagedEndpoint          func(ctx context.Context) (capability.RegistryEndpoint, error)
	KickBuild                func()
}

// IntakeFactory 由组合根注入（engine.Deps.IntakeFactory），把端口袋装配成
// Intake。engine 核不 import 应用模块包；工厂缺席（手动测试装配）时构建
// 摄取面停用：build 环退化为 no-op 步，门面 nil-safe 返回精确失败/零值，
// lifecycle 跳过孤儿构建清扫。
type IntakeFactory func(IntakePorts) Intake

package engine

// 项目解析面（Project Resolution Face，L1 复合原语 #9——机制原语分层见
// 设计稿 2026-10-30 §2.3；词条见 CONTEXT.md「项目解析面」）：engine 真核
// 的项目级只读解析帮手单源族。本面是 repos/capability 的读组合（I/O 合法
// ——L1 机制原语，与 P3a 下沉 domain 的纯裁决是两回事）；各域消费方一律
// 经此面，**禁止域事实帮手随首个消费者文件落户**（ADR-0088 P4 wave-0
// 收拢：下列五族曾随 backup/database 的首个消费者散落，是星间 7 条误置
// 边的病根）。
//
// 六族清单（定义位置由 internal/guards/resolutionface_test.go 钉死，搬移/
// 改名即红）：
//   - team 归属：projectTeamOf（行实取真源 projectTeam 住 task.go——任务
//     族私有，task 归核无边，不属本面）；
//   - 项目网络名：projectNetworkFacts + projectNetworkFactsNamesOnly 壳；
//   - 控制面节点：controlPlaneNode；
//   - 卷登记：ensureDatabaseVolume；
//   - 凭证解析：databasePassword；
//   - 数据库铸名：DBCredentialSecretName/DatabaseDNSName（P4 wave-3b 自
//     database.go 入面——凭证解析链的头段（铸 Secret 名）与寻址铸名
//     （per-Database DNS 名），backups/browses 的跨星消费曾是星间豁免
//     表成员；包级导出形态保留：interfaces 层（API 创建/投影）直调）；
//   - registry 端点：registryEndpoint/registryProjectEndpoints/
//     registryEndpointFor/managedEndpoint（P4 wave-2b 自 intake 文件入面
//     ——wave-0 注记兑现：族员曾暂驻 buildsource/materials/registrycontent，
//     intake 卫星化后卫星侧消费一律经 facts 端口注入，本面是唯一真源）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// projectTeamOf 读 Project 所属 Team（工具容器 Namespace 锚；不存在的
// Project 返回空 Team——备份执行不因 Team 面缺席而停摆，载体名解析只
// 消费 Project 段）。
func (e *Engine) projectTeamOf(projectID string) string {
	team, err := e.projectTeam(context.Background(), projectID)
	if err != nil {
		return ""
	}
	return team
}

// projectNetworkFacts 返回 Project 的活跃网络名列表与 egress:none 名集
// （数据库挂全部活跃项目网——受管 Proxy 同款语义、限本项目，ADR-0029
// 决策 5；egress 事实供投影填 Workload.EgressNetworks，ADR-0052 决策 6）。
func (e *Engine) projectNetworkFacts(ctx context.Context, projectID string) ([]string, map[string]bool) {
	rows, err := e.repos.Networks.ListByProject(ctx, projectID)
	if err != nil {
		e.log.Error("database reconcile: list project networks", "project", projectID, "err", err)
		return nil, nil
	}
	names := make([]string, 0, len(rows))
	egress := make(map[string]bool, len(rows))
	for _, n := range rows {
		names = append(names, n.Name)
		if n.EgressNone {
			egress[n.Name] = true
		}
	}
	return names, egress
}

// projectNetworkFactsNamesOnly 是 projectNetworkFacts 的名列表形态（备份
// 工具容器挂网/browse 会话挂网/preDeploy job 挂网消费面——egress 事实
// 语义上不适用于工具容器：平台工具载体非隔离对象，ADR-0052 决策 6）。
func (e *Engine) projectNetworkFactsNamesOnly(ctx context.Context, projectID string) []string {
	names, _ := e.projectNetworkFacts(ctx, projectID)
	return names
}

// controlPlaneNode 返回控制面节点的平台 ID（首个可用 manager——v1 单
// manager 形态下即 fleetlyd 所在节点；观测缓存，钉住动作幂等）。
func (e *Engine) controlPlaneNode(ctx context.Context) (string, error) {
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		return "", fmt.Errorf("describe cluster for restore pre-seeding: %w", err)
	}
	for _, n := range view.Nodes {
		if n.Available && n.NodeID != "" && n.Role == "manager" {
			return n.NodeID, nil
		}
	}
	return "", fmt.Errorf("no available manager node for restore pre-seeding")
}

// ensureDatabaseVolume 幂等补建挂靠卷行并返回行（名 = 数据库名；NotFound
// 即建，其余错误上抛）。预创建的同名卷被复用（用户可借 CreateVolume 预先
// 钉住节点——数据库收敛尊重既有 Placement）。返回行使调用方取行 id 作
// 载体键（ADR-0097：卷附件携带行 id，运行时零 name）。
func (e *Engine) ensureDatabaseVolume(ctx context.Context, row *database.Database) (*volume.Volume, error) {
	if existing, err := e.repos.Volumes.GetByName(ctx, row.ProjectID, row.Name); err == nil {
		return existing, nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	created := &volume.Volume{
		ProjectID: row.ProjectID, Name: row.Name,
	}
	if err := e.repos.Volumes.Create(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

// ensurePubSubVolume 幂等补建 pubsub 挂靠卷行并返回行（B4b；
// ensureDatabaseVolume 同构——名 = 实例名的确定性公式，ADR-0097 行 id
// 作载体键）。
func (e *Engine) ensurePubSubVolume(ctx context.Context, row *pubsub.PubSub) (*volume.Volume, error) {
	if existing, err := e.repos.Volumes.GetByName(ctx, row.ProjectID, row.Name); err == nil {
		return existing, nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	created := &volume.Volume{
		ProjectID: row.ProjectID, Name: row.Name,
	}
	if err := e.repos.Volumes.Create(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

// pubsubPassword 解密 pubsub 凭证回读密码（B4b；databasePassword 同构——
// 单真源 = Secret 里的连接材料方言串，ADR-0096 决策 4）。
func (e *Engine) pubsubPassword(ctx context.Context, row *pubsub.PubSub) (string, error) {
	if e.cipher == nil {
		return "", fmt.Errorf("backup: pubsub credentials require the master key")
	}
	sec, err := e.repos.Secrets.GetByName(ctx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		return "", fmt.Errorf("backup: lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("backup: decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := pubtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return "", fmt.Errorf("backup: credential secret %q: %w", row.CredentialsRef, err)
	}
	return password, nil
}

// databasePassword 解密数据库凭证回读密码（备份/恢复渲染共用；单真源
// = Secret 里的完整连接串，ADR-0029 决策 6）。
func (e *Engine) databasePassword(ctx context.Context, row *database.Database) (string, error) {
	if e.cipher == nil {
		return "", fmt.Errorf("backup: database credentials require the master key")
	}
	sec, err := e.repos.Secrets.GetByName(ctx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		return "", fmt.Errorf("backup: lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("backup: decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := dbtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return "", fmt.Errorf("backup: credential secret %q: %w", row.CredentialsRef, err)
	}
	return password, nil
}

// DBCredentialSecretName 铸凭证 Secret 名（API 创建面与投影共用的单源
// 公式）。公式真源自 P5 wave-3 起下沉 engineports（核/gRPC 面与 backups/
// browses 卫星三方消费，WorkloadID 先例）；本声明保解析面锚
// （resolutionface）与 interfaces 直调零改。
func DBCredentialSecretName(databaseName string) string {
	return engineports.DBCredentialSecretName(databaseName)
}

// DatabaseDNSName 铸 per-Database 稳定 DNS 名（engine 铸名公式真源，
// TaskDNSName 先例；Provider 经 Addressing 声明映射为自己的原语）。模板
// 子包不拥有铸名——ConnURL 收 host 注入（dbtemplate 包注释）。公式真源
// 同上下沉 engineports（核 rotate/database/projection、gRPC 投影面与
// backups 卫星恢复渲染三方消费）；本声明保解析面锚与既名直调零改。
func DatabaseDNSName(databaseID string) string {
	return engineports.DatabaseDNSName(databaseID)
}

// registryEndpoint 解析受管仓库端点（推送目标/下发引用/平台凭证的共同
// 真源）。无 Registry Provider = build 源部署的精确失败（附录 B.5①——
// 不做本机导入退化形态：裸 image ID 引用真机 swarm 不可拉取）。
func (e *Engine) registryEndpoint(ctx context.Context) (capability.RegistryEndpoint, error) {
	if e.registry == nil {
		return capability.RegistryEndpoint{}, fmt.Errorf("no registry provider wired; build-source deployments require the managed registry (set FLEETLY_REGISTRY_ADDR on the control plane)")
	}
	endpoint, err := e.registry.Endpoint(ctx)
	if err != nil {
		return capability.RegistryEndpoint{}, fmt.Errorf("resolve managed registry endpoint: %w", err)
	}
	if endpoint.Addr == "" {
		return capability.RegistryEndpoint{}, fmt.Errorf("managed registry endpoint returned an empty address")
	}
	return endpoint, nil
}

// registryProjectEndpoints 返回 Registry 的 per-Project 端点面（nil = 面
// 未提供，调用方回退平台凭证——升级零扰动序）。
func (e *Engine) registryProjectEndpoints() capability.ProjectEndpoints {
	if e.registry == nil {
		return nil
	}
	return capability.FacesOf(e.registry).ProjectEndpoints
}

// registryEndpointFor 解析代理调用凭证（per-Project 优先——accessControl
// 门禁与构建推送同源；平台凭证兜底——adminPolicy 全域读）。
func (e *Engine) registryEndpointFor(ctx context.Context, projectID string) (capability.RegistryEndpoint, error) {
	faces := capability.FacesOf(e.registry)
	if faces.ProjectEndpoints != nil {
		if ep, err := faces.ProjectEndpoints.EndpointForProject(ctx, projectID); err == nil {
			return ep, nil
		}
		// per-Project 凭证铸造失败（盘面等）→ 平台凭证回退（只读代理，
		// 权限面更宽不构成提权——受理位已做行级授权）。
	}
	return e.registry.Endpoint(ctx)
}

// managedEndpoint 给受管仓库端点解析补独立硬界（B15-2）：Endpoint 是
// registry Provider 的外部调用，可独立 hang——序列级界（materialize 序列
// 头 / ensureTaskWorkloads 下发段）虽覆盖既有消费面，但 hang 会吃光整段
// 预算让 Ensure 饿死；独立界让端点解析先失败，预算留给后续步。双重
// WithTimeout 取 min，语义不变（drift.go 既有形态）。
func (e *Engine) managedEndpoint(ctx context.Context) (capability.RegistryEndpoint, error) {
	ctx, cancel := e.boundedStep(ctx)
	defer cancel()
	return e.registry.Endpoint(ctx)
}

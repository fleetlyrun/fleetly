package engine

// 项目解析面（Project Resolution Face，L1 复合原语 #9——机制原语分层见
// 设计稿 2026-10-30 §2.3；词条见 CONTEXT.md「项目解析面」）：engine 真核
// 的项目级只读解析帮手单源族。本面是 repos/capability 的读组合（I/O 合法
// ——L1 机制原语，与 P3a 下沉 domain 的纯裁决是两回事）；各域消费方一律
// 经此面，**禁止域事实帮手随首个消费者文件落户**（ADR-0088 P4 wave-0
// 收拢：下列五族曾随 backup/database 的首个消费者散落，是星间 7 条误置
// 边的病根）。
//
// 五族清单（定义位置由 internal/guards/resolutionface_test.go 钉死，搬移/
// 改名即红）：
//   - team 归属：projectTeamOf（行实取真源 projectTeam 住 task.go——任务
//     族私有，task 归核无边，不属本面）；
//   - 项目网络名：projectNetworkFacts + projectNetworkFactsNamesOnly 壳；
//   - 控制面节点：controlPlaneNode；
//   - 卷登记：ensureDatabaseVolume；
//   - 凭证解析：databasePassword。
// 词条五族之一的 registry 端点族（registryEndpoint/managedEndpoint）暂驻
// intake 文件（今日无星间消费边），随 intake 卫星化波次入面。

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
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

// ensureDatabaseVolume 幂等补建挂靠卷行（名 = 数据库名；NotFound 即建，
// 其余错误上抛）。预创建的同名卷被复用（用户可借 CreateVolume 预先钉
// 住节点——数据库收敛尊重既有 Placement）。
func (e *Engine) ensureDatabaseVolume(ctx context.Context, row *database.Database) error {
	if _, err := e.repos.Volumes.GetByName(ctx, row.ProjectID, row.Name); err == nil {
		return nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return err
	}
	return e.repos.Volumes.Create(ctx, &volume.Volume{
		ProjectID: row.ProjectID, Name: row.Name,
	})
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

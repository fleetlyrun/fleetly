package naming

import (
	"fmt"
	"sort"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// ServiceLabels 构造 Swarm 服务的平台 label 最小集（state-model §2.4 对象
// 标记契约，表 Service 行：managed=true、app、process、deployment；v0.3 增
// team / project 两键——rbac-teams §4.3「label 集 +fleetly.team /
// fleetly.project（服务创建时写入；对账/清扫/管理查询的识别面）」；cron 为
// v0.2 契约、调用方经 state 常量另行附加）。写者唯一 = 适配器 Marker 端口；
// 密钥/payload 永不入 label。返回 map 无序，消费方（Docker API）对顺序无
// 语义依赖；需要稳定序列化时用 SortedServiceLabels。
//
// fleetly.app 的值是三段限定形 `team/prj/app`（QualifiedName——流标签口径，
// rbac-teams §4.3）。team/prj 是单词制 slug（进底座命名公式的两个不可变
// 段），经 validateComponent 校验（与命名函数同一字符集——两者最终都落到
// Swarm 对象名/label 值）。
func ServiceLabels(team, prj, app, service, deploymentID string) (map[string]string, error) {
	appLabel, err := QualifiedName(team, prj, app)
	if err != nil {
		return nil, err
	}
	if err := validateComponent("service", service); err != nil {
		return nil, err
	}
	if deploymentID == "" {
		return nil, fmt.Errorf("naming: deployment id is empty")
	}
	return map[string]string{
		state.LabelManaged:    state.ManagedLabelValue,
		state.LabelApp:        appLabel,
		state.LabelProcess:    service,
		state.LabelDeployment: deploymentID,
		state.LabelTeam:       team,
		state.LabelProject:    prj,
	}, nil
}

// ContainerLabels 构造容器归属 label（state-model §2.4 表 Container 行：
// 仅 fleetly.app——人工排障识别归属，不参与决策；执行中继的容器准入
// 判定同用此 label，architecture §2.6 D19）。值 = 三段限定形
// `team/prj/app`（流标签口径，ServiceLabels 同源）。
func ContainerLabels(team, prj, app string) (map[string]string, error) {
	appLabel, err := QualifiedName(team, prj, app)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		state.LabelApp: appLabel,
	}, nil
}

// SortedServiceLabels 返回键序稳定的 label 序列（golden 测试与日志脱敏
// 渲染用）。
func SortedServiceLabels(labels map[string]string) [][2]string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, labels[k]})
	}
	return out
}

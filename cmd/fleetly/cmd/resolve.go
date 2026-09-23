package cmd

// 凭据与上下文的统一解析矩阵（v0.3 W1-S4，rbac-teams 设计 §2.4）：读序
//
//	flag > env > config
//
// 三类值同序：token（--token / FLEETLY_TOKEN / config.token）、team
//（--team / FLEETLY_TEAM / config.current_team）、project（--project /
// FLEETLY_PROJECT / config.current_project）。解析单点在本文件（测试钉死
// 优先级矩阵）；connFlags.dial 与 auth status 是仅有的两个消费点——dial
// 取 token，status 另取上下文做展示（资源命令按上下文过滤是 W2）。
//
// 空值语义：flag 缺省空串、env 未设或空串、config 字段缺省——三态都以
// 「空 = 未设置」参与回落（export FLEETLY_TOKEN= 等价 unset，与既有 env
// 缺省回落纪律一致）。

import "os"

// 解析来源标签（resolveCredential/resolveContext 返回；空串 = 未解析到）。
const (
	sourceFlag   = "flag"
	sourceEnv    = "env"
	sourceConfig = "config"
)

// resolvedContext 是 team/project 上下文的解析投影（值 + 各自来源——两轴
// 独立回落，--team 不影响 project 的解析；json 标签 = auth status --json
// 的 context 契约形态）。
type resolvedContext struct {
	Team          string `json:"team"`
	Project       string `json:"project"`
	TeamSource    string `json:"team_source"`
	ProjectSource string `json:"project_source"`
}

// firstNonEmpty 按参数序返回首个非空值与来源标签（优先级矩阵的机械形：
// 调用序即优先级，逐对 (值, 来源) 传入）。
func firstNonEmpty(pairs ...[2]string) (string, string) {
	for _, p := range pairs {
		if p[0] != "" {
			return p[0], p[1]
		}
	}
	return "", ""
}

// resolveToken 解析生效 token：flag > FLEETLY_TOKEN > config.token。config
// 读取失败原样上抛（fail-loud，见 loadCLIConfig）。
func resolveToken(flagToken string) (token, source string, err error) {
	c, err := loadCLIConfig()
	if err != nil {
		return "", "", err
	}
	token, source = firstNonEmpty(
		[2]string{flagToken, sourceFlag},
		[2]string{os.Getenv("FLEETLY_TOKEN"), sourceEnv},
		[2]string{c.Token, sourceConfig},
	)
	return token, source, nil
}

// resolveContext 解析 team/project 上下文：flag > env（FLEETLY_TEAM /
// FLEETLY_PROJECT）> config（current_team/current_project）。与 token 同序
// 同空值语义。
func resolveContext(teamFlag, projectFlag string) (resolvedContext, error) {
	c, err := loadCLIConfig()
	if err != nil {
		return resolvedContext{}, err
	}
	team, teamSrc := firstNonEmpty(
		[2]string{teamFlag, sourceFlag},
		[2]string{os.Getenv("FLEETLY_TEAM"), sourceEnv},
		[2]string{c.CurrentTeam, sourceConfig},
	)
	project, projSrc := firstNonEmpty(
		[2]string{projectFlag, sourceFlag},
		[2]string{os.Getenv("FLEETLY_PROJECT"), sourceEnv},
		[2]string{c.CurrentProject, sourceConfig},
	)
	return resolvedContext{
		Team: team, Project: project,
		TeamSource: teamSrc, ProjectSource: projSrc,
	}, nil
}

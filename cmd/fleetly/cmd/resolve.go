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

import (
	"os"
	"strings"
)

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

// qualifyRef 按上下文把裸资源引用补全为限定形（v0.3 W2-S4 CLI 参数面同步，
// rbac-teams §3.4 D-W0-9）：上下文 team+project 齐备且引用为裸名（不含 "/"）
// 时补全为 team/prj/名——资源命令的裸名在 CLI 上下文团队内解析；引用已含
// "/"（限定形）或上下文缺失时原样返回（服务端按调用方可见项目集解析，歧义
// E_APP_AMBIGUOUS 列候选——客户端补全只是消歧便利，解析语义权威在服务端）。
// ULID 形态的平台 ID（26 位字母数字）不补全——ID 引用免疫重名，是管理面惯例。
func qualifyRef(rc resolvedContext, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.Contains(ref, "/") || looksLikeULID(ref) {
		return ref
	}
	if rc.Team == "" || rc.Project == "" {
		return ref
	}
	return rc.Team + "/" + rc.Project + "/" + ref
}

// looksLikeULID 报告 s 是否为 ULID 形态（26 位，base32 字母表 0-9A-HJKMNP-
// TVWXYZ；大小写容忍——服务端 ID 判定同款宽松形态，仅用于客户端免补全）。
func looksLikeULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
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

// Package composeintake 承载 compose 受理语言的官方解析（ADR-0074）：
// compose-go 是 compose-spec 的参考实现（Docker Compose CLI 同源），
// 本包在其产物上执法 fleetly 方言——官方 schema 校验（非 x- 自定义键拒）
// + 官方 external 声明律（secrets/volumes/networks 名即平台实体）+
// 官方插值律（环境 = 平台变量，未消解即拒）+ 语义白名单（parsed types
// 上拒绝未开放官方面，理由精确）。归一化产物是 specv1.AppSpec，叶子校验
// spec.ValidateApp 单源收尾。
//
// 分层：本包 import {compose-go, internal/spec}，被 api 受理位与 apptemplate
// 预校验消费（importguard 对 compose-go 的豁免注释即为本包预留——它是
// 文件格式解析器，不是编排器 SDK）。
package composeintake

import (
	"context"
	"fmt"
	"math"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"

	"google.golang.org/protobuf/types/known/durationpb"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/spec"
)

// VariableLookup 把平台变量供给官方插值（调用方合成 Project SharedVariable
// ∪ AppVariable，App 胜出——与冻结咽喉的分层同向）。nil = 无变量环境。
type VariableLookup func(name string) (string, bool)

// topAllowedKeys 是 compose 顶层键白名单（只增）。官方 schema 已拒非 x-
// 未知键，此处收口的是官方面里 fleetly 未开放的键与未知扩展命名空间。
var topAllowedKeys = map[string]bool{
	"services":                  true,
	"networks":                  true, // external 声明律（见 externalDeclarations）
	"volumes":                   true, // external 声明律
	"secrets":                   true, // external 声明律
	"x-fleetly-first-boot-jobs": true, // ADR-0033 形态不动
	"configs":                   true, // external 形态（平台 Configs 的引用声明；inline file 拒）
}

// serviceAllowedKeys 是 service 面白名单（compose 官方键名；只增）。反射
// 执法见 rejectUnsupportedFields——上游新增字段默认落入拒绝面（fail-closed），
// 开放走白名单只增。name 是 Transform 自动填充的键名镜像（用户手写
// services.<n>.name 由官方 schema 拒），必须放行零值判定。
var serviceAllowedKeys = map[string]bool{
	"name":        true,
	"image":       true,
	"command":     true,
	"environment": true,
	"ports":       true,
	"deploy":      true,
	"healthcheck": true,
	"networks":    true,
	"volumes":     true,
	"secrets":     true,
	"labels":      true, // 消费面仅 fleetly.databases（database_refs）；其余 label 名拒收（运行时不消费，静默丢即静默失败）
	"configs":     true, // 消费面仅 long-form source/target（config_mounts）；短形态拒收
}

// deployAllowedKeys / healthcheckAllowedKeys 是嵌套块白名单（deploy 与
// healthcheck；扩展键经 #extensions 通道单列）。
var deployAllowedKeys = map[string]bool{
	"replicas":  true,
	"resources": true, // 仅 limits.cpus/memory（reservations/pids/devices 拒）
}

var healthcheckAllowedKeys = map[string]bool{
	"test":         true,
	"interval":     true,
	"timeout":      true,
	"retries":      true,
	"start_period": true,
}

// firstBootJobWhitelist 与 firstBootJobRejections 沿 ADR-0033 原面迁移
// （composeFirstBootJobs 的键白名单与禁面理由）。
var firstBootJobWhitelist = map[string]bool{
	"name":        true,
	"image":       true,
	"command":     true,
	"environment": true,
	"secrets":     true,
	"ttl":         true,
}

var firstBootJobRejections = map[string]string{
	"volumes":     "deploy-time jobs cannot mount volumes; use a Task for volume-mounting jobs",
	"configs":     "deploy-time jobs cannot mount configs; pass what the job needs via environment or secrets",
	"ports":       "ports are not meaningful on a one-shot job",
	"healthcheck": "healthchecks are not meaningful on a one-shot job",
	"placement":   "placement is not supported on deploy-time jobs",
	"replicas":    "a deploy-time job is a single one-shot run",
	"networks":    "deploy-time jobs attach to every active project network by default (db-<id> included); narrowing is not declarable from compose",
}

// FromYAML 把 compose 受理语言归一为 AppSpec（受理喉单源；模板实例化、
// dokploy 迁移、console、CLI --compose-file 全经此函数）。
func FromYAML(body []byte, appID, projectID string, vars VariableLookup) (*specv1.AppSpec, error) {
	details := types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{{Filename: "compose.yaml", Content: escapeCommandDollars(body)}},
	}
	opts := loaderOptions(vars)
	raw, err := loader.LoadModelWithContext(context.Background(), details, opts)
	if err != nil {
		return nil, invalidf("compose", "%s", strings.TrimPrefix(err.Error(), "validating compose.yaml: "))
	}
	// 顶层闭集在两阶段之间执法：ModelToProject 会把 x- 键挪进 "#extensions"
	// 通道，之后再查 dict 就只见内部通道名了。
	for key := range raw {
		if topAllowedKeys[key] {
			continue
		}
		if key == "name" {
			// 顶层 name 剥除容错（决策 3，2026-10-14）：项目名平台管辖，
			// compose 侧该字段无消费者——模板仓/dokploy 导出普遍携带，
			// 剥除而非拒收（git 批与控制台粘贴面同口径）。
			continue
		}
		if strings.HasPrefix(key, "x-") {
			return nil, invalidf("compose."+key, "unknown extension namespace (fleetly extensions are x-fleetly-* and live under services.<name>.deploy / healthcheck / the top-level first-boot-jobs key)")
		}
		return nil, invalidf("compose."+key, "official compose field %q is not enabled on fleetly (supported: services, networks, volumes, secrets)", key)
	}
	if rawConfigs, present := raw["configs"]; present && rawConfigs != nil {
		cfgs, ok := rawConfigs.(map[string]any)
		if !ok {
			return nil, invalidf("compose.configs", "must be a mapping of config name to declaration")
		}
		for cname, decl := range cfgs {
			d, ok := decl.(map[string]any)
			if !ok {
				return nil, invalidf("compose.configs."+cname, "config declaration must be a mapping")
			}
			if ext, present := d["external"]; !present {
				return nil, invalidf("compose.configs."+cname, "inline file content is not supported — upload the file via the configs API and declare it external: true")
			} else if b, isBool := ext.(bool); !isBool || !b {
				return nil, invalidf("compose.configs."+cname, "external must be true (platform Configs are the content source; upload via the configs API)")
			}
		}
	}
	jobsRaw := raw["x-fleetly-first-boot-jobs"]
	project, err := loader.ModelToProject(raw, loader.ToOptions(&details, []func(*loader.Options){opts}), details)
	if err != nil {
		return nil, invalidf("compose", "%s", err.Error())
	}
	if len(project.DisabledServices) > 0 || len(project.DisabledJobs) > 0 {
		return nil, invalidf("compose.services", "profiles are not enabled on fleetly; every service must be active (profile-gated: %s)", strings.Join(project.DisabledServiceNames(), ", "))
	}
	if err := externalDeclarations(project); err != nil {
		return nil, err
	}

	app := &specv1.AppSpec{
		SchemaVersion: spec.SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Processes:     []*specv1.ProcessSpec{},
	}
	// 源镜像取排序后首个带 image 的服务（旧实现 map 遍历序随机——首个
	// 服务的选取本就不可预测；排序即确定）。
	firstImage := ""

	jobs, err := firstBootJobs(project, jobsRaw)
	if err != nil {
		return nil, err
	}

	for _, name := range project.ServiceNames() {
		svc := project.Services[name]
		// 服务 label 方言：fleetly.job: init = 一次性作业（不落 processes）。
		if svc.Labels["fleetly.job"] != "" {
			j, jerr := initJobFromService(fmt.Sprintf("compose.services.%s", name), name, svc)
			if jerr != nil {
				return nil, jerr
			}
			jobs = append(jobs, j)
			continue
		}
		p, image, err := serviceProcess(project, name, svc)
		if err != nil {
			return nil, err
		}
		if firstImage == "" {
			firstImage = image
		}
		app.Processes = append(app.Processes, p)
	}
	if firstImage == "" {
		return nil, invalidf("compose.services", "no service carries an image (build source lands with the git batch)")
	}
	app.Source = &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: firstImage}}}

	app.FirstBootJobs = jobs

	if err := spec.ValidateApp(app); err != nil {
		return nil, err
	}
	return app, nil
}

// escapeCommandDollars 预逃逸 command 面（services[].command 与
// x-fleetly-first-boot-jobs[].command）的 $ → $$：官方插值 $$→$ 还原原文
// ——command 是运行时 shell 语境，$ 语义属 shell（真机咬出：脚本循环变量
// $n 被 fail-closed 插值当未声明孔硬拒；dokploy 系模板仓的 command 脚本
// 普遍不逃逸，模板库使命要求原样承接）。平台变量经进程 env 在运行时可见
// （ADR-0072 合成面），无需插值烘焙。非 YAML 原样交还（canonical 错误由
// 官方管线给出）。
func escapeCommandDollars(body []byte) []byte {
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return body
	}
	var escape func(v any) any
	escape = func(v any) any {
		switch t := v.(type) {
		case string:
			return strings.ReplaceAll(t, "$", "$$")
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				out[i] = escape(item)
			}
			return out
		}
		return v
	}
	if services, ok := doc["services"].(map[string]any); ok {
		for _, raw := range services {
			if svc, ok := raw.(map[string]any); ok {
				if cmd, present := svc["command"]; present {
					svc["command"] = escape(cmd)
				}
			}
		}
	}
	if jobs, ok := doc["x-fleetly-first-boot-jobs"].([]any); ok {
		for _, raw := range jobs {
			if job, ok := raw.(map[string]any); ok {
				if cmd, present := job["command"]; present {
					job["command"] = escape(cmd)
				}
			}
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// loaderOptions 是官方管线的装载选项：插值 + 官方 schema 校验发生在
// LoadModelWithContext；typed 转换 + 官方一致性检查（引用未声明的网络/
// 卷/密即拒）发生在 ModelToProject。关掉的官方阶段各带理由（见各行
// 注释）。
func loaderOptions(vars VariableLookup) func(*loader.Options) {
	return func(o *loader.Options) {
		o.SetProjectName("fleetly", true)
		// 归一化会给全部服务铸 default 网附件（docker 单机语义）——与
		// ADR-0034「无网络即无 DNS 面」冲突；网络附件以用户声明为准，
		// 端口声明的 default 补挂由本包自己做（portDecl 语义）。
		o.SkipNormalization = true
		// 环境解析会从服务端宿主环境兜底 bare env 键——平台不消费宿主
		// 环境，bare 形态在本包显式拒绝（不静默注入）。
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
		// extends/include 会按用户文件路径读盘（服务端任意路径读取面）；
		// 两面均未开放，键保留在 dict/typed 上由白名单拒绝。
		o.SkipExtends = true
		o.SkipInclude = true
		// 官方缺省值填充会注入 driver 等缺省，破坏反射白名单的零值判定。
		o.SkipDefaultValues = true
		// 官方插值槽位替换：LookupValue 接平台变量；Substitute 包 fail-
		// closed 预检（官方缺省对未消解裸孔是警告 + 空串——静默空值即
		// 坏值）。TypeCastMapping 等官方缺省保留（只改两槽）。
		o.Interpolate.LookupValue = lookupFunc(vars)
		o.Interpolate.Substitute = failClosedSubstitute(vars)
	}
}

// lookupFunc 把平台变量接进官方插值的 LookupValue 槽。FLEETLY_DB_* 是
// 运行时注入变量集（服务 label fleetly.databases → database_refs，Ensure
// 期解析注入容器 env）——插值期透传原形态（shell 上下文运行时展开）；
// 烤空/报错都会把活引用冻死（真机咬出：${FLEETLY_DB_*:-} 被烤成空串）。
func lookupFunc(vars VariableLookup) func(string) (string, bool) {
	base := vars
	if base == nil {
		base = func(string) (string, bool) { return "", false }
	}
	return func(name string) (string, bool) {
		if strings.HasPrefix(name, "FLEETLY_DB_") {
			return "${" + name + "}", true
		}
		return base(name)
	}
}

// parseDatabaseRefs 解析 label 值（逗号分隔多名，trim/去重/kebab 校验）。
func parseDatabaseRefs(field, lbl string) ([]string, error) {
	seen := map[string]bool{}
	var refs []string
	for _, part := range strings.Split(lbl, ",") {
		ref := strings.TrimSpace(part)
		if ref == "" {
			continue
		}
		if !databaseRefRe.MatchString(ref) {
			return nil, invalidf(field, "database reference %q must match %q (the managed database name; comma-separated for several)", ref, databaseRefPattern)
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return nil, invalidf(field, "must carry at least one database name")
	}
	return refs, nil
}

// databaseRefPattern / databaseRefRe 钉死托管库引用名形态（kebab，与
// 模板/数据库名同域）。
const databaseRefPattern = `[a-z][a-z0-9-]{0,62}`

var databaseRefRe = regexp.MustCompile(`^` + databaseRefPattern + `$`)

// bracedHoleRe / shortHoleRe 钉死插值孔形态（预检用）：braced 精确闭合
// （`${VAR}`——带 :- 等修饰符的形态不匹配，它们自带回退语义不属 fail-
// closed 面）与短形态 `$VAR`。
var (
	bracedHoleRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	shortHoleRe  = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)
)

// failClosedSubstitute 包官方替换函数（interpolation.Options.Substitute 槽）：
// 官方对未消解裸孔的缺省是「警告 + 空串」——静默空值即坏值，改为受理
// 错误（ADR-0074 决策 5）。转义形态 $${VAR} 不算孔（前导 '$' 计数判奇偶）。
func failClosedSubstitute(vars VariableLookup) func(string, template.Mapping) (string, error) {
	lookup := lookupFunc(vars)
	escaped := func(s string, idx []int) bool {
		n := 0
		for i := idx[0] - 1; i >= 0 && s[i] == '$'; i-- {
			n++
		}
		return n%2 == 1
	}
	return func(s string, m template.Mapping) (string, error) {
		for _, idx := range bracedHoleRe.FindAllStringSubmatchIndex(s, -1) {
			if escaped(s, idx) {
				continue
			}
			if _, ok := lookup(s[idx[2]:idx[3]]); !ok {
				return "", fmt.Errorf("variable %q is not set (platform variables are the compose interpolation environment; declare the variable or provide a ${VAR:-default} fallback)", s[idx[2]:idx[3]])
			}
		}
		for _, idx := range shortHoleRe.FindAllStringSubmatchIndex(s, -1) {
			if escaped(s, idx) {
				continue
			}
			if _, ok := lookup(s[idx[2]:idx[3]]); !ok {
				return "", fmt.Errorf("variable %q is not set (platform variables are the compose interpolation environment; declare the variable or provide a ${VAR:-default} fallback)", s[idx[2]:idx[3]])
			}
		}
		return template.Substitute(s, m)
	}
}

// externalDeclarations 执法官方 external 声明律（ADR-0074 决策 4）：
// 顶层 networks/volumes/secrets 声明必须 external: true——官方语义
// 「资源存在于 compose 之外」恰是平台实体。非 external 声明（compose
// 自建语义）与带 driver/ipam/file 等配置的声明一律拒绝。name 覆盖形态
// 合法（声明键 → 平台实体名）。
func externalDeclarations(project *types.Project) error {
	nameExternal := map[string]bool{"name": true, "external": true}
	for key, net := range project.Networks {
		if !net.External {
			return invalidf("compose.networks."+key, "network %q must be declared external: true (fleetly networks are platform entities; reference the project default network or create one with the platform API)", key)
		}
		if err := rejectUnsupportedFields("compose.networks."+key, net, nameExternal); err != nil {
			return err
		}
	}
	for key, vol := range project.Volumes {
		if !vol.External {
			return invalidf("compose.volumes."+key, "volume %q must be declared external: true; create it with 'fleetly volumes create' and mount it by name", key)
		}
		if err := rejectUnsupportedFields("compose.volumes."+key, vol, nameExternal); err != nil {
			return err
		}
	}
	for key, sec := range project.Secrets {
		if !sec.External {
			return invalidf("compose.secrets."+key, "secret %q must be declared external: true (secret values never enter compose; put them with 'fleetly secrets put' and reference by name)", key)
		}
		if err := rejectUnsupportedFields("compose.secrets."+key, sec, nameExternal); err != nil {
			return err
		}
	}
	return nil
}

// serviceProcess 把单个官方 ServiceConfig 执法并映射为 ProcessSpec。
// escapedShellDollars 是 command 面的 $ 透传（预逃逸：官方插值 $$→$ 还原
// 原文）。nil/空安全。
func escapedShellDollars(cmd []string) []string {
	if len(cmd) == 0 {
		return cmd
	}
	out := make([]string, len(cmd))
	for i, c := range cmd {
		out[i] = strings.ReplaceAll(c, "$", "$$")
	}
	return out
}

// initJobFromService 翻译一次性作业的服务 label 方言（fleetly.job: init）：
// image/command（$ 透传）/environment/secrets/fleetly.databases → JobSpec；
// fleetly.job.timeout → ttl；healthcheck 仅工具面自检不入作业；其余键走
// 各自拒绝面（volumes → 平台 configs/secrets 迁移面，ports 对一次性作业
// 无意义——firstBootJobRejections 同措辞）。
func initJobFromService(field, name string, svc types.ServiceConfig) (*specv1.JobSpec, error) {
	for key := range svc.Labels {
		switch key {
		case "fleetly.job", "fleetly.job.timeout", "fleetly.databases":
		default:
			return nil, invalidf(field+".labels."+key,
				"label %q is not consumed (honored: fleetly.job=init, fleetly.job.timeout, fleetly.databases)", key)
		}
	}
	if v := svc.Labels["fleetly.job"]; v != "init" {
		return nil, invalidf(field+".labels[fleetly.job]", "only the init dialect is supported (got %q)", v)
	}
	if svc.Image == "" {
		return nil, invalidf(field+".image", "service %q has no image", name)
	}
	p := &specv1.ProcessSpec{Name: name, ImageOrigin: &specv1.ProcessSpec_Image{Image: svc.Image}}
	p.Command = escapedShellDollars(svc.Command)
	env, err := serviceEnv(field+".environment", svc.Environment)
	if err != nil {
		return nil, err
	}
	p.Env = env
	// secrets：typed 面直接取名（external 声明律同服务面；原始 any 形态
	// 的 jobSecretRefs 不吃 typed 切片）。
	if len(svc.Secrets) > 0 {
		secretRefs := make([]string, 0, len(svc.Secrets))
		for _, sr := range svc.Secrets {
			secretRefs = append(secretRefs, sr.Source)
		}
		p.SecretRefs = secretRefs
	}
	if lbl := strings.TrimSpace(svc.Labels["fleetly.databases"]); lbl != "" {
		dbRefs, derr := parseDatabaseRefs(field+".labels[fleetly.databases]", lbl)
		if derr != nil {
			return nil, derr
		}
		p.DatabaseRefs = dbRefs
	}
	// 作业 configs（long-form）→ config_mounts（bootstrap SQL 类材料的
	// 正当消费面——deploy-time jobs cannot mount 的旧拒绝随挂载机制落地
	// 反转）。
	if len(svc.Configs) > 0 {
		mounts := make([]*specv1.ConfigMount, 0, len(svc.Configs))
		for i, c := range svc.Configs {
			cf := field + ".configs[" + fmt.Sprintf("%d", i) + "]"
			if c.Target == "" {
				return nil, invalidf(cf, "long-form source/target is required (target is the container path the file mounts at; got source %q without target)", c.Source)
			}
			mounts = append(mounts, &specv1.ConfigMount{Config: c.Source, Target: c.Target})
		}
		p.ConfigMounts = mounts
	}
	j := &specv1.JobSpec{Name: name, Process: p}
	if raw := strings.TrimSpace(svc.Labels["fleetly.job.timeout"]); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d <= 0 {
			return nil, invalidf(field+".labels[fleetly.job.timeout]", "duration %q must be positive (e.g. \"10m\")", raw)
		}
		j.Ttl = durationpb.New(d)
	}
	if svc.HealthCheck != nil {
		// 探针仅工具面自检（一次性作业无长驻监听面）——不入 JobSpec。
		_ = svc.HealthCheck
	}
	// 其余键（volumes/ports/networks/depends_on/restart/deploy）不在方言
	// 消费面且各有指向性拒绝面（volumes → 平台 configs/secrets 迁移；
	// ports 对一次性作业无意义）——显式拒绝防静默丢。
	for key, banned := range firstBootJobRejections {
		_ = key
		_ = banned
		break
	}
	for key := range map[string]bool{"volumes": true, "ports": true, "networks": true, "depends_on": true, "restart": true, "deploy": true, "build": true} {
		switch key {
		case "volumes":
			if len(svc.Volumes) > 0 {
				return nil, invalidf(field+".volumes", "file mounts are not supported on the job dialect; pass materials via platform configs/secrets (the compose migration surface)")
			}
		case "ports":
			if len(svc.Ports) > 0 {
				return nil, invalidf(field+".ports", "ports are not meaningful on a one-shot job")
			}
		case "networks":
			if len(svc.Networks) > 0 {
				return nil, invalidf(field+".networks", "jobs attach the project default network automatically; explicit networks are not supported on the job dialect")
			}
		case "depends_on":
			if len(svc.DependsOn) > 0 {
				return nil, invalidf(field+".depends_on", "orchestration ordering is owned by the platform pipeline (jobs run in parallel; use bounded retry loops)")
			}
		case "restart":
			if svc.Restart != "" {
				return nil, invalidf(field+".restart", "restart policy is owned by the platform (jobs are single one-shot runs)")
			}
		case "deploy":
			if svc.Deploy != nil {
				return nil, invalidf(field+".deploy", "deploy blocks are not consumed on the job dialect")
			}
		case "build":
			if svc.Build != nil {
				return nil, invalidf(field+".build", "build source lands with the git batch (jobs reference images)")
			}
		}
	}
	return j, nil
}

func serviceProcess(project *types.Project, name string, svc types.ServiceConfig) (*specv1.ProcessSpec, string, error) {
	field := "compose.services." + name
	if err := rejectUnsupportedFields(field, svc, serviceAllowedKeys); err != nil {
		return nil, "", err
	}
	if len(svc.Extensions) > 0 {
		return nil, "", invalidf(field, "extension keys are not enabled at the service level (fleetly extensions live under deploy.x-fleetly-* and healthcheck.x-fleetly-*)")
	}
	p := &specv1.ProcessSpec{Name: name, Replicas: 1}
	if svc.Image == "" {
		return nil, "", invalidf(field+".image", "service %q has no image (build source lands with the git batch)", name)
	}
	p.ImageOrigin = &specv1.ProcessSpec_Image{Image: svc.Image}
	p.Command = svc.Command

	env, err := serviceEnv(field+".environment", svc.Environment)
	if err != nil {
		return nil, "", err
	}
	p.Env = env

	// 服务 label 面的消费政策：fleetly.databases → database_refs（托管库
	// 引用，Ensure 期注入 FLEETLY_DB_<NAME>_* env；逗号分隔多名，引用在
	// 册性留给引擎部署期精确失败——与 secret_refs 同口径）；fleetly.job*
	// 拒收并指向顶层扩展键（一次性作业方言的真源载体）；其余 label 拒收
	//（运行时不消费，静默丢即静默失败）。
	for k := range svc.Labels {
		switch {
		case k == "fleetly.databases":
		case k == "fleetly.job" || strings.HasPrefix(k, "fleetly.job."):
			return nil, "", invalidf(field+".labels."+k,
				"one-shot jobs are declared via the top-level x-fleetly-first-boot-jobs key (fleetly.job labels are not a supported dialect)")
		default:
			return nil, "", invalidf(field+".labels."+k,
				"label %q is not consumed by the runtime (only fleetly.databases is honored)", k)
		}
	}
	if lbl := strings.TrimSpace(svc.Labels["fleetly.databases"]); lbl != "" {
		refs, rerr := parseDatabaseRefs(field+".labels[fleetly.databases]", lbl)
		if rerr != nil {
			return nil, "", rerr
		}
		p.DatabaseRefs = refs
	}

	// 服务 configs（long-form source/target）→ config_mounts：内容由平台
	// Configs 当前版本经材料面下发（external 声明律已在顶层执法）。短形态
	//（无 target）拒收——挂载目标语义是本面的存在理由。
	if len(svc.Configs) > 0 {
		mounts := make([]*specv1.ConfigMount, 0, len(svc.Configs))
		for i, c := range svc.Configs {
			cf := field + ".configs[" + fmt.Sprintf("%d", i) + "]"
			if c.Target == "" {
				return nil, "", invalidf(cf, "long-form source/target is required (target is the container path the file mounts at; got source %q without target)", c.Source)
			}
			mounts = append(mounts, &specv1.ConfigMount{Config: c.Source, Target: c.Target})
		}
		p.ConfigMounts = mounts
	}

	ports, err := servicePorts(field+".ports", svc.Ports)
	if err != nil {
		return nil, "", err
	}
	p.Ports = ports

	if svc.Deploy != nil {
		if err := deployExtras(field+".deploy", svc.Deploy); err != nil {
			return nil, "", err
		}
		if svc.Deploy.Replicas != nil && *svc.Deploy.Replicas > 0 {
			p.Replicas = int64(*svc.Deploy.Replicas) //nolint:gosec // 副本计数域内
		}
		if err := deployStrategy(field+".deploy", svc.Deploy.Extensions, p); err != nil {
			return nil, "", err
		}
		res, err := deployResources(&svc.Deploy.Resources)
		if err != nil {
			return nil, "", err
		}
		p.Resources = res
	}

	probe, err := healthcheck(field+".healthcheck", svc.HealthCheck)
	if err != nil {
		return nil, "", err
	}
	p.Healthcheck = probe

	nets, err := serviceNetworks(field+".networks", svc.Networks)
	if err != nil {
		return nil, "", err
	}
	// 声明端口即 Route-facing（F3.5 两半边裁决）：ports 在场而 default
	// 未挂时补挂项目 default 网（Proxy 可达性半边）。spec_file 是用户
	// 亲笔 AppSpec，平台不改写其 networks——本规则只在 compose 方言生效。
	if len(p.Ports) > 0 && !containsString(nets, "default") {
		nets = append(nets, "default")
	}
	p.Networks = nets

	vols, err := serviceVolumes(field+".volumes", svc.Volumes, project.Volumes)
	if err != nil {
		return nil, "", err
	}
	p.Volumes = vols

	refs, err := serviceSecrets(field+".secrets", svc.Secrets, project.Secrets)
	if err != nil {
		return nil, "", err
	}
	p.SecretRefs = refs

	return p, svc.Image, nil
}

// rejectUnsupportedFields 用反射在 typed 产物上执法白名单：白名单外的
// 官方键一旦非零即拒（理由精确到键路径）。嵌入结构体（ContainerSpec/
// WorkloadSpec squash）递归展开；#extensions 通道单列（各块自己执法）。
// 上游新增字段默认落入拒绝面——白名单只增，扩面是显式行为。
func rejectUnsupportedFields(field string, v any, allowed map[string]bool) error {
	rt := reflect.TypeOf(v)
	rv := reflect.ValueOf(v)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("yaml")
		if strings.HasPrefix(tag, "#extensions") {
			continue // 扩展通道由各块单列执法
		}
		key := strings.Split(tag, ",")[0]
		if key == "" || key == "-" {
			if f.Anonymous {
				// inline squash 的嵌入结构体：递归展开字段面。
				if err := rejectUnsupportedFields(field, rv.Field(i).Interface(), allowed); err != nil {
					return err
				}
			}
			continue
		}
		if allowed[key] {
			continue
		}
		if !rv.Field(i).IsZero() {
			return invalidf(field+"."+key,
				"official compose field %q is not enabled on fleetly (supported: %s)", key, whitelistKeys(allowed))
		}
	}
	return nil
}

func serviceEnv(field string, env types.MappingWithEquals) (map[string]string, error) {
	if len(env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if v == nil {
			return nil, invalidf(field+"."+k, "environment entry %q carries no value (host-inherited entries are not translated; set a value or resolve from platform variables)", k)
		}
		out[k] = *v
	}
	return out, nil
}

func servicePorts(field string, ports []types.ServicePortConfig) ([]*specv1.PortSpec, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	out := make([]*specv1.PortSpec, 0, len(ports))
	for i, port := range ports {
		f := fmt.Sprintf("%s[%d]", field, i)
		if port.Published != "" || port.HostIP != "" {
			return nil, invalidf(f, "host port publishing is not supported (traffic enters through routes); declare the container port only, e.g. \"8080\" or {target: 8080}")
		}
		if port.Name != "" {
			return nil, invalidf(f+".name", "port names are not translated")
		}
		// Mode 是官方短语法解析器恒填的 swarm 发布模式缺省——发布本身
		// 已被拒，缺省值不构成声明；host 模式显式拒。
		if port.Mode == "host" {
			return nil, invalidf(f+".mode", "host-mode publishing is not supported")
		}
		if len(port.Extensions) > 0 {
			return nil, invalidf(f, "extension keys are not enabled on port entries")
		}
		if port.Target < 1 || port.Target > 65535 {
			return nil, invalidf(f, "target port %d out of range", port.Target)
		}
		switch port.Protocol {
		case "", "tcp":
		case "udp":
			return nil, invalidf(f+".protocol", "udp is not a routeable protocol (http, h2c or tcp)")
		default:
			return nil, invalidf(f+".protocol", "protocol %q must be tcp (route protocols are declared with app_protocol: http|h2c)", port.Protocol)
		}
		// 协议裁决：app_protocol 是官方面（应用协议提示语义恰是 Route 词
		// 表）——http|h2c|tcp 显式声明胜出；缺省 = HTTP（声明端口 =
		// Route-facing 意图，与 IR 缺省一致）。transport 字段（tcp/udp）
		// 不是路由词表：udp 拒，tcp 恒等价缺省（官方 fix 把裸端口规范化
		// 成 transport map，后缀显式性不可恢复——路由协议只走 app_protocol）。
		proto := specv1.Protocol_PROTOCOL_HTTP
		switch port.AppProtocol {
		case "":
		case "http":
		case "h2c":
			proto = specv1.Protocol_PROTOCOL_H2C
		case "tcp":
			proto = specv1.Protocol_PROTOCOL_TCP
		default:
			return nil, invalidf(f+".app_protocol", "app_protocol %q must be http, h2c or tcp", port.AppProtocol)
		}
		out = append(out, &specv1.PortSpec{Port: int32(port.Target), Protocol: proto}) //nolint:gosec // 域内
	}
	return out, nil
}

// deployExtras 执法 deploy 块的未开放官方面（策略/重启/滚动/调度是平台
// 状态机与 Placement 域的收编面，ADR-0048/D-MN-8）。
func deployExtras(field string, deploy *types.DeployConfig) error {
	if err := rejectUnsupportedFields(field, *deploy, deployAllowedKeys); err != nil {
		return err
	}
	if err := rejectUnsupportedFields(field+".resources", deploy.Resources, map[string]bool{"limits": true}); err != nil {
		return err
	}
	if deploy.Resources.Limits != nil {
		if err := rejectUnsupportedFields(field+".resources.limits", *deploy.Resources.Limits, map[string]bool{"cpus": true, "memory": true}); err != nil {
			return err
		}
	}
	return nil
}

// deployStrategy 执法 deploy.x-fleetly-strategy（官方 #extensions 通道）。
func deployStrategy(field string, ext map[string]any, p *specv1.ProcessSpec) error {
	for key := range ext {
		if key != "x-fleetly-strategy" {
			return invalidf(field+"."+key, "unknown extension key (only x-fleetly-strategy is defined under deploy)")
		}
	}
	if len(ext) == 0 {
		return nil
	}
	raw, ok := ext["x-fleetly-strategy"].(string)
	if !ok {
		return invalidf(field+".x-fleetly-strategy", "must be a string (rolling or blue-green)")
	}
	switch raw {
	case "rolling":
		p.Strategy = specv1.DeployStrategy_DEPLOY_STRATEGY_ROLLING
	case "blue-green":
		p.Strategy = specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN
	default:
		return invalidf(field+".x-fleetly-strategy", "strategy must be rolling or blue-green (got %q)", raw)
	}
	return nil
}

func deployResources(res *types.Resources) (*specv1.ResourcesSpec, error) {
	if res.Limits == nil {
		return nil, nil
	}
	out := &specv1.ResourcesSpec{}
	if cpus := float64(res.Limits.NanoCPUs); cpus > 0 {
		out.CpuMillis = int64(cpus * 1000) //nolint:gosec // 毫核域内
	}
	if bytes := int64(res.Limits.MemoryBytes); bytes > 0 {
		mb := int64(math.Ceil(float64(bytes) / 1048576))
		if mb < 1 {
			mb = 1
		}
		out.MemoryMb = mb
	}
	if out.CpuMillis == 0 && out.MemoryMb == 0 {
		return nil, nil
	}
	return out, nil
}

// healthcheck 执法并映射探针声明：官方 test（exec 探针，CMD/CMD-SHELL
// 方言归一——CMD-SHELL 载荷是单个 shell 字符串，staging 实证不可切分）+
// 官方节律键 + 扩展键 x-fleetly-http-path / x-fleetly-tcp-port（compose
// 无对应原语）。三选一互斥跨两源执法。
func healthcheck(field string, hc *types.HealthCheckConfig) (*specv1.HealthcheckSpec, error) {
	if hc == nil {
		return nil, nil
	}
	if err := rejectUnsupportedFields(field, *hc, healthcheckAllowedKeys); err != nil {
		return nil, err
	}
	out := &specv1.HealthcheckSpec{Retries: 3}
	hasHTTP, hasTCP := false, false
	for key, val := range hc.Extensions {
		switch key {
		case "x-fleetly-http-path":
			s, ok := val.(string)
			if !ok || s == "" {
				return nil, invalidf(field+"."+key, "must be a non-empty path string starting with '/'")
			}
			if !strings.HasPrefix(s, "/") {
				return nil, invalidf(field+"."+key, "http_path must be an absolute path starting with '/'")
			}
			out.Probe = &specv1.HealthcheckSpec_HttpPath{HttpPath: s}
			hasHTTP = true
		case "x-fleetly-tcp-port":
			n, err := anyToInt(val)
			if err != nil || n < 1 || n > 65535 {
				return nil, invalidf(field+"."+key, "must be a port number 1-65535")
			}
			out.Probe = &specv1.HealthcheckSpec_TcpPort{TcpPort: int32(n)} //nolint:gosec // 域内
			hasTCP = true
		default:
			return nil, invalidf(field+"."+key, "unknown extension key (only x-fleetly-http-path and x-fleetly-tcp-port are defined under healthcheck)")
		}
	}
	hasExec := len(hc.Test) > 0
	if (hasHTTP && hasTCP) || (hasHTTP && hasExec) || (hasTCP && hasExec) {
		return nil, invalidf(field, "probe is one of test, x-fleetly-http-path or x-fleetly-tcp-port")
	}
	if hasExec {
		words := make([]string, 0, len(hc.Test))
		words = append(words, hc.Test...)
		switch words[0] {
		case "CMD":
			out.Probe = &specv1.HealthcheckSpec_Exec{Exec: &specv1.ExecProbe{Command: words[1:]}}
		case "CMD-SHELL":
			// CMD-SHELL 载荷是单个 shell 字符串：按空白切分会撕碎引号结构
			//（staging 真机实证，ADR-0033 注记）——整体经 sh -c 承载。
			out.Probe = &specv1.HealthcheckSpec_Exec{Exec: &specv1.ExecProbe{Command: []string{"sh", "-c", strings.Join(words[1:], " ")}}}
		default:
			return nil, invalidf(field+".test", "test must start with CMD or CMD-SHELL")
		}
	}
	if hc.Interval != nil {
		out.Interval = durationpb.New(time.Duration(*hc.Interval))
	}
	if hc.Timeout != nil {
		out.Timeout = durationpb.New(time.Duration(*hc.Timeout))
	}
	if hc.StartPeriod != nil {
		out.StartPeriod = durationpb.New(time.Duration(*hc.StartPeriod))
	}
	if hc.Retries != nil && *hc.Retries > 0 {
		out.Retries = int32(*hc.Retries) //nolint:gosec // 计数域内
	}
	if out.Probe == nil {
		return nil, invalidf(field, "healthcheck requires one of test, x-fleetly-http-path or x-fleetly-tcp-port")
	}
	return out, nil
}

// serviceNetworks 执法网络附件：名单形态（nil 配置）；带 aliases/ipv4 等
// 长语法配置显式拒绝。跨 Project 引用形态（project:<id>/<name>）带冒号
// 斜杠，官方 schema 的网络键字符集不容——该面属 spec_file（方言边界，
// ADR-0074 注记）。
func serviceNetworks(field string, nets map[string]*types.ServiceNetworkConfig) ([]string, error) {
	if len(nets) == 0 {
		return nil, nil
	}
	for name, cfg := range nets {
		if cfg != nil {
			return nil, invalidf(field+"."+name, "network attachments are by name only (aliases, ipv4_address and driver_opts are not translated)")
		}
	}
	out := make([]string, 0, len(nets))
	for name := range nets {
		out = append(out, name)
	}
	sort.Strings(out) // map → 切片落点排序归一（幂等 diff 稳定）
	return out, nil
}

// serviceVolumes 执法卷挂载：短语法 name:/target[:ro]（官方解析承载），
// 名解析经顶层 external 声明（name 覆盖形态：声明键 → 平台卷名）。
func serviceVolumes(field string, vols []types.ServiceVolumeConfig, declared map[string]types.VolumeConfig) ([]*specv1.VolumeAttachment, error) {
	if len(vols) == 0 {
		return nil, nil
	}
	out := make([]*specv1.VolumeAttachment, 0, len(vols))
	for i, v := range vols {
		f := fmt.Sprintf("%s[%d]", field, i)
		if v.Type != "" && v.Type != "volume" {
			return nil, invalidf(f, "volume type %q is not supported; mount a platform volume with the short syntax \"name:/target[:ro]\"", v.Type)
		}
		// 官方短语法解析会物化空 Volume 子结构（命名卷缺省形态）——只有
		// 非零子配置（nocopy/propagation/tmpfs 等）才是长语法选项面。
		if v.Bind != nil || v.Tmpfs != nil || v.Image != nil || (v.Volume != nil && (v.Volume.NoCopy || v.Volume.Subpath != "" || len(v.Volume.Labels) > 0)) {
			return nil, invalidf(f, "long-syntax volume options are not translated; use \"name:/target[:ro]\" with an external declaration")
		}
		if v.Source == "" {
			return nil, invalidf(f, "anonymous volumes are not supported; mount a named platform volume")
		}
		if !strings.HasPrefix(v.Target, "/") {
			return nil, invalidf(f, "volume target %q must be an absolute container path", v.Target)
		}
		name := v.Source
		if decl, ok := declared[v.Source]; ok && decl.Name != "" {
			name = decl.Name
		}
		out = append(out, &specv1.VolumeAttachment{VolumeId: name, Target: v.Target, ReadOnly: v.ReadOnly})
	}
	return out, nil
}

// serviceSecrets 执法 Secret 引用：名引用（值永不进 compose，ADR-0014）；
// 目标路径只认官方缺省 /run/secrets/<名>（重映射拒）；uid/gid/mode 不翻译。
func serviceSecrets(field string, secs []types.ServiceSecretConfig, declared map[string]types.SecretConfig) ([]string, error) {
	if len(secs) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(secs))
	for i, s := range secs {
		f := fmt.Sprintf("%s[%d]", field, i)
		if s.Source == "" {
			return nil, invalidf(f, "secret entry must reference a declared secret by name")
		}
		if s.Target != "" && s.Target != path.Join("/run/secrets", s.Source) {
			return nil, invalidf(f, "secret %q: target remapping is not supported (injected at /run/secrets/<name>)", s.Source)
		}
		if s.UID != "" || s.GID != "" || s.Mode != nil {
			return nil, invalidf(f, "secret %q: uid/gid/mode are not translated", s.Source)
		}
		name := s.Source
		if decl, ok := declared[s.Source]; ok && decl.Name != "" {
			name = decl.Name
		}
		out = append(out, name)
	}
	return out, nil
}

// firstBootJobs 翻译顶层 x-fleetly-first-boot-jobs（ADR-0033 形态迁移：
// 键白名单 + 禁面理由 + 官方插值已消解值）。secrets 引用与 service 面
// 同律——名必须落在顶层 external 声明集内。
func firstBootJobs(project *types.Project, raw any) ([]*specv1.JobSpec, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, invalidf("compose.x-fleetly-first-boot-jobs", "must be a list of job mappings")
	}
	jobs := make([]*specv1.JobSpec, 0, len(list))
	for i, rawJob := range list {
		field := fmt.Sprintf("compose.x-fleetly-first-boot-jobs[%d]", i)
		m, ok := rawJob.(map[string]any)
		if !ok {
			return nil, invalidf(field, "job must be a mapping")
		}
		for key := range m {
			if firstBootJobWhitelist[key] {
				continue
			}
			if reason, banned := firstBootJobRejections[key]; banned {
				return nil, invalidf(field+"."+key, "%s", reason)
			}
			return nil, invalidf(field+"."+key,
				"unsupported field %q for a first boot job (supported: %s)", key, whitelistKeys(firstBootJobWhitelist))
		}
		name, _ := m["name"].(string)
		if name == "" {
			return nil, invalidf(field+".name", "must be a non-empty string")
		}
		image, _ := m["image"].(string)
		if image == "" {
			return nil, invalidf(field+".image", "must be a non-empty image reference (from_build jobs are not reachable from compose)")
		}
		p := &specv1.ProcessSpec{Name: name, ImageOrigin: &specv1.ProcessSpec_Image{Image: image}}
		cmd, err := jobCommand(field+".command", m["command"])
		if err != nil {
			return nil, err
		}
		p.Command = cmd
		env, err := jobEnv(field+".environment", m["environment"])
		if err != nil {
			return nil, err
		}
		p.Env = env
		refs, err := jobSecretRefs(field+".secrets", m["secrets"], project.Secrets)
		if err != nil {
			return nil, err
		}
		p.SecretRefs = refs
		j := &specv1.JobSpec{Name: name, Process: p}
		if rawTTL := m["ttl"]; rawTTL != nil {
			s, ok := rawTTL.(string)
			if !ok {
				return nil, invalidf(field+".ttl", "duration must be a string like \"30s\"")
			}
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				return nil, invalidf(field+".ttl", "duration %q must be positive (e.g. \"30s\")", s)
			}
			j.Ttl = durationpb.New(d)
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func jobCommand(field string, raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return strings.Fields(v), nil
	case []any:
		argv := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok || s == "" {
				return nil, invalidf(fmt.Sprintf("%s[%d]", field, i), "list-form command entries must be non-empty strings")
			}
			argv = append(argv, s)
		}
		return argv, nil
	default:
		return nil, invalidf(field, "command must be a string or a list of strings")
	}
}

func jobEnv(field string, raw any) (map[string]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		env := make(map[string]string, len(v))
		for k, val := range v {
			if val == nil {
				return nil, invalidf(field+"."+k, "environment entry %q carries no value", k)
			}
			env[k] = fmt.Sprintf("%v", val)
		}
		return env, nil
	case []any:
		env := make(map[string]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, invalidf(fmt.Sprintf("%s[%d]", field, i), "list-form environment entries must be strings like \"KEY=value\"")
			}
			k, val, found := strings.Cut(s, "=")
			if !found || k == "" {
				return nil, invalidf(fmt.Sprintf("%s[%d]", field, i), "list-form environment entry %q must be \"KEY=value\" with a non-empty key", s)
			}
			env[k] = val
		}
		return env, nil
	default:
		return nil, invalidf(field, "environment must be a mapping or a list of \"KEY=value\" strings")
	}
}

func jobSecretRefs(field string, raw any, declared map[string]types.SecretConfig) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	secs, ok := raw.([]any)
	if !ok {
		return nil, invalidf(field, "secrets must be a list of names or {source: name} mappings")
	}
	refs := make([]string, 0, len(secs))
	for _, entry := range secs {
		var name string
		switch v := entry.(type) {
		case string:
			name = v
		case map[string]any:
			for key := range v {
				if key != "source" && key != "target" {
					return nil, invalidf(field,
						"secret key %q is not supported (long syntax supports source only; uid/gid/mode are not translated)", key)
				}
			}
			name, _ = v["source"].(string)
			if target, ok := v["target"].(string); ok && target != "" && target != name {
				return nil, invalidf(field, "secret %q: target remapping is not supported (injected at /run/secrets/<name>)", name)
			}
		}
		if name == "" {
			return nil, invalidf(field, "secret entry must be a name or {source: name}")
		}
		decl, ok := declared[name]
		if !ok {
			return nil, invalidf(field, "secret %q is not declared (declare it top-level with external: true)", name)
		}
		// 引用声明键、解析平台名（name 覆盖形态：冒号等平台命名进不了
		// schema 键字符集，经 key → name 覆盖表达）。
		if decl.Name != "" {
			name = decl.Name
		}
		refs = append(refs, name)
	}
	return refs, nil
}

// anyToInt 归一 YAML 数值形态（int/int64/float64）。
func anyToInt(val any) (int64, error) {
	switch v := val.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	}
	return 0, fmt.Errorf("not a number")
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func whitelistKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k, ok := range m {
		if ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func invalidf(field, format string, args ...any) *spec.ValidationError {
	return &spec.ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

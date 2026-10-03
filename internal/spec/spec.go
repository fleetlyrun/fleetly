// Package spec 承接 IR 的校验、归一化与包装（原始生成类型住 genproto
// module，tech-stack §2）。叶子纯度：不 import 任何 fleetly internal 包
// （守卫见 internal/guards）。
package spec

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// SchemaVersion 是当前 IR 版本（读入 check-strategy：可读旧版+提示，
// 拒绝跳代——只升不降，架构 §3）。
const SchemaVersion int32 = 1

// ValidationError 是校验失败（字段路径 + 理由；调用方转 apperr）。
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func invalidf(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// ValidateApp 校验 AppSpec（归一化后的期望状态）。
func ValidateApp(s *specv1.AppSpec) error {
	if s == nil {
		return invalidf("app", "spec is nil")
	}
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("app.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if s.GetApp().GetId() == "" {
		return invalidf("app.id", "must not be empty")
	}
	if s.GetApp().GetProject() == "" {
		return invalidf("app.project", "must not be empty")
	}
	if err := validateSource(s.GetSource()); err != nil {
		return err
	}
	if err := ValidateBuild("app.build", s.GetBuild()); err != nil {
		return err
	}
	if len(s.GetProcesses()) == 0 {
		return invalidf("app.processes", "at least one process is required")
	}
	seen := map[string]bool{}
	for i, p := range s.GetProcesses() {
		if err := ValidateProcess(fmt.Sprintf("app.processes[%d]", i), p); err != nil {
			return err
		}
		if seen[p.GetName()] {
			return invalidf("app.processes[%d].name", "duplicate process name %q", p.GetName())
		}
		seen[p.GetName()] = true
	}
	jobNames := map[string]bool{}
	for i, j := range s.GetFirstBootJobs() {
		if err := ValidateJob(fmt.Sprintf("app.first_boot_jobs[%d]", i), j); err != nil {
			return err
		}
		if jobNames[j.GetName()] {
			return invalidf("app.first_boot_jobs[%d].name", "duplicate first boot job name %q", j.GetName())
		}
		jobNames[j.GetName()] = true
	}
	return nil
}

// ValidateJob 校验部署期一次性作业（firstBootJobs，ADR-0030）。禁面
// fail-closed：Task 域不渲染的面（卷/config）与一次性执行无意义的面
// （探针/端口/placement/多副本）一律拒绝且理由精确；网络三形态与 App
// Process 同域（缺省挂靠由引擎铸造面裁决——全部活跃项目网）。
func ValidateJob(field string, j *specv1.JobSpec) error {
	if j == nil {
		return invalidf(field, "job is nil")
	}
	if j.GetName() == "" {
		return invalidf(field+".name", "must not be empty")
	}
	p := j.GetProcess()
	if p == nil {
		return invalidf(field+".process", "process template is required (C-13 reshaped form)")
	}
	switch origin := p.GetImageOrigin().(type) {
	case *specv1.ProcessSpec_Image:
		if origin.Image == "" {
			return invalidf(field+".process.image", "must not be empty")
		}
	case *specv1.ProcessSpec_FromBuild:
		if origin.FromBuild == "" {
			return invalidf(field+".process.from_build", "must not be empty")
		}
	default:
		return invalidf(field+".process", "image or from_build is required")
	}
	// process.name 空 = 铸造时落 job.name（单一名字真源）；非空须相等，
	// 静默分叉比拒绝更糟。
	if n := p.GetName(); n != "" && n != j.GetName() {
		return invalidf(field+".process.name", "must be empty or equal to the job name %q (got %q)", j.GetName(), n)
	}
	// ttl 必填（ADR-0030 决策 4）：无界等待的部署不是合法状态。
	ttl := j.GetTtl().AsDuration()
	if ttl <= 0 || ttl > 86400*time.Second {
		return invalidf(field+".ttl", "deploy-time jobs must declare a hard timeout within (0s, 86400s] (got %s)", ttl)
	}
	if len(p.GetVolumes()) > 0 {
		return invalidf(field+".process.volumes", "deploy-time jobs cannot mount volumes; use a Task for volume-mounting jobs")
	}
	if len(p.GetConfigRefs()) > 0 {
		return invalidf(field+".process.config_refs", "deploy-time jobs cannot mount configs; pass what the job needs via env or secret_refs")
	}
	if len(p.GetPorts()) > 0 {
		return invalidf(field+".process.ports", "ports are not meaningful on a one-shot job")
	}
	if p.GetHealthcheck() != nil {
		return invalidf(field+".process.healthcheck", "healthchecks are not meaningful on a one-shot job")
	}
	if p.GetPlacement() != nil {
		return invalidf(field+".process.placement", "placement is not supported on deploy-time jobs")
	}
	if p.GetReplicas() > 1 {
		return invalidf(field+".process.replicas", "a deploy-time job is a single one-shot run")
	}
	for i, net := range p.GetNetworks() {
		if net == "" {
			return invalidf(fmt.Sprintf("%s.process.networks[%d]", field, i), "must not be empty")
		}
		if IsCrossProjectRef(net) {
			if err := validateCrossProjectRef(fmt.Sprintf("%s.process.networks[%d]", field, i), net); err != nil {
				return err
			}
		}
	}
	return nil
}

// BuilderNames 是 builder 名值域（CONTEXT.md Builder 词条单源冻结：
// dockerfile、railpack、static；词条变更走 ADR——叶子无注册表面，值域
// 漂移=词汇漂移）。名与 strategy oneof 形态一一配对。
const (
	BuilderDockerfile = "dockerfile"
	BuilderRailpack   = "railpack"
	BuilderStatic     = "static"
)

// bareSemverRe 钉死 railpack 钉版形态（bare semver：拒 v 前缀/latest/空，
// ADR-0032——钉版可被漂移形态绕过等于没钉）。
var bareSemverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// staticOutputDirRe 钉死 static output_dir 字符集（N1 收尾批 B11）：产物
// 目录内插进生成 Dockerfile 的 COPY 指令并进 dockerfile.v0 前端路径面
// ——`-` 开头可被解析为旗标，空白/Dockerfile 语法元字符破坏指令语法；
// 白名单拒掉整面。与 validateRelPath 的结构逃逸检查互补（形态 × 结构
// 双轴）。ValidateBuild 受理执法 + builders 翻译面防御纵深共用本真源。
const StaticOutputDirPattern = `[A-Za-z0-9._/-]+`

var staticOutputDirRe = regexp.MustCompile(`^` + StaticOutputDirPattern + `$`)

// ValidStaticOutputDir 报告产物目录是否落在白名单字符集且不以 "-" 开头
// （`-` 开头在 Dockerfile 指令里是旗标位）。
func ValidStaticOutputDir(dir string) bool {
	return staticOutputDirRe.MatchString(dir) && !strings.HasPrefix(dir, "-")
}

// ValidateBuild 校验构建声明（ADR-0032）：builder 名值域与 strategy 配对、
// railpack pinned_version bare semver、static output_dir 路径逃逸
// fail-closed（tar-slip 同理）。nil = 无构建声明（镜像直投），合法。
// strategy oneof 缺席=非法（归一化面恒填充；存量行防御性路由在 engine）。
func ValidateBuild(field string, b *specv1.BuildSpec) error {
	if b == nil {
		return nil
	}
	name := b.GetBuilder()
	require := func(want string) error {
		if name != want {
			return invalidf(field+".builder", "strategy %q requires builder %q (builder name and strategy form must pair)", want, want)
		}
		return nil
	}
	switch strategy := b.GetStrategy().(type) {
	case *specv1.BuildSpec_Dockerfile:
		if err := require(BuilderDockerfile); err != nil {
			return err
		}
		// 空 = 平台缺省 Dockerfile（Provider 同语义）；路径逃逸仍 fail-closed。
		if err := validateRelPath(field+".dockerfile", strategy.Dockerfile); err != nil {
			return err
		}
	case *specv1.BuildSpec_Railpack:
		if err := require(BuilderRailpack); err != nil {
			return err
		}
		v := strategy.Railpack.GetPinnedVersion()
		if !bareSemverRe.MatchString(v) {
			return invalidf(field+".railpack.pinned_version",
				"must be a bare semver like \"0.39.0\" (got %q; no v prefix, no latest — pinning prevents build-plan drift)", v)
		}
	case *specv1.BuildSpec_Static:
		if err := require(BuilderStatic); err != nil {
			return err
		}
		out := strategy.Static.GetOutputDir()
		if out == "" {
			return invalidf(field+".static.output_dir", "must not be empty; use \".\" for the context root")
		}
		if err := validateRelPath(field+".static.output_dir", out); err != nil {
			return err
		}
		// 字符集白名单（N1 收尾批 B11）：output_dir 内插进生成 Dockerfile
		// 的 COPY 指令——`-` 开头会被解析为旗标、空白/元字符破坏指令
		// 语法；白名单在受理入口拒绝（builder 侧 clean 面防御纵深）。
		if !ValidStaticOutputDir(out) {
			return invalidf(field+".static.output_dir",
				"may only contain letters, digits, dot, slash and underscore and must not start with \"-\" (got %q; it is interpolated into a COPY instruction)", out)
		}
	default:
		return invalidf(field, "a strategy is required (dockerfile, railpack or static)")
	}
	for i, cf := range b.GetCacheFrom() {
		if strings.TrimSpace(cf) == "" {
			return invalidf(fmt.Sprintf("%s.cache_from[%d]", field, i), "must not be empty")
		}
	}
	return nil
}

// validateRelPath 校验上下文内相对路径（空=合法缺省；拒绝对路径与 ..
// 逃逸；反斜杠一并拒——Windows 形态路径在 Linux 上下文里只会静默失配）。
func validateRelPath(field, p string) error {
	if p == "" {
		return nil
	}
	if strings.Contains(p, "\\") {
		return invalidf(field, "must use forward slashes (got %q)", p)
	}
	clean := path.Clean(p)
	if path.IsAbs(p) || clean == ".." || strings.HasPrefix(clean, "../") {
		return invalidf(field, "must stay inside the build context (got %q)", p)
	}
	return nil
}

// imageRefForbiddenRe 匹配镜像引用中的注入/荒谬字符（P2 注入守卫，docs/
// design/2026-10-03-optimization-proposals.md）：控制字符、空白与反引号。
// 完整 reference 语法归 daemon/registry 裁决——这里只堵 shell/配置定界的
// 逃逸面与不可传输形态（ref 经 daemon API 与配置文件传播，永不进 shell）。
var imageRefForbiddenRe = regexp.MustCompile("[\\s\\x00-\\x1f\\x7f`]")

// validateImageRef 校验镜像引用（Source.Image.Ref 与进程 image 同口径）。
func validateImageRef(field, ref string) error {
	if ref == "" {
		return invalidf(field, "must not be empty")
	}
	if bad := imageRefForbiddenRe.FindString(ref); bad != "" {
		return invalidf(field, "image reference %q contains forbidden character %q", ref, bad)
	}
	return nil
}

// ValidateProcess 校验单个进程模板（TaskSpec 单元素复用同口径）。
func ValidateProcess(field string, p *specv1.ProcessSpec) error {
	if p == nil {
		return invalidf(field, "process is nil")
	}
	if p.GetName() == "" {
		return invalidf(field+".name", "must not be empty")
	}
	// 进程名字符集白名单（N1 收尾批 B9）：名字直接进 swarm 服务名与 DNS
	// 面，白名单外形态经载体名 sanitize 折叠（web.1 与 web-1 同名）——
	// 静默丢失一进程，入口即拒。
	if !processNameRe.MatchString(p.GetName()) {
		return invalidf(field+".name",
			"process name %q must match %q (names become swarm service names; use lowercase letters, digits and inner hyphens)", p.GetName(), processNamePattern)
	}
	switch origin := p.GetImageOrigin().(type) {
	case *specv1.ProcessSpec_Image:
		if err := validateImageRef(field+".image", origin.Image); err != nil {
			return err
		}
	case *specv1.ProcessSpec_FromBuild:
		if origin.FromBuild == "" {
			return invalidf(field+".from_build", "must not be empty")
		}
	default:
		return invalidf(field, "image or from_build is required")
	}
	for i, port := range p.GetPorts() {
		if port.GetPort() < 1 || port.GetPort() > 65535 {
			return invalidf(fmt.Sprintf("%s.ports[%d].port", field, i), "port %d out of range", port.GetPort())
		}
		switch port.GetProtocol() {
		case specv1.Protocol_PROTOCOL_HTTP, specv1.Protocol_PROTOCOL_H2C, specv1.Protocol_PROTOCOL_TCP:
		default:
			return invalidf(fmt.Sprintf("%s.ports[%d].protocol", field, i), "protocol must be http, h2c or tcp")
		}
	}
	if p.GetReplicas() < 0 {
		return invalidf(field+".replicas", "must not be negative")
	}
	if err := validateHealthcheck(field+".healthcheck", p.GetHealthcheck()); err != nil {
		return err
	}
	for i, net := range p.GetNetworks() {
		if net == "" {
			return invalidf(fmt.Sprintf("%s.networks[%d]", field, i), "must not be empty")
		}
		// 跨 Project 引用（ADR-0013 附录 A.2）：形态在此校验（叶子包无
		// DB 面），存在性与批准态由受理/投影面裁决。
		if IsCrossProjectRef(net) {
			if err := validateCrossProjectRef(fmt.Sprintf("%s.networks[%d]", field, i), net); err != nil {
				return err
			}
		}
	}
	// Secret 引用名字符集（N1 收尾批 A3）：引用名原样成为容器内
	// /run/secrets/<名> 的文件目标（swarm translate 原样透传）——白名单是
	// 路径逃逸的唯一入口防线（/、\、空白、控制字符与 ".." 全拒；冒号合法：
	// 平台数据库凭证名 database:<name> 是既定形态，ADR-0029）。
	for i, ref := range p.GetSecretRefs() {
		if ref == "" {
			return invalidf(fmt.Sprintf("%s.secret_refs[%d]", field, i), "must not be empty")
		}
		if !ValidSecretName(ref) {
			return invalidf(fmt.Sprintf("%s.secret_refs[%d]", field, i),
				"secret name %q must match %q and must not contain \"..\" (secret names become /run/secrets/<name> paths)", ref, SecretNamePattern)
		}
	}
	return nil
}

func validateHealthcheck(field string, h *specv1.HealthcheckSpec) error {
	if h == nil {
		return nil // 探针可选
	}
	kinds := 0
	if h.GetHttpPath() != "" {
		kinds++
	}
	if h.GetTcpPort() != 0 {
		kinds++
	}
	if h.GetExec() != nil {
		kinds++
	}
	if kinds == 0 {
		return invalidf(field, "one of http_path, tcp_port or exec is required")
	}
	if kinds > 1 {
		return invalidf(field, "at most one probe kind is allowed")
	}
	if h.GetRetries() < 0 {
		return invalidf(field+".retries", "must not be negative")
	}
	return nil
}

func validateSource(src *specv1.Source) error {
	switch origin := src.GetKind().(type) {
	case *specv1.Source_Git:
		if origin.Git.GetRepo() == "" {
			return invalidf("app.source.git.repo", "must not be empty")
		}
		if origin.Git.GetRef() == "" {
			return invalidf("app.source.git.ref", "must not be empty")
		}
	case *specv1.Source_Image:
		if err := validateImageRef("app.source.image.ref", origin.Image.GetRef()); err != nil {
			return err
		}
	case *specv1.Source_Upload:
		if origin.Upload.GetId() == "" {
			return invalidf("app.source.upload.id", "must not be empty")
		}
	default:
		return invalidf("app.source", "one of git, image or upload is required")
	}
	return nil
}

// ValidateTask 校验 TaskSpec（F1.5，ADR-0012/0025）。
func ValidateTask(s *specv1.TaskSpec) error {
	if s == nil {
		return invalidf("task", "spec is nil")
	}
	if s.GetTask().GetId() == "" {
		return invalidf("task.id", "must not be empty")
	}
	if s.GetTask().GetProject() == "" {
		return invalidf("task.project", "must not be empty")
	}
	return validateTaskCommon(s)
}

// ValidateTaskTemplate 校验 Schedule 的冻结 TaskSpec 模板（F1.7，
// ADR-0018）：与 ValidateTask 同规则，唯 task ref 缺席——身份锚由
// Schedule 行提供（project 在行上、task id 由 fire 时铸造）。
func ValidateTaskTemplate(s *specv1.TaskSpec) error {
	if s == nil {
		return invalidf("task", "spec is nil")
	}
	return validateTaskCommon(s)
}

// validateTaskCommon 是两校验面的共用体（ref 在场性由各自入口裁决）。
func validateTaskCommon(s *specv1.TaskSpec) error {
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("task.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if err := ValidateProcess("task.process", s.GetProcess()); err != nil {
		return err
	}
	// Task 的网络附件只有 network_group 一条轨（Run 创建时刻挂靠，
	// ADR-0012）；process.networks 是 App Process 的跨挂面，TaskSpec 禁用。
	if len(s.GetProcess().GetNetworks()) > 0 {
		return invalidf("task.process.networks", "tasks attach via network_group only (process.networks is an app-process surface)")
	}
	// ADR-0018：TTL 上限 86400s。
	if ttl := s.GetTtlSeconds(); ttl < 0 || ttl > 86400 {
		return invalidf("task.ttl_seconds", "must be within [0, 86400]")
	}
	if s.GetDesiredConcurrency() < 0 {
		return invalidf("task.desired_concurrency", "must not be negative")
	}
	// 双形态显式声明时校验值；空 = 按 desired_concurrency 推导。
	switch form := TaskForm(s); form {
	case FormOneShot:
		if s.GetDesiredConcurrency() > 1 {
			return invalidf("task.form", "one-shot tasks cannot declare desired_concurrency > 1")
		}
	case FormResident:
	default:
		return invalidf("task.form", "must be one of \"one-shot\" or \"resident\" (got %q)", s.GetForm())
	}
	if g := s.GetNetworkGroup(); g != "" && !validNetworkGroupName(g) {
		return invalidf("task.network_group", "must match %[1]s (got %q)", networkGroupNamePattern, g)
	}
	return nil
}

// TaskForm 返回解析后的双形态（空声明按 desired_concurrency 推导：>1 即
// resident，否则 one-shot）。
func TaskForm(s *specv1.TaskSpec) string {
	if f := s.GetForm(); f != "" {
		return f
	}
	if s.GetDesiredConcurrency() > 1 {
		return FormResident
	}
	return FormOneShot
}

// Task 双形态常量（存储/事件值 kebab-case，与 Deployment.state 同款拼写冻结）。
const (
	FormOneShot  = "one-shot"
	FormResident = "resident"
)

// networkGroupNamePattern 钉死网络组名格式（DNS label 安全：组名直接进
// 平台网络名与 DNS 面；首尾须为字母数字）。
const networkGroupNamePattern = `[a-z0-9]([a-z0-9-]{0,36}[a-z0-9])?`

var networkGroupNameRe = regexp.MustCompile(`^` + networkGroupNamePattern + `$`)

func validNetworkGroupName(g string) bool { return networkGroupNameRe.MatchString(g) }

// processNamePattern 钉死进程名字符集（N1 收尾批 B9）：进程名进 swarm
// 服务名与平台 DNS 面——仅差特殊字符的名字（web.1 / web-1）经载体名
// sanitize 折叠成同名载体，一进程无声丢失；白名单在 spec 入口拒绝折叠
// 源头。形态与网络组名同款 DNS label；TaskSpec 进程名由平台铸造为
// "run"（合法），first_boot_jobs 的进程名铸时落 job 名且走 Task 域
// （载体名不含进程名），均不受影响。
const processNamePattern = networkGroupNamePattern

var processNameRe = regexp.MustCompile(`^` + processNamePattern + `$`)

// SecretNamePattern 钉死 Secret 名字符集（N1 收尾批 A3：防路径逃逸——
// 引用名原样成为容器内 /run/secrets/<名> 文件目标与 swarm 载体名成分，
// /、\、空白与控制字符全拒；首字符字母数字挡住 "." 与 "-" 开头形态；
// 冒号合法=平台数据库凭证名 database:<name> 既定形态（ADR-0029）；长度
// 上界 64 对齐载体名预算）。PutSecret 受理面与 spec 校验（compose secrets
// / task secret_refs）共用本真源。
const SecretNamePattern = `[A-Za-z0-9][A-Za-z0-9:._-]{0,63}` //nolint:gosec // 字符集正则字面量, 非凭证值

var secretNameRe = regexp.MustCompile(`^` + SecretNamePattern + `$`)

// ValidSecretName 报告 Secret 名是否落在白名单字符集且不含 ".."（无 "/"
// 时 ".." 只能以子串形态出现——仍拒：好名字不疼，坏名字不赌）。
func ValidSecretName(name string) bool {
	return secretNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// ValidateDatabase 校验 DatabaseSpec。
func ValidateDatabase(s *specv1.DatabaseSpec) error {
	if s == nil {
		return invalidf("database", "spec is nil")
	}
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("database.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if s.GetDatabase().GetId() == "" {
		return invalidf("database.id", "must not be empty")
	}
	if s.GetEngine() == "" {
		return invalidf("database.engine", "must not be empty")
	}
	if s.GetCredentialsRef() == "" {
		return invalidf("database.credentials_ref", "must reference a project secret")
	}
	return nil
}

// IsNetworkGroupRef 报告网络附件是否 Task Network Group 跨挂形态
// （taskGroup:<name>）。
func IsNetworkGroupRef(net string) bool {
	return strings.HasPrefix(net, "taskGroup:")
}

// NetworkGroupName 剥离前缀返回组名。
func NetworkGroupName(net string) string {
	return strings.TrimPrefix(net, "taskGroup:")
}

// crossProjectPrefix 是跨 Project 引用前缀（ADR-0013 附录 A.2：spec
// networks 第三形态，project 段一律平台 ID——名字跨 Team 可同名，不可作锚）。
const crossProjectPrefix = "project:"

// platformIDRe 钉死平台 ID 形态（26 位大写字母数字——newID 铸造的 ULID
// 满足；比 Crockford 全集宽一位字母面：仓内 fixture 惯用 01JD0PROJ… 形）。
var platformIDRe = regexp.MustCompile(`^[0-9A-Z]{26}$`)

// IsCrossProjectRef 报告网络附件是否跨 Project 引用形态
// （project:<project-id>/<network-name>）。
func IsCrossProjectRef(net string) bool {
	return strings.HasPrefix(net, crossProjectPrefix)
}

// CrossProjectRefParts 解析跨 Project 引用；非本形态 ok=false。
func CrossProjectRefParts(net string) (projectID, networkName string, ok bool) {
	projectID, networkName, found := strings.Cut(strings.TrimPrefix(net, crossProjectPrefix), "/")
	if !found {
		return "", "", false
	}
	return projectID, networkName, true
}

func validateCrossProjectRef(field, net string) error {
	projectID, networkName, ok := CrossProjectRefParts(net)
	if !ok {
		return invalidf(field, "cross-project reference must be \"project:<project-id>/<network-name>\"")
	}
	if !platformIDRe.MatchString(projectID) {
		return invalidf(field, "project part must be a platform project id (26-char uppercase), not a project name (names are only unique within a team)")
	}
	if networkName == "" {
		return invalidf(field, "network name part must not be empty")
	}
	return nil
}

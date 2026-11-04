package composeintake

// pre-deploy job 受理面（ADR-0033 两种声明形态 + ADR-0098 序语义）：
// 顶层 x-fleetly-pre-deploy-jobs 列表与服务 label 方言（fleetly.job: init）
// 的翻译/执法/收集，以及 after 依赖承载的确定性拓扑排（断环 fail-closed）。
// FromYAML 是唯一调用方（受理喉单源）；序的解析产物 = preDeployJobs
// repeated 位次（spec 面零依赖字段）。

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"google.golang.org/protobuf/types/known/durationpb"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// jobDialectAllowedKeys 是服务 label 作业方言（fleetly.job: init）的服务面
// 白名单（只增）：消费面 = image/command/environment/secrets/configs 五族 +
// labels（方言载体：fleetly.job/fleetly.job.timeout/fleetly.job.after/
// fleetly.databases）+ name（Transform 自动填充的键名镜像，放行零值判定）。
// 指向性拒绝面七键（volumes/ports/networks/depends_on/restart/deploy/build）
// 在白名单前先行执法——supported 名单只列真消费面；其余官方面（entrypoint/
// user/working_dir/privileged/cap_add/healthcheck 等）显式拒，静默丢即静默
// 失败（ADR-0074）。
var jobDialectAllowedKeys = map[string]bool{
	"name":        true,
	"image":       true,
	"command":     true,
	"environment": true,
	"secrets":     true,
	"configs":     true,
	"labels":      true,
}

// preDeployJobWhitelist 与 preDeployJobRejections 沿 ADR-0033 原面迁移
// （composePreDeployJobs 的键白名单与禁面理由）。
var preDeployJobWhitelist = map[string]bool{
	"name":        true,
	"image":       true,
	"command":     true,
	"environment": true,
	"secrets":     true,
	"ttl":         true,
	"after":       true, // ADR-0098 序承载：依赖名列表（受理时消费，不进冻结体）
}

var preDeployJobRejections = map[string]string{
	"volumes":     "deploy-time jobs cannot mount volumes; use a Task for volume-mounting jobs",
	"configs":     "deploy-time jobs cannot mount configs; pass what the job needs via environment or secrets",
	"ports":       "ports are not meaningful on a one-shot job",
	"healthcheck": "healthchecks are not meaningful on a one-shot job",
	"placement":   "placement is not supported on deploy-time jobs",
	"replicas":    "a deploy-time job is a single one-shot run",
	"networks":    "deploy-time jobs attach to every active project network by default (db-<id> included); narrowing is not declarable from compose",
	"depends_on":  "declare ordering with after (a list of job names); compose depends_on is not consumed on deploy-time jobs (they run serially in the resolved order)",
}

// initJobFromService 翻译一次性作业的服务 label 方言（fleetly.job: init）：
// image/command（$ 透传，预逃逸统一在官方加载前，与服务面同口径）/
// environment/secrets/configs/fleetly.databases → JobSpec；fleetly.job.timeout
// → ttl；fleetly.job.after → after 承载（ADR-0098，受理时消费不进
// JobSpec）。拒绝面：volumes/ports/networks/depends_on/restart/deploy/build
// 各带指向性文案（preDeployJobRejections 同措辞）；其余官方面与服务级
// x- 扩展键经白名单收口显式拒（静默丢即静默失败，ADR-0074）。
func initJobFromService(field, name string, svc types.ServiceConfig, project *types.Project) (jobDecl, error) {
	for _, key := range sortedKeys(svc.Labels) {
		switch key {
		case "fleetly.job", "fleetly.job.timeout", "fleetly.job.after", "fleetly.databases":
		default:
			return jobDecl{}, invalidf(field+".labels."+key,
				"label %q is not consumed (honored: fleetly.job=init, fleetly.job.timeout, fleetly.job.after, fleetly.databases)", key)
		}
	}
	if v := svc.Labels["fleetly.job"]; v != "init" {
		return jobDecl{}, invalidf(field+".labels[fleetly.job]", "only the init dialect is supported (got %q)", v)
	}
	// 指向性拒绝面（preDeployJobRejections 同措辞；直接罗列保确定性序，
	// 不经 map 遍历）。
	if len(svc.Volumes) > 0 {
		return jobDecl{}, invalidf(field+".volumes", "file mounts are not supported on the job dialect; pass materials via platform configs/secrets (the compose migration surface)")
	}
	if len(svc.Ports) > 0 {
		return jobDecl{}, invalidf(field+".ports", "ports are not meaningful on a one-shot job")
	}
	if len(svc.Networks) > 0 {
		return jobDecl{}, invalidf(field+".networks", "jobs attach the project default network automatically; explicit networks are not supported on the job dialect")
	}
	if len(svc.DependsOn) > 0 {
		// 文案回正（ADR-0098 决策 5）：「jobs run in parallel」自 F1.13
		// 串行化起失真；序承载指向 after。
		return jobDecl{}, invalidf(field+".depends_on", "declare ordering with the fleetly.job.after label (a comma-separated list of job names); compose depends_on is not consumed (jobs run serially in the resolved order)")
	}
	if svc.Restart != "" {
		return jobDecl{}, invalidf(field+".restart", "restart policy is owned by the platform (jobs are single one-shot runs)")
	}
	if svc.Deploy != nil {
		return jobDecl{}, invalidf(field+".deploy", "deploy blocks are not consumed on the job dialect")
	}
	if svc.Build != nil {
		return jobDecl{}, invalidf(field+".build", "build source lands with the git batch (jobs reference images)")
	}
	// 白名单收口：其余官方面显式拒（fail-closed，上游新增字段默认落入
	// 拒绝面）；服务级 x- 扩展键同拒——job 方言无扩展载体。
	if err := rejectUnsupportedFields(field, svc, jobDialectAllowedKeys); err != nil {
		return jobDecl{}, err
	}
	if len(svc.Extensions) > 0 {
		return jobDecl{}, invalidf(field, "extension keys are not enabled on the job dialect (fleetly extensions live under deploy.x-fleetly-* and healthcheck.x-fleetly-* on long-running services)")
	}
	if svc.Image == "" {
		return jobDecl{}, invalidf(field+".image", "service %q has no image", name)
	}
	p := &specv1.ProcessSpec{Name: name, ImageOrigin: &specv1.ProcessSpec_Image{Image: svc.Image}}
	p.Command = svc.Command
	env, err := serviceEnv(field+".environment", svc.Environment)
	if err != nil {
		return jobDecl{}, err
	}
	p.Env = env
	// secrets：typed 面与服务面同律执法（R03-M7）——target 只认官方缺省
	// /run/secrets/<source>，uid/gid/mode 不翻译；name 覆盖解析平台名。
	// 旧实现直接取 Source 静默丢全部长语法字段（同文件写成 service 或
	// 顶层 job 会精确拒绝，行为按方言分叉）。
	if len(svc.Secrets) > 0 {
		secretRefs := make([]string, 0, len(svc.Secrets))
		for i, sr := range svc.Secrets {
			sf := field + ".secrets[" + fmt.Sprintf("%d", i) + "]"
			if sr.Source == "" {
				return jobDecl{}, invalidf(sf, "secret entry must reference a declared secret by name")
			}
			if sr.Target != "" && sr.Target != path.Join("/run/secrets", sr.Source) {
				return jobDecl{}, invalidf(sf, "secret %q: target remapping is not supported (injected at /run/secrets/<name>)", sr.Source)
			}
			if sr.UID != "" || sr.GID != "" || sr.Mode != nil {
				return jobDecl{}, invalidf(sf, "secret %q: uid/gid/mode are not translated", sr.Source)
			}
			secretRefs = append(secretRefs, declaredSecretName(project.Secrets, sr.Source))
		}
		p.SecretRefs = secretRefs
	}
	if lbl := strings.TrimSpace(svc.Labels["fleetly.databases"]); lbl != "" {
		dbRefs, derr := parseDatabaseRefs(field+".labels[fleetly.databases]", lbl)
		if derr != nil {
			return jobDecl{}, derr
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
				return jobDecl{}, invalidf(cf, "long-form source/target is required (target is the container path the file mounts at; got source %q without target)", c.Source)
			}
			mounts = append(mounts, &specv1.ConfigMount{Config: declaredConfigName(project.Configs, c.Source), Target: c.Target})
		}
		p.ConfigMounts = mounts
	}
	j := &specv1.JobSpec{Name: name, Process: p}
	if raw := strings.TrimSpace(svc.Labels["fleetly.job.timeout"]); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d <= 0 {
			return jobDecl{}, invalidf(field+".labels[fleetly.job.timeout]", "duration %q must be positive (e.g. \"10m\")", raw)
		}
		j.Ttl = durationpb.New(d)
	}
	decl := jobDecl{job: j, field: field, afterF: field + ".labels[fleetly.job.after]"}
	if raw := strings.TrimSpace(svc.Labels["fleetly.job.after"]); raw != "" {
		names, aerr := jobAfterLabel(decl.afterF, raw)
		if aerr != nil {
			return jobDecl{}, aerr
		}
		decl.after = names
	}
	return decl, nil
}

// jobDecl 是受理面 job 声明的收集形态（ADR-0098）：自然序承载（顶层
// 声明序在前、label 方言按服务名字典序在后——FromYAML 收集序）+ after
// 依赖承载 + 声明源字段路径（拒绝面寻址用）。orderPreDeployJobs 的输入。
type jobDecl struct {
	job    *specv1.JobSpec
	field  string   // 声明源（compose.x-fleetly-pre-deploy-jobs[i] / compose.services.<name>）
	afterF string   // after 承载的字段路径（环/引用错误寻址）
	after  []string // after 依赖名（受理时消费，不进冻结体；无承载为 nil）
}

// preDeployJobs 翻译顶层 x-fleetly-pre-deploy-jobs（ADR-0033 形态迁移：
// 键白名单 + 禁面理由 + 官方插值已消解值）。secrets 引用与 service 面
// 同律——名必须落在顶层 external 声明集内。
func preDeployJobs(project *types.Project, raw any) ([]jobDecl, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, invalidf("compose.x-fleetly-pre-deploy-jobs", "must be a list of job mappings")
	}
	decls := make([]jobDecl, 0, len(list))
	for i, rawJob := range list {
		field := fmt.Sprintf("compose.x-fleetly-pre-deploy-jobs[%d]", i)
		m, ok := rawJob.(map[string]any)
		if !ok {
			return nil, invalidf(field, "job must be a mapping")
		}
		for key := range m {
			if preDeployJobWhitelist[key] {
				continue
			}
			if reason, banned := preDeployJobRejections[key]; banned {
				return nil, invalidf(field+"."+key, "%s", reason)
			}
			return nil, invalidf(field+"."+key,
				"unsupported field %q for a pre-deploy job (supported: %s)", key, whitelistKeys(preDeployJobWhitelist))
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
		// after 承载（ADR-0098 决策 2）：单名 string / 名单 list 双形态
		//（command 同先例）；受理时消费，不进 JobSpec。
		after, err := jobAfter(field+".after", m["after"])
		if err != nil {
			return nil, err
		}
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
		decls = append(decls, jobDecl{job: j, field: field, afterF: field + ".after", after: after})
	}
	return decls, nil
}

// jobAfter 解析顶层 job 条目的 after 承载：单名 string 或名单 list（空串
// 条目拒绝——依赖名承载不容静默丢）。
func jobAfter(field string, raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case string:
		if v == "" {
			return nil, invalidf(field, "must be a job name or a list of job names")
		}
		return []string{v}, nil
	case []any:
		names := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok || s == "" {
				return nil, invalidf(fmt.Sprintf("%s[%d]", field, i), "after entries must be non-empty job names")
			}
			names = append(names, s)
		}
		if len(names) == 0 {
			return nil, invalidf(field, "must carry at least one job name")
		}
		return names, nil
	default:
		return nil, invalidf(field, "after must be a job name or a list of job names")
	}
}

// jobAfterLabel 解析 label 方言的 fleetly.job.after（逗号分隔多名，
// trim/跳空段——fleetly.databases 同律）。
func jobAfterLabel(field, lbl string) ([]string, error) {
	var names []string
	for _, part := range strings.Split(lbl, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, invalidf(field, "must carry at least one pre-deploy job name (comma-separated for several)")
	}
	return names, nil
}

// orderPreDeployJobs 解析 after 承载为执行序（ADR-0098）：自然序（现状
// 收集序——顶层声明序在前、label 方言字典序在后）为基做 Kahn 拓扑排，
// 每拍取**最小自然序**的就绪 job（输出确定且唯一）；无任何 after 承载时
// 自然序原样返回（存量受理零漂移，确定性不回退）。环 fail-closed：剩余
// 集中沿 after 可自达者即环参与者，字典序点名（E_INVALID_ARGUMENT 经
// ValidationError 通道，mapValidationError 既有映射）。
func orderPreDeployJobs(decls []jobDecl) ([]*specv1.JobSpec, error) {
	anyAfter := false
	for _, d := range decls {
		if len(d.after) > 0 {
			anyAfter = true
			break
		}
	}
	out := make([]*specv1.JobSpec, 0, len(decls))
	if !anyAfter {
		for _, d := range decls {
			out = append(out, d.job)
		}
		return out, nil
	}
	// 名字索引；重名在 after 在场时受理源先行拒绝（引用不可歧义——
	// ValidateApp 的重名执法在受理尾，此处可寻址到 compose 源）。
	idxByName := make(map[string]int, len(decls))
	for i, d := range decls {
		if _, dup := idxByName[d.job.GetName()]; dup {
			return nil, invalidf(d.field, "duplicate pre-deploy job name %q", d.job.GetName())
		}
		idxByName[d.job.GetName()] = i
	}
	// after 引用执法（未知名拒绝）+ 依赖下标化（同名重复依赖静默去重，
	// parseDatabaseRefs 同律——同一依赖重复声明不丢语义）。
	deps := make([][]int, len(decls))
	for i, d := range decls {
		seen := map[string]bool{}
		for _, name := range d.after {
			if seen[name] {
				continue
			}
			seen[name] = true
			j, ok := idxByName[name]
			if !ok {
				return nil, invalidf(d.afterF, "after references pre-deploy job %q which is not declared", name)
			}
			deps[i] = append(deps[i], j)
		}
	}
	emitted := make([]bool, len(decls))
	for len(out) < len(decls) {
		pick := -1
		for i := range decls {
			if emitted[i] {
				continue
			}
			ready := true
			for _, dep := range deps[i] {
				if !emitted[dep] {
					ready = false
					break
				}
			}
			if ready {
				pick = i
				break
			}
		}
		if pick < 0 {
			// 断环：环参与者 = 剩余集中沿 after 可自达者（含自环）；字典序
			// 点名，错误锚在首个（自然序）参与者的 after 承载位。
			var cycle []string
			first := -1
			for i := range decls {
				if emitted[i] || !afterReachesSelf(i, deps) {
					continue
				}
				if first < 0 {
					first = i
				}
				cycle = append(cycle, decls[i].job.GetName())
			}
			sort.Strings(cycle)
			// 不可达防御（受理面永不 panic 律）：无就绪节点必有环、环成员必
			// 自达——first 走不到 -1；仍守一层诚实失败。
			if first < 0 || len(cycle) == 0 {
				return nil, invalidf("compose.x-fleetly-pre-deploy-jobs", "pre-deploy job ordering could not be resolved")
			}
			return nil, invalidf(decls[first].afterF,
				"pre-deploy job ordering declares a cycle among %s (after must be acyclic; jobs run serially in the resolved order)",
				strings.Join(cycle, ", "))
		}
		emitted[pick] = true
		out = append(out, decls[pick].job)
	}
	return out, nil
}

// afterReachesSelf 判 start 沿 after 依赖是否可回到自身（环成员判定）。
func afterReachesSelf(start int, deps [][]int) bool {
	visited := make([]bool, len(deps))
	stack := append([]int(nil), deps[start]...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == start {
			return true
		}
		if visited[n] {
			continue
		}
		visited[n] = true
		stack = append(stack, deps[n]...)
	}
	return false
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

// 归一化两源（F0.8）：Compose 受控子集与镜像直投统一归一成 AppSpec
// （架构 §4：归一化结果即 Revision 冻结体，部署基线永远以 Revision 为准，
// 不回读源文件）。Compose 白名单只增；受管/未知字段显式拒绝且理由精确。
package spec

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// composeWhitelist 是受控子集的白名单（compose 顶层与服务键；只增）。
var composeWhitelist = map[string]bool{
	"services": true, // 顶层唯一入口（name/networks 顶层不翻译）
	// 顶层 volumes 声明：接受空声明（name: null/{}）——Volume 实体在平台侧
	//（fleetly volumes create，钉住/驱动选项不入 compose）；带驱动的声明
	// 显式拒绝（B2）。
	"volumes": true,
	// firstBootJobs 扩展键（ADR-0033）：部署期 init job 声明，语义面是
	// ADR-0030 引擎链。
	"x-fleetly-first-boot-jobs": true,
}

var composeServiceWhitelist = map[string]bool{
	"image":       true,
	"command":     true,
	"environment": true,
	"ports":       true,
	"deploy":      true,
	"healthcheck": true,
	"networks":    true,
	// N0 修复批 B2 声明面补齐：
	"volumes": true, // 挂载平台 Volume（短语法 name:/target）
	"secrets": true, // 按名引用 Project Secret → secret_refs
}

// composeHealthcheckWhitelist 是 healthcheck 子键白名单：compose 原生节律
// 键 + fleetly 探针声明扩展（http_path/tcp_port——compose 无对应原语，
// exec 探针走 test；三选一）。
var composeHealthcheckWhitelist = map[string]bool{
	"test":         true,
	"interval":     true,
	"timeout":      true,
	"retries":      true,
	"start_period": true,
	"http_path":    true,
	"tcp_port":     true,
	"disable":      false, // 探针是平台健康门数据源，禁用探针不可声明
}

var composeDeployWhitelist = map[string]bool{
	"replicas":  true,
	"resources": true, // limits.cpus/memory 受控翻译
	// strategy 是 fleetly 扩展键（ADR-0048 决策 1：compose 服务键
	// deploy.strategy，值 rolling|blue-green——镜像直投/spec_file 面外的
	// 第三 intake 形态；非 compose 标准键，同 http_path/tcp_port 扩展先例）。
	"strategy":        true,
	"restart_policy":  false, // 重启策略是平台语义（受管）——显式拒绝
	"update_config":   false,
	"rollback_config": false,
	"placement":       false, // Placement 以平台节点 ID 为锚（compose 约束语法不翻译）
	"labels":          false,
}

// composeJobWhitelist 是 first boot job 的字段白名单（ADR-0033 最小面：
// 复用 compose 服务词汇；from_build 在 compose intake 不可达——App 无
// Build 声明）。
var composeJobWhitelist = map[string]bool{
	"name":        true,
	"image":       true,
	"command":     true,
	"environment": true,
	"secrets":     true,
	"ttl":         true,
}

// composeJobRejections 是 job 禁面键的拒绝理由（ADR-0030 决策 8 同口径；
// networks 指向缺省挂靠）。键名层即拒，不待 IR 层。
var composeJobRejections = map[string]string{
	"volumes":     "deploy-time jobs cannot mount volumes; use a Task for volume-mounting jobs",
	"configs":     "deploy-time jobs cannot mount configs; pass what the job needs via environment or secrets",
	"ports":       "ports are not meaningful on a one-shot job",
	"healthcheck": "healthchecks are not meaningful on a one-shot job",
	"placement":   "placement is not supported on deploy-time jobs",
	"replicas":    "a deploy-time job is a single one-shot run",
	"networks":    "deploy-time jobs attach to every active project network by default (db-<id> included); narrowing is not declarable from compose",
}

// ImageDeploy 归一化镜像直投：单 process web（名可指定，默认 web）。
// probe 是可选探针声明（B2：http path 或 tcp port——二选一，nil = 无探针）。
// env 是 App 级变量直传（ADR-0043 决策 2：键按 EnvNamePattern 受理面即拒；
// Project 层 SharedVariable 的合成在冻结咽喉，此处只落 App 层）。
// ports 是可选端口声明（F3.5）：Route 后端解析的期望集供给面——声明即
// Route-facing（挂靠项目 default 网，Proxy 可达），见 portDeclNetworks。
func ImageDeploy(appID, projectID, image string, processName string, probe *specv1.HealthcheckSpec, env map[string]string, ports []*specv1.PortSpec) (*specv1.AppSpec, error) {
	if err := ValidateEnvKeys("image.env", env); err != nil {
		return nil, err
	}
	if processName == "" {
		processName = "web"
	}
	spec := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: image}}},
		Processes: []*specv1.ProcessSpec{{
			Name: processName,
			ImageOrigin: &specv1.ProcessSpec_Image{
				Image: image,
			},
			Replicas:    1,
			Healthcheck: probe,
			Env:         env,
			Ports:       ports,
			Networks:    portDeclNetworks(ports),
		}},
	}
	if err := ValidateApp(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// portDeclNetworks 是单进程形态（image 直投/upload）端口声明的挂网裁决
// （F3.5）：声明端口 = Route-facing 意图——进程挂靠项目 default 网（项目
// 出生必建、ensureNetworks create-or-get 兜底），受管 Proxy 挂全部项目网
// 即可达后端（static Route 404 的可达性半边）。未声明端口维持"无网络即
// 无 DNS 面"的既有诚实语义（ADR-0034 决策 2），行为零变化。
func portDeclNetworks(ports []*specv1.PortSpec) []string {
	if len(ports) == 0 {
		return nil
	}
	return []string{"default"}
}

// ParseSpecFile 解析裸 AppSpec 部署面（第四 intake 源，F3.5）：protojson
// 解码（未知字段拒绝——fail-closed，AppSpec 消息 schema 即可写面边界，
// ADR-0033"校验爆炸半径"担忧的解法：字段白名单不存在、全部字段合法，
// 约束由 ValidateApp 叶子单源执法）+ AppRef 权威覆写（归属锚来自
// DeployRequest.app_id 行，文件内声明不信任、恒覆写）+ schema_version
// 缺省当前版（0 = 未声明；>当前版由 ValidateApp 拒——读入 check-strategy
// 的"拒绝跳代"半边）。归一化产物与三形态同喉——freezeRevision 的共享
// 变量合成与内容寻址复用天然覆盖。
func ParseSpecFile(body []byte, appID, projectID string) (*specv1.AppSpec, error) {
	s := &specv1.AppSpec{}
	if err := protojson.Unmarshal(body, s); err != nil {
		return nil, invalidf("spec_file", "invalid AppSpec JSON: %v", err)
	}
	if s.GetSchemaVersion() == 0 {
		s.SchemaVersion = SchemaVersion
	}
	s.App = &specv1.AppRef{Id: appID, Project: projectID}
	if err := ValidateApp(s); err != nil {
		return nil, err
	}
	return s, nil
}

// UploadDeployInput 是上传产物形态的部署输入（三 strategy 旗标经此组装
// BuildSpec，ADR-0032；Builder 空值按 Dockerfile 缺省轨处理）。
type UploadDeployInput struct {
	AppID    string
	Project  string
	UploadID string
	// ProcessName 是进程名（缺省 web）。
	ProcessName string
	// Builder 是构建器名（dockerfile|railpack|static；空 = dockerfile）。
	Builder string
	// Dockerfile 是 dockerfile 形态的构建文件路径（缺省 Dockerfile）。
	Dockerfile string
	// RailpackVersion 是 railpack 形态的钉版（必填，bare semver）。
	RailpackVersion string
	// OutputDir 是 static 形态的产物目录（缺省 "."）。
	OutputDir string
	// Probe 是可选探针声明（http path 或 tcp port）。
	Probe *specv1.HealthcheckSpec
	// Env 是 App 级变量直传（ADR-0043 决策 2；键按 EnvNamePattern 受理面
	// 即拒——与 image 直投同口径）。
	Env map[string]string
	// Ports 是可选端口声明（F3.5）：Route 后端解析的期望集供给面——声明
	// 即 Route-facing（挂靠项目 default 网，Proxy 可达），见 portDeclNetworks。
	Ports []*specv1.PortSpec
}

// UploadDeploy 归一化上传产物形态（F1.10，ADR-0019 附录 A；strategy 面
// ADR-0032）：Source.upload + BuildSpec（三 strategy）+ 单进程 from_build
// （与 git 源 webhook 路径同构；from_build 的 digest 解析按进程名，值本身
// 只要求非空）。
func UploadDeploy(in UploadDeployInput) (*specv1.AppSpec, error) {
	if err := ValidateEnvKeys("upload.env", in.Env); err != nil {
		return nil, err
	}
	builder := in.Builder
	if builder == "" {
		builder = BuilderDockerfile
	}
	var build *specv1.BuildSpec
	switch builder {
	case BuilderDockerfile:
		dockerfile := in.Dockerfile
		if dockerfile == "" {
			dockerfile = "Dockerfile"
		}
		build = &specv1.BuildSpec{Builder: builder, Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: dockerfile}}
	case BuilderRailpack:
		build = &specv1.BuildSpec{
			Builder:  builder,
			Strategy: &specv1.BuildSpec_Railpack{Railpack: &specv1.RailpackBuilder{PinnedVersion: in.RailpackVersion}},
		}
	case BuilderStatic:
		outputDir := in.OutputDir
		if outputDir == "" {
			outputDir = "."
		}
		build = &specv1.BuildSpec{
			Builder:  builder,
			Strategy: &specv1.BuildSpec_Static{Static: &specv1.StaticBuilder{OutputDir: outputDir}},
		}
	default:
		return nil, invalidf("build.builder",
			"unknown builder %q (supported: dockerfile, railpack, static)", builder)
	}
	processName := in.ProcessName
	if processName == "" {
		processName = "web"
	}
	s := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: in.AppID, Project: in.Project},
		Source:        &specv1.Source{Kind: &specv1.Source_Upload{Upload: &specv1.UploadSource{Id: in.UploadID}}},
		Build:         build,
		Processes: []*specv1.ProcessSpec{{
			Name:        processName,
			ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: processName},
			Replicas:    1,
			Healthcheck: in.Probe,
			Env:         in.Env,
			Ports:       in.Ports,
			Networks:    portDeclNetworks(in.Ports),
		}},
	}
	if err := ValidateApp(s); err != nil {
		return nil, err
	}
	return s, nil
}

// ComposeDoc 是 Compose 文档的解码面（原生 map 类型——yaml.v3 对命名
// map 类型会递归复用该类型填充嵌套 map，导致 map[string]any 断言失败；
// 白名单在 map 层执法）。
type ComposeDoc = map[string]any

// NormalizeCompose 把 Compose 受控子集归一成 AppSpec：白名单外字段显式
// 拒绝（理由精确到字段路径——受管字段显式拒绝，未知字段提示清单）。
func NormalizeCompose(doc ComposeDoc, appID, projectID string) (*specv1.AppSpec, error) {
	for key := range doc {
		if !composeWhitelist[key] {
			return nil, invalidf("compose."+key,
				"unsupported compose field (supported: services, volumes); use the platform API for this concern")
		}
	}
	// 顶层 volumes：只接受空声明（null/{}）——Volume 实体与驱动选项在
	// 平台侧。非 map 形态显式拒绝（N0.1 P2-3：此前类型断言失败被静默
	// 跳过——列表/字符串形态的声明等于没校验）。
	if raw, present := doc["volumes"]; present && raw != nil {
		vols, ok := raw.(map[string]any)
		if !ok {
			return nil, invalidf("compose.volumes", "top-level volumes must be a mapping of volume names")
		}
		for name, raw := range vols {
			if raw == nil {
				continue
			}
			if decl, ok := raw.(map[string]any); ok && len(decl) == 0 {
				continue
			}
			return nil, invalidf("compose.volumes."+name,
				"volume declarations with driver options are not translated; create the volume with 'fleetly volumes create' and mount it by name")
		}
	}
	services, ok := doc["services"].(map[string]any)
	if !ok || len(services) == 0 {
		return nil, invalidf("compose.services", "at least one service is required")
	}

	spec := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Processes:     []*specv1.ProcessSpec{},
	}
	// 源镜像取第一个服务（多服务 Compose 的单镜像源口径；构建源随 Git 批次）。
	firstImage := ""

	for name, raw := range services {
		svc, ok := raw.(map[string]any)
		if !ok {
			return nil, invalidf("compose.services."+name, "service must be a mapping")
		}
		p, image, err := composeService(name, svc)
		if err != nil {
			return nil, err
		}
		if firstImage == "" {
			firstImage = image
		}
		spec.Processes = append(spec.Processes, p)
	}
	if firstImage == "" {
		return nil, invalidf("compose.services", "no service carries an image (build source lands with the git batch)")
	}
	spec.Source = &specv1.Source{Kind: &specv1.Source_Image{Image: &specv1.ImageSource{Ref: firstImage}}}

	// firstBootJobs 扩展键（ADR-0033）：受控子集的部署期 init job 声明。
	if raw, present := doc["x-fleetly-first-boot-jobs"]; present && raw != nil {
		jobs, err := composeFirstBootJobs(raw)
		if err != nil {
			return nil, err
		}
		spec.FirstBootJobs = jobs
	}

	if err := ValidateApp(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// composeService 翻译单个 compose 服务（白名单执法 + 字段级拒绝理由）。
func composeService(name string, svc map[string]any) (*specv1.ProcessSpec, string, error) {
	field := "compose.services." + name
	for key := range svc {
		if allowed, seen := composeServiceWhitelist[key]; !seen {
			return nil, "", invalidf(field+"."+key,
				"unsupported field %q for service %q (supported: %s)", key, name, whitelistKeys(composeServiceWhitelist))
		} else if !allowed {
			// 白名单显式 false = 受管字段（平台语义，拒绝并说明归属）。
			return nil, "", invalidf(field+"."+key,
				"field %q is managed by the platform and cannot be set from compose", key)
		}
	}
	p := &specv1.ProcessSpec{Name: name, Replicas: 1}

	if img, ok := svc["image"].(string); ok && img != "" {
		p.ImageOrigin = &specv1.ProcessSpec_Image{Image: img}
	} else {
		return nil, "", invalidf(field+".image", "service %q has no image (build lands with the git batch)", name)
	}
	cmd, err := composeCommand(field+".command", svc["command"])
	if err != nil {
		return nil, "", err
	}
	p.Command = cmd
	env, err := composeEnv(field+".environment", svc["environment"])
	if err != nil {
		return nil, "", err
	}
	p.Env = env
	if ports, ok := svc["ports"].([]any); ok {
		for i, raw := range ports {
			port, err := composePort(field+".ports", raw)
			if err != nil {
				return nil, "", err
			}
			_ = i
			p.Ports = append(p.Ports, port)
		}
	}
	if deploy, ok := svc["deploy"].(map[string]any); ok {
		for key := range deploy {
			if allowed, seen := composeDeployWhitelist[key]; !seen {
				return nil, "", invalidf(field+".deploy."+key,
					"unsupported deploy field %q (supported: replicas, resources.limits, strategy)", key)
			} else if !allowed {
				return nil, "", invalidf(field+".deploy."+key,
					"deploy field %q is managed by the platform and cannot be set from compose", key)
			}
		}
		if r, ok := deploy["replicas"].(int); ok && r > 0 {
			p.Replicas = int64(r) //nolint:gosec // int→int64 域内
		}
		// 部署策略（ADR-0048）：人类词形 rolling|blue-green → 枚举；空 =
		// 缺省滚动（零值，不落字段——存量 Revision 冻结体零漂移）。
		if raw, ok := deploy["strategy"].(string); ok && raw != "" {
			switch raw {
			case "rolling":
				p.Strategy = specv1.DeployStrategy_DEPLOY_STRATEGY_ROLLING
			case "blue-green":
				p.Strategy = specv1.DeployStrategy_DEPLOY_STRATEGY_BLUE_GREEN
			default:
				return nil, "", invalidf(field+".deploy.strategy",
					"strategy must be rolling or blue-green (got %q)", raw)
			}
		}
		if res, ok := deploy["resources"].(map[string]any); ok {
			if limits, ok := res["limits"].(map[string]any); ok {
				p.Resources = &specv1.ResourcesSpec{}
				if cpus, ok := limits["cpus"].(string); ok && cpus != "" {
					millis, err := cpusToMillis(field+".deploy.resources.limits.cpus", cpus)
					if err != nil {
						return nil, "", err
					}
					p.Resources.CpuMillis = millis
				}
				if mem, ok := limits["memory"].(string); ok && mem != "" {
					mb, err := memoryToMB(field+".deploy.resources.limits.memory", mem)
					if err != nil {
						return nil, "", err
					}
					p.Resources.MemoryMb = mb
				}
			}
		}
	}
	if nets, ok := svc["networks"].([]any); ok {
		for _, raw := range nets {
			if n, ok := raw.(string); ok && n != "" {
				p.Networks = append(p.Networks, n)
			}
		}
	}
	// 声明端口即 Route-facing（F3.5 裁决的两半边）：ports 在场而 default
	// 未挂时补挂项目 default 网——与 image/upload 形态的 portDeclNetworks
	// 同语义（Proxy 可达性半边）。2026-10-06 staging 走查发现：compose 声明
	// ports 不挂网时 Route 后端发布成不可达名（502），声明面只兑现了 404
	// 半边。spec_file 是用户亲笔 AppSpec，平台不改写其 networks（用户全权）。
	if len(p.Ports) > 0 && !slices.Contains(p.Networks, "default") {
		p.Networks = append(p.Networks, "default")
	}
	// 卷挂载（B2）：短语法 name:/target[:mode]——name 是平台 Volume 名
	//（fleetly volumes create 的实体），target 容器内绝对路径，mode 可选
	// ro/rw（N0.1 P2-3：此前 :ro 被吞进 target——mode 现在正确解析并在
	// ro 时只读挂载）。
	if vols, ok := svc["volumes"].([]any); ok {
		for _, raw := range vols {
			s, ok := raw.(string)
			if !ok {
				return nil, "", invalidf(field+".volumes", "volume entry must be the short syntax \"name:/target[:ro|rw]\"")
			}
			name, rest, found := strings.Cut(s, ":")
			target, mode, _ := strings.Cut(rest, ":")
			if !found || name == "" || !strings.HasPrefix(target, "/") {
				return nil, "", invalidf(field+".volumes", "volume %q must be \"name:/target[:ro|rw]\" with an absolute target", s)
			}
			readOnly := false
			switch mode {
			case "":
			case "ro":
				readOnly = true
			case "rw":
			default:
				return nil, "", invalidf(field+".volumes", "volume %q: unknown mode %q (expected ro or rw)", s, mode)
			}
			p.Volumes = append(p.Volumes, &specv1.VolumeAttachment{VolumeId: name, Target: target, ReadOnly: readOnly})
		}
	}
	// Secret 引用（B2，服务与 job 共用助手）：短语法 [name...] 或长语法
	// {source: name}——值永不进 compose（经 fleetly secrets put 落库，注入
	// 时解析，ADR-0014）。长语法只认 source（target==name 容忍）；uid/gid/
	// mode 等其余子键显式拒绝（N0.1 P2-3：此前静默丢弃）。
	refs, err := composeSecretRefs(field+".secrets", svc["secrets"])
	if err != nil {
		return nil, "", err
	}
	p.SecretRefs = refs
	if hc, ok := svc["healthcheck"].(map[string]any); ok {
		probe, err := composeHealthcheck(field+".healthcheck", hc)
		if err != nil {
			return nil, "", err
		}
		p.Healthcheck = probe
	}
	return p, p.GetImage(), nil
}

// composeCommand 翻译 command 两形态（ADR-0033）：string → Fields 切分
// （compose shell 形态，语义与既有服务面一致），list → 字面 argv（引号/
// 空格保真——"读 secret 文件 → export → exec"的 sh -c 包装是必需形态：
// Secret 注入是文件面而非 env 插值，ADR-0014）。
func composeCommand(field string, raw any) ([]string, error) {
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
		if len(argv) == 0 {
			return nil, nil
		}
		return argv, nil
	default:
		return nil, invalidf(field, "command must be a string or a list of strings")
	}
}

// composeEnv 翻译 environment 两形态：map（K: V）与 list（["K=V"]——compose
// 原生第二形态；此前 map 断言失败被静默丢弃，ADR-0033 修复为显式拒绝/翻译）。
func composeEnv(field string, raw any) (map[string]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		env := make(map[string]string, len(v))
		for k, val := range v {
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

// composeSecretRefs 翻译 secrets（服务与 job 共用面）：短语法 [name...] /
// 长语法 {source: name}——值永不进 compose（经 fleetly secrets put 落库，
// 注入时解析，ADR-0014）。长语法只认 source（target==name 容忍）；
// uid/gid/mode 等其余子键显式拒绝（N0.1 P2-3：此前静默丢弃）。
func composeSecretRefs(field string, raw any) ([]string, error) {
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
		// 名字符集（N1 收尾批 A3）：引用名原样成为 /run/secrets/<名> 路径——
		// 翻译层即拒（ValidateProcess 兜底，此处给 compose 侧精确字段名）。
		if !ValidSecretName(name) {
			return nil, invalidf(field,
				"secret name %q must match %q and must not contain \"..\" (secret names become /run/secrets/<name> paths)", name, SecretNamePattern)
		}
		refs = append(refs, name)
	}
	return refs, nil
}

// composeFirstBootJobs 翻译 x-fleetly-first-boot-jobs 扩展键（ADR-0033）：
// 白名单最小面 + 禁面键名层即拒（理由与 ValidateJob 同口径，ADR-0030
// 决策 8）；ttl 值域与 JobSpec 禁面由 ValidateApp→ValidateJob 既有执法。
// 执行语义（串行/游标/回滚不重跑）是 ADR-0030 引擎链，本层只喂食。
func composeFirstBootJobs(raw any) ([]*specv1.JobSpec, error) {
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
			if composeJobWhitelist[key] {
				continue
			}
			if reason, banned := composeJobRejections[key]; banned {
				return nil, invalidf(field+"."+key, "%s", reason)
			}
			return nil, invalidf(field+"."+key,
				"unsupported field %q for a first boot job (supported: %s)", key, whitelistKeys(composeJobWhitelist))
		}
		name, ok := m["name"].(string)
		if !ok || name == "" {
			return nil, invalidf(field+".name", "must be a non-empty string")
		}
		image, ok := m["image"].(string)
		if !ok || image == "" {
			return nil, invalidf(field+".image", "must be a non-empty image reference (from_build jobs are not reachable from compose)")
		}
		p := &specv1.ProcessSpec{Name: name, ImageOrigin: &specv1.ProcessSpec_Image{Image: image}}
		cmd, err := composeCommand(field+".command", m["command"])
		if err != nil {
			return nil, err
		}
		p.Command = cmd
		env, err := composeEnv(field+".environment", m["environment"])
		if err != nil {
			return nil, err
		}
		p.Env = env
		refs, err := composeSecretRefs(field+".secrets", m["secrets"])
		if err != nil {
			return nil, err
		}
		p.SecretRefs = refs
		j := &specv1.JobSpec{Name: name, Process: p}
		if rawTTL := m["ttl"]; rawTTL != nil {
			ttl, err := composeDuration(field+".ttl", rawTTL)
			if err != nil {
				return nil, err
			}
			j.Ttl = ttl
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func composePort(field string, raw any) (*specv1.PortSpec, error) {
	s, ok := raw.(string)
	if !ok {
		return nil, invalidf(field, "port entry must be a string like \"8080\" or \"8080/tcp\"")
	}
	parts := strings.SplitN(s, "/", 2)
	var port int
	if _, err := fmt.Sscanf(parts[0], "%d", &port); err != nil || port < 1 || port > 65535 {
		return nil, invalidf(field, "port %q out of range", parts[0])
	}
	spec := &specv1.PortSpec{Port: int32(port), Protocol: specv1.Protocol_PROTOCOL_HTTP} //nolint:gosec
	if len(parts) == 2 {
		switch parts[1] {
		case "http":
			spec.Protocol = specv1.Protocol_PROTOCOL_HTTP
		case "h2c":
			spec.Protocol = specv1.Protocol_PROTOCOL_H2C
		case "tcp":
			spec.Protocol = specv1.Protocol_PROTOCOL_TCP
		default:
			return nil, invalidf(field, "protocol %q must be http, h2c or tcp", parts[1])
		}
	}
	return spec, nil
}

// composeHealthcheck 归一探针声明（B2 补齐全键）：compose 原生 test
// （exec 探针）+ 节律键（interval/timeout/retries/start_period）+ fleetly
// 扩展声明 http_path/tcp_port（http/tcp 探针——compose 无对应原语）。
// 探针三选一；disable 不可声明（探针是 L1 健康门数据源）。
func composeHealthcheck(field string, hc map[string]any) (*specv1.HealthcheckSpec, error) {
	for key := range hc {
		if allowed, seen := composeHealthcheckWhitelist[key]; !seen {
			return nil, invalidf(field+"."+key,
				"unknown healthcheck field %q (supported: %s)", key, whitelistKeys(composeHealthcheckWhitelist))
		} else if !allowed {
			return nil, invalidf(field+"."+key,
				"healthcheck field %q is managed by the platform and cannot be set from compose", key)
		}
	}
	out := &specv1.HealthcheckSpec{Retries: 3}
	// 探针三选一。
	hasHTTP, hasTCP, hasExec := hc["http_path"] != nil, hc["tcp_port"] != nil, hc["test"] != nil
	if (hasHTTP && hasTCP) || (hasHTTP && hasExec) || (hasTCP && hasExec) {
		return nil, invalidf(field+".healthcheck", "probe is one of test, http_path or tcp_port")
	}
	if raw, ok := hc["http_path"].(string); ok && raw != "" {
		if !strings.HasPrefix(raw, "/") {
			return nil, invalidf(field+".healthcheck.http_path", "http_path must be an absolute path starting with '/'")
		}
		out.Probe = &specv1.HealthcheckSpec_HttpPath{HttpPath: raw}
	} else if hasHTTP {
		return nil, invalidf(field+".healthcheck.http_path", "http_path must be a non-empty path string starting with '/'")
	}
	if raw, ok := hc["tcp_port"].(int); ok && raw > 0 {
		out.Probe = &specv1.HealthcheckSpec_TcpPort{TcpPort: int32(raw)} //nolint:gosec // 端口域内
	} else if hasTCP {
		return nil, invalidf(field+".healthcheck.tcp_port", "tcp_port must be a port number 1-65535")
	}
	if test, ok := hc["test"].([]any); ok && len(test) > 1 {
		// compose test 形态 ["CMD", ...] / ["CMD-SHELL", cmd]——exec 探针。
		// CMD-SHELL 的载荷是**单个 shell 字符串**：按空白切分会撕碎引号结构
		//（staging 真机实证：bash -c 'exec 3<>/dev/tcp/...' 被切成碎 argv →
		// 探针恒败 unexpected EOF，2026-10-02）——整体经 sh -c 承载。
		words := make([]string, 0, len(test))
		for _, w := range test {
			words = append(words, fmt.Sprintf("%v", w))
		}
		var exec *specv1.ExecProbe
		switch words[0] {
		case "CMD":
			exec = &specv1.ExecProbe{Command: words[1:]}
		case "CMD-SHELL":
			exec = &specv1.ExecProbe{Command: []string{"sh", "-c", strings.Join(words[1:], " ")}}
		default:
			return nil, invalidf(field+".healthcheck.test", "test must start with CMD or CMD-SHELL")
		}
		out.Probe = &specv1.HealthcheckSpec_Exec{Exec: exec}
	}
	var err error
	if out.Interval, err = composeDuration(field+".healthcheck.interval", hc["interval"]); err != nil {
		return nil, err
	}
	if out.Timeout, err = composeDuration(field+".healthcheck.timeout", hc["timeout"]); err != nil {
		return nil, err
	}
	if out.StartPeriod, err = composeDuration(field+".healthcheck.start_period", hc["start_period"]); err != nil {
		return nil, err
	}
	if r, ok := hc["retries"].(int); ok && r > 0 {
		out.Retries = int32(r) //nolint:gosec // 计数域内
	}
	if out.Probe == nil {
		return nil, invalidf(field+".healthcheck", "healthcheck requires one of test, http_path or tcp_port")
	}
	return out, nil
}

// composeDuration 解析 compose 时长字面量（"30s"/"1m30s" 等 Go 形态）。
func composeDuration(field string, raw any) (*durationpb.Duration, error) {
	if raw == nil {
		return nil, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, invalidf(field, "duration must be a string like \"30s\"")
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return nil, invalidf(field, "duration %q must be positive (e.g. \"30s\")", s)
	}
	return durationpb.New(d), nil
}

func cpusToMillis(field, cpus string) (int64, error) {
	var v float64
	if _, err := fmt.Sscanf(cpus, "%g", &v); err != nil || v <= 0 {
		return 0, invalidf(field, "cpus %q must be a positive number", cpus)
	}
	return int64(v * 1000), nil //nolint:gosec // 毫核域内
}

func memoryToMB(field, mem string) (int64, error) {
	mem = strings.ToLower(mem)
	mult := int64(1)
	switch {
	case strings.HasSuffix(mem, "g"), strings.HasSuffix(mem, "gb"):
		mult = 1024
		mem = strings.TrimSuffix(strings.TrimSuffix(mem, "gb"), "g")
	case strings.HasSuffix(mem, "m"), strings.HasSuffix(mem, "mb"):
		mem = strings.TrimSuffix(strings.TrimSuffix(mem, "mb"), "m")
	default:
		return 0, invalidf(field, "memory %q must use m/g suffix (e.g. 512m)", mem)
	}
	v, err := strconv.ParseInt(mem, 10, 64)
	if err != nil || v <= 0 {
		return 0, invalidf(field, "memory %q must be a positive number", mem)
	}
	return v * mult, nil
}

func whitelistKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k, ok := range m {
		if ok {
			keys = append(keys, k)
		}
	}
	return strings.Join(keys, ", ")
}

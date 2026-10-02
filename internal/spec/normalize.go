// 归一化两源（F0.8）：Compose 受控子集与镜像直投统一归一成 AppSpec
// （架构 §4：归一化结果即 Revision 冻结体，部署基线永远以 Revision 为准，
// 不回读源文件）。Compose 白名单只增；受管/未知字段显式拒绝且理由精确。
package spec

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	"replicas":        true,
	"resources":       true,  // limits.cpus/memory 受控翻译
	"restart_policy":  false, // 重启策略是平台语义（受管）——显式拒绝
	"update_config":   false,
	"rollback_config": false,
	"placement":       false, // Placement 以平台节点 ID 为锚（compose 约束语法不翻译）
	"labels":          false,
}

// ImageDeploy 归一化镜像直投：单 process web（名可指定，默认 web）。
// probe 是可选探针声明（B2：http path 或 tcp port——二选一，nil = 无探针）。
func ImageDeploy(appID, projectID, image string, processName string, probe *specv1.HealthcheckSpec) (*specv1.AppSpec, error) {
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
		}},
	}
	if err := ValidateApp(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// UploadDeploy 归一化上传产物形态（F1.10，ADR-0019 附录 A）：Source.upload
// + dockerfile 构建 + 单进程 from_build（与 git 源 webhook 路径同构；
// from_build 的 digest 解析按进程名，值本身只要求非空）。
func UploadDeploy(appID, projectID, uploadID, dockerfile, processName string, probe *specv1.HealthcheckSpec) (*specv1.AppSpec, error) {
	if processName == "" {
		processName = "web"
	}
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	s := &specv1.AppSpec{
		SchemaVersion: SchemaVersion,
		App:           &specv1.AppRef{Id: appID, Project: projectID},
		Source:        &specv1.Source{Kind: &specv1.Source_Upload{Upload: &specv1.UploadSource{Id: uploadID}}},
		Build: &specv1.BuildSpec{
			Builder:  "dockerfile",
			Strategy: &specv1.BuildSpec_Dockerfile{Dockerfile: dockerfile},
		},
		Processes: []*specv1.ProcessSpec{{
			Name:        processName,
			ImageOrigin: &specv1.ProcessSpec_FromBuild{FromBuild: processName},
			Replicas:    1,
			Healthcheck: probe,
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
	if cmd, ok := svc["command"].(string); ok && cmd != "" {
		p.Command = strings.Fields(cmd)
	}
	if env, ok := svc["environment"].(map[string]any); ok {
		p.Env = map[string]string{}
		for k, v := range env {
			p.Env[k] = fmt.Sprintf("%v", v)
		}
	}
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
					"unsupported deploy field %q (supported: replicas, resources.limits)", key)
			} else if !allowed {
				return nil, "", invalidf(field+".deploy."+key,
					"deploy field %q is managed by the platform and cannot be set from compose", key)
			}
		}
		if r, ok := deploy["replicas"].(int); ok && r > 0 {
			p.Replicas = int64(r) //nolint:gosec // int→int64 域内
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
	// Secret 引用（B2）：短语法 [name...] 或长语法 {source: name}——值
	// 永不进 compose（经 fleetly secrets put 落库，注入时解析，ADR-0014）。
	// 长语法只认 source（target==name 容忍）；uid/gid/mode 等其余子键显式
	// 拒绝（N0.1 P2-3：此前静默丢弃）。
	if secs, ok := svc["secrets"].([]any); ok {
		for _, raw := range secs {
			var name string
			switch v := raw.(type) {
			case string:
				name = v
			case map[string]any:
				for key := range v {
					if key != "source" && key != "target" {
						return nil, "", invalidf(field+".secrets",
							"secret key %q is not supported (long syntax supports source only; uid/gid/mode are not translated)", key)
					}
				}
				name, _ = v["source"].(string)
				if target, ok := v["target"].(string); ok && target != "" && target != name {
					return nil, "", invalidf(field+".secrets", "secret %q: target remapping is not supported (injected at /run/secrets/<name>)", name)
				}
			}
			if name == "" {
				return nil, "", invalidf(field+".secrets", "secret entry must be a name or {source: name}")
			}
			p.SecretRefs = append(p.SecretRefs, name)
		}
	}
	if hc, ok := svc["healthcheck"].(map[string]any); ok {
		probe, err := composeHealthcheck(field+".healthcheck", hc)
		if err != nil {
			return nil, "", err
		}
		p.Healthcheck = probe
	}
	return p, p.GetImage(), nil
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
		words := make([]string, 0, len(test))
		for _, w := range test {
			words = append(words, fmt.Sprintf("%v", w))
		}
		var exec *specv1.ExecProbe
		switch words[0] {
		case "CMD":
			exec = &specv1.ExecProbe{Command: words[1:]}
		case "CMD-SHELL":
			exec = &specv1.ExecProbe{Command: strings.Fields(strings.Join(words[1:], " "))}
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

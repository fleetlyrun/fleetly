// 归一化两源（F0.8）：Compose 受控子集与镜像直投统一归一成 AppSpec
// （架构 §4：归一化结果即 Revision 冻结体，部署基线永远以 Revision 为准，
// 不回读源文件）。Compose 白名单只增；受管/未知字段显式拒绝且理由精确。
package spec

import (
	"fmt"
	"strconv"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// composeWhitelist 是受控子集的白名单（compose 顶层与服务键；只增）。
var composeWhitelist = map[string]bool{
	"services": true, // 顶层唯一入口（name/networks/volumes 顶层随对应批次）
}

var composeServiceWhitelist = map[string]bool{
	"image":       true,
	"command":     true,
	"environment": true,
	"ports":       true,
	"deploy":      true,
	"healthcheck": true,
	"networks":    true,
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
func ImageDeploy(appID, projectID, image string, processName string) (*specv1.AppSpec, error) {
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
			Replicas: 1,
		}},
	}
	if err := ValidateApp(spec); err != nil {
		return nil, err
	}
	return spec, nil
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
				"unsupported compose field (supported: services); use the platform API for this concern")
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

func composeHealthcheck(field string, hc map[string]any) (*specv1.HealthcheckSpec, error) {
	out := &specv1.HealthcheckSpec{Retries: 3}
	if test, ok := hc["test"].([]any); ok && len(test) > 1 {
		// compose test 形态 ["CMD", ...] / ["CMD-SHELL", cmd]——exec 探针
		//（http/tcp 探针经平台 API 声明，compose 无对应原语）。
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
		}
		if exec != nil {
			out.Probe = &specv1.HealthcheckSpec_Exec{Exec: exec}
		}
	}
	_ = field
	return out, nil
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

package compose

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// 本文件实现 §2.4 受控子集校验的第一层：在 compose-go canonical dict 上
// 做「逐字段白名单 + 显式拒绝清单 + 受管字段」判定。选择在 dict 层而非
// typed 层做白名单的原因：typed 解码会丢失「键存在但值为零值」的信息
// （如 privileged: false 与未写不可区分），而受控子集契约的对象是书写
// 面本身——写了不支持的字段就要显式报错，不静默放行。
//
// 校验不静默（§2.4）：每处违规产出独立错误并携带违规字段路径上下文。
// 多处违规时报告首个（fail-fast，与 docker stack 逐字段报错习惯一致）。

// topLevelWhitelist 是顶层键白名单（§2.4 支持清单；version 为 compose 规范
// 废弃键、loader 在 schema 校验后删除，此处天然不可见；x-* 扩展键为
// compose 标准扩展位、loader 移入 Extensions，同样不可见；secrets 已移入
// 拒绝清单——S16-C1，见 topLevelRejectList）。
var topLevelWhitelist = map[string]bool{
	"name":     true,
	"services": true,
	"networks": true,
	"volumes":  true,
}

// topLevelRejectList 是顶层键显式拒绝清单（优先于白名单缺省拒绝，message
// 给出契约理由）。
var topLevelRejectList = map[string]string{
	// S16-C1：secrets 在 Load 期即拒——v0.1 平台密钥库未接入，放行只会在
	// preparing 晚期被规划层拒绝（错误码还误导为运行时问题）；显式拒绝比
	// 「照抄文档示例必失败」诚实。v0.2 平台密钥库接入后解除。
	"secrets": "v0.1 平台密钥库未接入，secrets 不受支持（显式拒绝；v0.2 平台密钥库接入后开放）",
}

// serviceRejectList 是拒绝清单+危险字段的显式条目（优先于通用白名单缺省
// 拒绝，message 给出契约理由）。reason 前缀约定见各条目。
var serviceRejectList = map[string]string{
	// v0.1 拒绝清单（架构 §2.4）
	"depends_on": "v0.1 拒绝清单字段（编排顺序由平台发布管线管理）",
	"extends":    "v0.1 拒绝清单字段（服务复用不受支持）",
	"include":    "v0.1 拒绝清单字段（多文件合并不受支持）",
	"profiles":   "v0.1 拒绝清单字段（服务按全量部署，无 profile 门控）",
	"configs":    "v0.1 拒绝清单字段（配置注入用 environment/secrets 承载）",
	// S16-C1：secrets 在 Load 期即拒（与顶层 topLevelRejectList 同理由）——
	// v0.1 平台密钥库未接入，规划层晚期拒绝（E_RUNTIME_UNAVAILABLE）不可达
	// 于此；planner.go 的快速失败分支保留作纵深。
	"secrets": "v0.1 平台密钥库未接入，secrets 不受支持（显式拒绝；v0.2 平台密钥库接入后开放）",
	// 危险字段（Coolify CVE-2025-34159 根因类；默认拒绝，admin 显式开启
	// + 审计的旁路为后续票 TODO）
	"privileged":          "危险字段：特权容器默认拒绝（admin 旁路 TODO）",
	"cap_add":             "危险字段：内核能力提升默认拒绝（admin 旁路 TODO）",
	"cap_drop":            "危险字段家族：内核能力操作默认拒绝（admin 旁路 TODO）",
	"pid":                 "危险字段：宿主 PID 命名空间默认拒绝（admin 旁路 TODO）",
	"devices":             "危险字段：设备挂载默认拒绝（admin 旁路 TODO）",
	"device_cgroup_rules": "危险字段：设备 cgroup 规则默认拒绝（admin 旁路 TODO）",
	"network_mode":        "network_mode: host 等宿主网络模式在拒绝清单（服务一律经 app 专属网络）",
	"ports":               "宿主端口发布不在 v0.1 受控子集（路由用 expose + fleetly.domains）",
	"external_links":      "v0.1 拒绝清单字段（跨栈链接不受支持）",
	"links":               "legacy 链接不在受控子集（服务互访用 compose 服务名）",
	"container_name":      "容器名由平台管理（Swarm 服务名 fleetly-<app>-<service>）",
}

// serviceWhitelist 是服务级键白名单（§2.4 支持清单逐项）。
var serviceWhitelist = map[string]bool{
	"build":             true,
	"image":             true,
	"command":           true, // §2.4 示例（worker: command: node worker.js）
	"expose":            true, // 路由目标端口
	"labels":            true, // 平台 label 约定载体
	"healthcheck":       true,
	"environment":       true,
	"env_file":          true, // 允许但仅限非密钥（架构 §2.4 密钥行）
	"volumes":           true, // 命名卷挂载（bind/tmpfs 语义层拒绝）
	"networks":          true, // 栈内网络
	"deploy":            true,
	"stop_signal":       true,
	"stop_grace_period": true,
}

// buildWhitelist：build 段仅保留平台消费子键（§2.4 build 注释只定义
// context/dockerfile 的模式裁决；构建引擎参数不在支持清单）。
var buildWhitelist = map[string]bool{
	"context":    true,
	"dockerfile": true,
}

// healthcheckWhitelist：平台默认值覆盖 interval/timeout/retries/start_period
// 四子字段（5s/3s/3/10s，§2.4）；disable/start_interval 不在支持清单。
var healthcheckWhitelist = map[string]bool{
	"test":         true,
	"interval":     true,
	"timeout":      true,
	"retries":      true,
	"start_period": true,
}

// deployWhitelist：deploy.* 除受管字段外照用（release-semantics §2.8 列举：
// parallelism/delay/restart_policy/resources/replicas/placement），加上
// update_config 与 mode（mode=global 的取值级拒绝见 validateDeployDict，
// M1-4：v0.1 单节点不支持 global）。endpoint_mode/rollback_config/labels
// 不在列举内（rollback 平台侧 opt-in、永不使用 Swarm 原生回滚，D-REL-1）。
var deployWhitelist = map[string]bool{
	"mode":           true,
	"replicas":       true,
	"update_config":  true,
	"resources":      true,
	"restart_policy": true,
	"placement":      true,
}

var updateConfigWhitelist = map[string]bool{
	"order":          true,
	"parallelism":    true,
	"delay":          true,
	"failure_action": true, // 受管字段：必须 pause（校验见后）
	"monitor":        true, // 受管字段：省略或 5s
}

var resourcesWhitelist = map[string]bool{"limits": true}
var resourceLimitsWhitelist = map[string]bool{"cpus": true, "memory": true}
var restartPolicyWhitelist = map[string]bool{
	"condition": true, "delay": true, "max_attempts": true, "window": true,
}
var placementWhitelist = map[string]bool{"constraints": true}

// volumeLongWhitelist 是服务卷挂载允许的子键。短语法经 canonical transform
// 解析为长语法映射，encode 会附带空对象子键（volume/bind）——bind 形态
// 仍由 type=volume 限定拒绝（错误信息更精确）。
var volumeLongWhitelist = map[string]bool{
	"type": true, "source": true, "target": true, "read_only": true,
	"volume": true, "bind": true,
}

// S16-C1：secrets（服务级与顶层）在 Load 期即拒——见 serviceRejectList 与
// topLevelRejectList 条目；secretLongWhitelist 与两个形态校验函数
//（validateServiceSecretsDict / validateSecretsDict）随之删除（拒绝先于
// 形态校验，无从到达）；规划层（planner.go）的晚期快速失败分支保留作纵深。

// envFileLongWhitelist 是 env_file 长语法允许的子键。
var envFileLongWhitelist = map[string]bool{"path": true, "required": true, "format": true}

// networkDefReject：顶层网络定义拒绝的键（外部网络在 v0.1 拒绝清单；name
// 覆写与平台命名纪律冲突——网络名由平台按 app 专属命名）。
// 其余 compose 标准网络键（driver/driver_opts/ipam/internal/attachable/
// labels）按「栈内网络」支持面放行。
var networkDefReject = map[string]string{
	"external": "外部网络在 v0.1 拒绝清单（app 专属 overlay 网络由平台创建）",
	"name":     "网络资源名由平台管理，不接受 name 覆写",
}

// volumeDefReject：顶层卷定义拒绝的键（卷由平台卷注册表管理）。
var volumeDefReject = map[string]string{
	"external": "外部卷不受支持（应用卷由平台按 app 创建并登记）",
	"name":     "卷资源名由平台管理（fleetly-<app>-<key>），不接受 name 覆写",
}

// specNamePattern 校验顶层 name 形态（与 compose 项目名字符集一致）。
var specNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validSpecName 判定应用标识是否合法。
func validSpecName(name string) bool { return specNamePattern.MatchString(name) }

// validateDict 在 canonical dict 上执行受控子集校验。abs 仅用于错误信息
// 定位；返回首个违规（fail-fast）。
func validateDict(abs string, dict map[string]any) error {
	// 顶层 name = 应用标识（平台按它命名 Swarm 资源 fleetly-<app>-*）。
	// 缺失在 loader 层已报错（schema 校验开启 + SkipNormalization 组合）；
	// 此处校验形态：小写字母/数字开头，仅含小写字母/数字/-/_（与 compose
	// 项目名字符集一致，保证 Swarm/卷/网络命名安全）。
	name, _ := dict["name"].(string)
	if name == "" {
		return errCompose("compose 缺少顶层 name（应用标识，如 name: my-api）").WithContext("path", "name")
	}
	if !validSpecName(name) {
		return errCompose("compose 顶层 name %q 非法（要求 ^[a-z0-9][a-z0-9_-]*$：小写字母/数字开头，仅小写字母/数字/-/_）", name).
			WithContext("path", "name")
	}

	for _, key := range sortedKeys(dict) {
		if strings.HasPrefix(key, "x-") {
			continue
		}
		if reason, rejected := topLevelRejectList[key]; rejected {
			return errCompose("compose 顶层字段 %q：%s", key, reason).
				WithContext("path", key)
		}
		if !topLevelWhitelist[key] {
			return errCompose("compose 顶层字段 %q 不在受控子集（支持：name/services/networks/volumes）", key).
				WithContext("path", key)
		}
	}

	servicesDict, _ := dict["services"].(map[string]any)
	if len(servicesDict) == 0 {
		return errCompose("compose 未声明任何服务（services 不能为空）").WithContext("path", "services")
	}

	for _, name := range sortedKeys(servicesDict) {
		svcDict, ok := servicesDict[name].(map[string]any)
		if !ok {
			return errCompose("服务 %q 定义必须是映射", name).WithContext("path", "services."+name)
		}
		if err := validateServiceDict(name, svcDict); err != nil {
			return err
		}
	}

	if err := validateNetworksDict(dict); err != nil {
		return err
	}
	if err := validateVolumesDict(dict); err != nil {
		return err
	}
	return nil
}

// validateServiceDict 对单个服务执行白名单/拒绝清单/受管字段校验。
func validateServiceDict(name string, svc map[string]any) error {
	prefix := "services." + name
	for _, key := range sortedKeys(svc) {
		if strings.HasPrefix(key, "x-") {
			continue
		}
		if reason, rejected := serviceRejectList[key]; rejected {
			return errCompose("服务 %q 使用不受支持的字段 %q：%s", name, key, reason).
				WithContext("path", prefix+"."+key)
		}
		if !serviceWhitelist[key] {
			return errCompose("服务 %q 的字段 %q 不在受控子集支持清单", name, key).
				WithContext("path", prefix+"."+key)
		}
	}

	// 服务必须有运行体：image 或 build 至少声明一项（§2.4：build/image 两
	// 种模式；两者皆无则无物可部署）。
	_, hasImage := svc["image"]
	_, hasBuild := svc["build"]
	if !hasImage && !hasBuild {
		return errCompose("服务 %q 未声明 image 或 build（无运行体）", name).WithContext("path", prefix)
	}

	if buildDict, ok := svc["build"].(map[string]any); ok {
		if err := checkSubKeys(name, prefix+".build", "build", buildDict, buildWhitelist); err != nil {
			return err
		}
	}
	if hcDict, ok := svc["healthcheck"].(map[string]any); ok {
		if err := checkSubKeys(name, prefix+".healthcheck", "healthcheck", hcDict, healthcheckWhitelist); err != nil {
			return err
		}
	}
	if envDict, ok := svc["environment"].(map[string]any); ok {
		// 变量插值关闭 → 裸键（无值，期望从宿主环境透传）无法确定性归一化，
		// 显式拒绝。
		for _, k := range sortedKeys(envDict) {
			if envDict[k] == nil {
				return errCompose("服务 %q 的 environment 条目 %q 未给字面值（fleetly 关闭变量插值与环境透传）", name, k).
					WithContext("path", prefix+".environment."+k)
			}
		}
	} else if envList, ok := svc["environment"].([]any); ok {
		// 列表形态（canonical transform 不改写 environment）：每项必须是
		// "KEY=VALUE" 字面赋值；裸键（透传）拒绝，理由同上。
		for i, e := range envList {
			s, _ := e.(string)
			if !strings.Contains(s, "=") {
				return errCompose("服务 %q 的 environment 条目 %q 未给字面值（fleetly 关闭变量插值与环境透传）", name, s).
					WithContext("path", fmt.Sprintf("%s.environment[%d]", prefix, i))
			}
		}
	}
	if err := validateEnvFileDict(name, prefix, svc["env_file"]); err != nil {
		return err
	}
	if err := validateServiceVolumesDict(name, prefix, svc["volumes"]); err != nil {
		return err
	}
	if err := validateServiceNetworksDict(name, prefix, svc["networks"]); err != nil {
		return err
	}
	if err := validateDeployDict(name, prefix, svc); err != nil {
		return err
	}
	return nil
}

// validateDeployDict 校验 deploy 段：白名单 + 受管字段 + 更新策略安全性。
// svcDict 为整个服务定义（order 安全性判定需要卷挂载与 mode 信息）。
func validateDeployDict(name, prefix string, svcDict map[string]any) error {
	deployAny, present := svcDict["deploy"]
	if !present || deployAny == nil {
		return nil
	}
	deploy, ok := deployAny.(map[string]any)
	if !ok {
		return errCompose("服务 %q 的 deploy 必须是映射", name).WithContext("path", prefix+".deploy")
	}
	if err := checkSubKeys(name, prefix+".deploy", "deploy", deploy, deployWhitelist); err != nil {
		return err
	}

	// M1-4（v0.1 诚实裁决）：deploy.mode: global 显式拒绝——单节点拓扑下
	// global 的副本语义（每节点一实例，单机即恒 1）与失败停机语义（scale=0
	// 对 global 无效，首发失败后崩溃循环不会停）均未实现；放行只会得到
	// 无法停机的失败现场（对齐 C1 secrets 先例：Load 期即拒优于发布期晚败
	// 且误导）。v0.2 多节点开放后解除。
	if mode, present := deploy["mode"]; present {
		if s, _ := mode.(string); s == "global" {
			return apperr.New("E_COMPOSE_UNSUPPORTED",
				"服务 %q 声明 deploy.mode: global：v0.1 单节点不支持 global 模式（副本语义与失败停机语义未实现；v0.2 开放，请用 replicated + replicas 表达）", name).
				WithContext("path", prefix+".deploy.mode")
		}
	}

	if ucAny, ok := deploy["update_config"]; ok && ucAny != nil {
		uc, ok := ucAny.(map[string]any)
		if !ok {
			return errCompose("服务 %q 的 deploy.update_config 必须是映射", name).
				WithContext("path", prefix+".deploy.update_config")
		}
		if err := checkSubKeys(name, prefix+".deploy.update_config", "update_config", uc, updateConfigWhitelist); err != nil {
			return err
		}
		// 受管字段政策（release-semantics §2.8）：校验拒绝、不静默覆盖。
		if fa, present := uc["failure_action"]; present {
			if s, _ := fa.(string); s != "pause" {
				return apperr.New("E_COMPOSE_MANAGED_FIELD",
					"服务 %q 的 deploy.update_config.failure_action=%q 违反平台治理（必须为 pause 或省略——发布失败动作由平台固定 pause）", name, fmt.Sprint(fa)).
					WithContext("path", prefix+".deploy.update_config.failure_action")
			}
		}
		if mon, present := uc["monitor"]; present {
			if s, _ := mon.(string); s != "5s" {
				return apperr.New("E_COMPOSE_MANAGED_FIELD",
					"服务 %q 的 deploy.update_config.monitor=%q 违反平台治理（必须为 5s 或省略——更新监控窗由平台固定）", name, fmt.Sprint(mon)).
					WithContext("path", prefix+".deploy.update_config.monitor")
			}
		}
		// 更新顺序安全性：有卷服务强制 stop-first（发布降级边界：双任务并发
		// 挂同一本地卷有数据风险），显式 start-first 冲突 → UNSAFE_STRATEGY。
		//（global + start-first 的同型检查已随 M1-4 删除——mode: global 在
		// 本函数更早处整体拒绝，该分支不可达。）
		if order, present := uc["order"]; present {
			if s, _ := order.(string); s == "start-first" && serviceHasVolumes(svcDict) {
				return apperr.New("E_COMPOSE_UNSAFE_STRATEGY",
					"服务 %q 声明了命名卷挂载，显式 start-first 与平台强制 stop-first 冲突（双任务并发挂同一本地卷有数据风险）", name).
					WithContext("path", prefix+".deploy.update_config.order")
			}
		}
	}

	if resAny, ok := deploy["resources"]; ok && resAny != nil {
		res, ok := resAny.(map[string]any)
		if !ok {
			return errCompose("服务 %q 的 deploy.resources 必须是映射", name).
				WithContext("path", prefix+".deploy.resources")
		}
		if err := checkSubKeys(name, prefix+".deploy.resources", "resources", res, resourcesWhitelist); err != nil {
			return err
		}
		if limitsAny, ok := res["limits"]; ok && limitsAny != nil {
			limits, ok := limitsAny.(map[string]any)
			if !ok {
				return errCompose("服务 %q 的 deploy.resources.limits 必须是映射", name).
					WithContext("path", prefix+".deploy.resources.limits")
			}
			if err := checkSubKeys(name, prefix+".deploy.resources.limits", "limits", limits, resourceLimitsWhitelist); err != nil {
				return err
			}
		}
	}

	if rpAny, ok := deploy["restart_policy"]; ok && rpAny != nil {
		rp, ok := rpAny.(map[string]any)
		if !ok {
			return errCompose("服务 %q 的 deploy.restart_policy 必须是映射", name).
				WithContext("path", prefix+".deploy.restart_policy")
		}
		if err := checkSubKeys(name, prefix+".deploy.restart_policy", "restart_policy", rp, restartPolicyWhitelist); err != nil {
			return err
		}
	}

	if placeAny, ok := deploy["placement"]; ok && placeAny != nil {
		place, ok := placeAny.(map[string]any)
		if !ok {
			return errCompose("服务 %q 的 deploy.placement 必须是映射", name).
				WithContext("path", prefix+".deploy.placement")
		}
		if err := checkSubKeys(name, prefix+".deploy.placement", "placement", place, placementWhitelist); err != nil {
			return err
		}
		if constraints, ok := place["constraints"].([]any); ok {
			for i, c := range constraints {
				expr, _ := c.(string)
				if !placementConstraintAllowed(expr) {
					return errCompose("服务 %q 的放置约束 %q 超出命名空间（仅允许 node.labels.fleetly.*）", name, expr).
						WithContext("path", fmt.Sprintf("%s.deploy.placement.constraints[%d]", prefix, i))
				}
			}
		}
	}
	return nil
}

// serviceHasVolumes 判断服务是否声明了卷挂载（canonical dict 层：长语法
// 映射或短语法字符串）。
func serviceHasVolumes(svc map[string]any) bool {
	switch v := svc["volumes"].(type) {
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return false
	}
}

// placementConstraintAllowed 判定单条 Swarm 约束表达式是否落在
// node.labels.fleetly.* 命名空间（stateful-placement §2.3）。支持
// ==/!=/in/notin 运算形态；无法识别的形态一律拒绝（白名单纪律）。
func placementConstraintAllowed(expr string) bool {
	s := strings.TrimSpace(expr)
	ops := []string{"==", "!=", " notin ", " in ", "notin ", "in "}
	head := ""
	for _, op := range ops {
		if idx := strings.Index(s, op); idx > 0 {
			head = strings.TrimSpace(s[:idx])
			break
		}
	}
	if head == "" {
		// 纯存在性约束（如 node.labels.fleetly.rack）也视为合法头。
		head = strings.TrimSuffix(s, " ")
	}
	return strings.HasPrefix(head, "node.labels.fleetly.")
}

// validateEnvFileDict 校验 env_file 形态。canonical transform（transformEnvFile）
// 把所有形态归一为「映射列表」：字符串项 → {path, required: true}，映射项
// 补 required 缺省；白名单子键 {path, required, format}。
func validateEnvFileDict(name, prefix string, envFileAny any) error {
	if envFileAny == nil {
		return nil
	}
	items, ok := envFileAny.([]any)
	if !ok {
		// canonical 化后理论上不可达；兜底拒绝异常形态。
		return errCompose("服务 %q 的 env_file 形态不支持（字符串或列表）", name).
			WithContext("path", prefix+".env_file")
	}
	for i, item := range items {
		path := fmt.Sprintf("%s.env_file[%d]", prefix, i)
		m, ok := item.(map[string]any)
		if !ok {
			return errCompose("服务 %q 的 env_file 列表项类型不支持", name).WithContext("path", path)
		}
		if err := checkSubKeysAt(name, path, "env_file", m, envFileLongWhitelist); err != nil {
			return err
		}
	}
	return nil
}

// validateServiceVolumesDict 校验服务卷挂载形态。canonical transform
// （transformVolumeMount）把短语法解析为长语法映射：{type, source, target,
// read_only?}；不可解析形态（ignoreParseError）保留字符串、由 typed 解码
// 兜底报错。type 限定 volume（bind/tmpfs 拒绝）；宿主 bind 与 docker.sock
// 的语义判定在 typed 层补刀。
func validateServiceVolumesDict(name, prefix string, volumesAny any) error {
	if volumesAny == nil {
		return nil
	}
	items, ok := volumesAny.([]any)
	if !ok {
		return errCompose("服务 %q 的 volumes 必须是列表", name).WithContext("path", prefix+".volumes")
	}
	for i, item := range items {
		path := fmt.Sprintf("%s.volumes[%d]", prefix, i)
		switch v := item.(type) {
		case string:
			// canonical 化残留的不可解析短语法：仅接受 mode 段 ro/rw。
			parts := strings.Split(v, ":")
			if len(parts) == 3 {
				mode := strings.TrimSpace(parts[2])
				if mode != "ro" && mode != "rw" {
					return errCompose("服务 %q 的卷挂载选项 %q 不支持（仅 ro/rw）", name, parts[2]).WithContext("path", path)
				}
			}
		case map[string]any:
			if err := checkSubKeysAt(name, path, "volumes", v, volumeLongWhitelist); err != nil {
				return err
			}
			if t, present := v["type"]; present {
				if s, _ := t.(string); s != "volume" {
					return errCompose("服务 %q 的卷挂载 type=%q 不受支持（v0.1 仅命名卷；bind/tmpfs 拒绝）", name, fmt.Sprint(t)).
						WithContext("path", path+".type")
				}
			}
		default:
			return errCompose("服务 %q 的卷挂载项类型不支持", name).WithContext("path", path)
		}
	}
	return nil
}

// validateServiceNetworksDict 校验服务网络引用形态：短语法列表或「键为
// 网络名、值为空」的映射（aliases/ipam 等每网络配置不在支持清单——服务
// 别名 = compose 服务名，平台管理）。
func validateServiceNetworksDict(name, prefix string, networksAny any) error {
	if networksAny == nil {
		return nil
	}
	switch v := networksAny.(type) {
	case []any:
		return nil // 短语法
	case map[string]any:
		for _, net := range sortedKeys(v) {
			if conf := v[net]; conf != nil {
				if m, ok := conf.(map[string]any); !ok || len(m) > 0 {
					return errCompose("服务 %q 的网络 %q 携带配置（aliases/ipam 等不在支持清单；服务别名由平台按 compose 服务名管理）", name, net).
						WithContext("path", prefix+".networks."+net)
				}
			}
		}
		return nil
	default:
		return errCompose("服务 %q 的 networks 形态不支持", name).WithContext("path", prefix+".networks")
	}
}

// validateNetworksDict 校验顶层网络定义（外部网络/name 覆写拒绝）。
func validateNetworksDict(dict map[string]any) error {
	netsAny, ok := dict["networks"]
	if !ok || netsAny == nil {
		return nil
	}
	nets, ok := netsAny.(map[string]any)
	if !ok {
		return errCompose("顶层 networks 必须是映射").WithContext("path", "networks")
	}
	for _, net := range sortedKeys(nets) {
		def, ok := nets[net].(map[string]any)
		if !ok {
			continue // 空定义合法（栈内默认网络）
		}
		for _, key := range sortedKeys(def) {
			if reason, rejected := networkDefReject[key]; rejected {
				return errCompose("网络 %q 的 %q：%s", net, key, reason).
					WithContext("path", "networks."+net+"."+key)
			}
		}
	}
	return nil
}

// validateVolumesDict 校验顶层卷定义。
func validateVolumesDict(dict map[string]any) error {
	volsAny, ok := dict["volumes"]
	if !ok || volsAny == nil {
		return nil
	}
	vols, ok := volsAny.(map[string]any)
	if !ok {
		return errCompose("顶层 volumes 必须是映射").WithContext("path", "volumes")
	}
	for _, vol := range sortedKeys(vols) {
		def, ok := vols[vol].(map[string]any)
		if !ok {
			continue // 空定义合法（§2.4 示例 volumes: data:）
		}
		for _, key := range sortedKeys(def) {
			if reason, rejected := volumeDefReject[key]; rejected {
				return errCompose("卷 %q 的 %q：%s", vol, key, reason).
					WithContext("path", "volumes."+vol+"."+key)
			}
		}
	}
	return nil
}

// checkSubKeys 是子段白名单的统一入口（带服务名）。
func checkSubKeys(name, path, section string, dict map[string]any, whitelist map[string]bool) error {
	return checkSubKeysAt(name, path, section, dict, whitelist)
}

// checkSubKeysAt 对映射子段逐键白名单校验。
func checkSubKeysAt(name, path, section string, dict map[string]any, whitelist map[string]bool) error {
	for _, key := range sortedKeys(dict) {
		if strings.HasPrefix(key, "x-") {
			continue
		}
		if !whitelist[key] {
			return errCompose("服务 %q 的 %s.%s 不在受控子集支持清单", name, section, key).
				WithContext("path", path+"."+key)
		}
	}
	return nil
}

package compose

import (
	"fmt"
	"sort"
	"strings"
	"time"

	// time/tzdata 内嵌 IANA 时区库：cron 时区 label（fleetly.cron.timezone）
	// 的 LoadLocation 在 scratch/alpine 等无系统 zoneinfo 形态下仍可解析
	//（E5 Cron；~450KB 二进制体积换可移植性）。
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"github.com/oklog/ulid/v2"
	"golang.org/x/net/idna"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// 平台 label 契约常量（架构 §2.4 平台约定表、state-model §2.4 保留前缀）。
const (
	LabelDomains       = "fleetly.domains"
	LabelPlacementNode = "fleetly.placement.node"
	LabelCron          = "fleetly.cron"
	LabelCronTimezone  = "fleetly.cron.timezone"
	LabelCronTimeout   = "fleetly.cron.timeout"
	// LabelJob 是部署期一次性作业声明（DT-4，torchwood 线）：值词表仅
	// "init"——发布管线在晋级（长驻服务对账）前以新 spec 跑一次性 job，
	// 全过才晋级；与 fleetly.cron 互斥（同一服务的两种一次性执行语义不可
	// 并存）。见 parseJobLabels 的值契约。
	LabelJob = "fleetly.job"
	// LabelJobTimeout 是 init job 看门狗预算（fleetly.cron.timeout 同形态：
	// 正 Go duration；缺省平台预算 10m）。孤儿 label（无 fleetly.job）在
	// 解析期拒绝——与 cron 家族的孤儿纪律同款。
	LabelJobTimeout = "fleetly.job.timeout"
	// LabelS3 是对象存储凭证注入开关（E3 对象存储 §2.4/D-S3-6）：值为
	// "true" 的服务由发布引擎注入 S3 system env（source=system——env 三层
	// 合并链的最高层）；rustfs 模式额外牵线平台内部网络（E3-4）。label 出
	// 现而 s3.mode=unset → 部署规划期 E_S3_NOT_CONFIGURED（诚实拒绝）。
	LabelS3 = "fleetly.s3"

	// LabelDatabases 是库引用声明 label（E4 托管数据库，managed-databases
	// §2.4/D-DB-4）：值为逗号分隔的库实例名列表（单一真源 = compose——
	// 与 fleetly.domains 同载体同风格；不建与 compose 并行的期望态）。发布
	// 引擎解析：实例存在性哨兵（E_DB_NOT_FOUND）、env 前缀冲突哨兵
	//（E_DB_ENV_PREFIX_CONFLICT）、未就绪计划警告（W_DB_REFERENCE_NOT_READY）。
	LabelDatabases = "fleetly.databases"

	// LabelNamespace 是平台保留 label 命名空间前缀：用户占用约定键之外的
	// fleetly.* 键 → E_LABEL_RESERVED（422）。
	LabelNamespace = "fleetly."

	// domain 契约上限（架构 §2.4）：每服务 ≤5、每 app ≤10。
	maxDomainsPerService = 5
	maxDomainsPerApp     = 10

	// jobLabelInitValue 是 fleetly.job 的唯一合法值（DT-4：部署期 init job）。
	jobLabelInitValue = "init"
)

// knownFleetlyLabels 是平台承认的平台约定键全集（cron 家族自 E5 Cron 起
// 生效——值契约见 parseCronSchedule；s3 为 E3-4 起的生效契约键；databases
// 为 E4 起的生效契约键——值契约见 parseDatabasesLabel；job 家族自 DT-4 起
// 生效——值契约见 parseJobLabels）。
var knownFleetlyLabels = map[string]bool{
	LabelDomains:       true,
	LabelPlacementNode: true,
	LabelCron:          true,
	LabelCronTimezone:  true,
	LabelCronTimeout:   true,
	LabelJob:           true,
	LabelJobTimeout:    true,
	LabelS3:            true,
	LabelDatabases:     true,
}

// parseS3Label 校验 fleetly.s3 label 值并给出开关结论（E3-4）：契约形态
// 是字面 "true"（设计 §2.4 的唯一书写形态）；其他值一律拒绝——静默容忍
// "false"/拼写错误（如 "ture"）会制造「以为关了/以为开了」的悬案，fail
// -loud 比猜意图诚实。错误码取保留命名空间的既有码（值违反键契约 =
// 该键没有被正确使用；不发明新码，注册表只增纪律）。
func parseS3Label(service, value string) error {
	if value == "true" {
		return nil
	}
	return apperr.New("E_LABEL_RESERVED",
		"service %q declares label %q with value %q (the only accepted value is %q)",
		service, LabelS3, value, "true").
		WithContext("path", "services."+service+".labels."+LabelS3).
		WithContext("reason", "invalid_value")
}

// parseDatabasesLabel 解析 fleetly.databases label 值（E4 托管数据库，
// managed-databases §2.4/D-DB-4）：逗号分隔的库实例名列表 → trim 归一化、
// 排序（书写顺序不影响 spec_hash）。形态契约：
//   - 空条目（空串/纯空白）拒绝；
//   - 每个名字必须匹配库实例名字符集 ^[a-z0-9][a-z0-9_-]*$（与 compose
//     顶层 name / 库实例名同规则——state.ValidateDatabaseName 的同一形态，
//     compose 层复用 specNamePattern）；
//   - 同服务重复引用同名实例拒绝（重复声明是「以为引了两次」的歧义形态，
//     fail-loud 比静默去重诚实）。
//
// 形态违规 → E_COMPOSE_UNSUPPORTED（label 值形态是 compose 书写面契约；
// 实例存在性属引擎规划期前哨——E_DB_NOT_FOUND，解析层不拥有库清单）。
// 返回排序后的实例名列表。
func parseDatabasesLabel(service, value string) ([]string, error) {
	rawItems := strings.Split(value, ",")
	names := make([]string, 0, len(rawItems))
	seen := map[string]bool{}
	for i, raw := range rawItems {
		name := strings.TrimSpace(raw)
		pathCtx := fmt.Sprintf("services.%s.labels.%s[%d]", service, LabelDatabases, i)
		if name == "" {
			return nil, errCompose("database reference list of service %q contains an empty entry (%q expects a comma-separated list of database instance names)", service, LabelDatabases).
				WithContext("path", pathCtx).WithContext("reason", "empty_entry")
		}
		if !validSpecName(name) {
			return nil, errCompose("database reference %q of service %q has an invalid name (%s must match ^[a-z0-9][a-z0-9_-]*$: starts with a lowercase letter or digit, only lowercase letters/digits/-/_ allowed)", name, service, LabelDatabases).
				WithContext("path", pathCtx).WithContext("reason", "invalid_name")
		}
		if seen[name] {
			return nil, errCompose("service %q references database instance %q more than once (duplicate entries in %s are rejected; declare each instance once)", service, name, LabelDatabases).
				WithContext("path", pathCtx).WithContext("reason", "duplicate_entry")
		}
		seen[name] = true
		names = append(names, name)
	}
	sortStrings(names)
	return names, nil
}

// parseCronSchedule 解析 fleetly.cron label 家族（E5 Cron，架构 §4.3 声明
// 行）：表达式经 cron.ParseStandard（恰五段标准式——六段含秒与非法式在
// 解析期即拒，契约天然成立）、时区经 time.LoadLocation、超时经
// time.ParseDuration（须 > 0）。校验落 compose 归一化期（fail-loud：非法
// 值在 Load 即拒，不静默当未声明——与 parseS3Label 同一纪律）。错误码取
// 保留命名空间既有码（值违反键契约 = 该键没有被正确使用；注册表只增）。
// 时区数据库经 time/tzdata 内嵌（本包 import，scratch/dind 形态无系统
// zoneinfo 也可解析）。
func parseCronSchedule(service string, labels map[string]string) (*CronSchedule, error) {
	expr := strings.TrimSpace(labels[LabelCron])
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, apperr.New("E_LABEL_RESERVED",
			"service %q declares label %q with an invalid expression %q: %v (the platform contract is the 5-field standard crontab form, e.g. \"*/5 * * * *\"; 6-field expressions with seconds are rejected)",
			service, LabelCron, labels[LabelCron], err).
			WithContext("path", "services."+service+".labels."+LabelCron).
			WithContext("reason", "invalid_expression")
	}
	_ = sched // 值契约校验用；下次触发计算由调度器重新解析（internal/cron）

	out := &CronSchedule{Expression: expr}
	if raw, ok := labels[LabelCronTimezone]; ok {
		tz := strings.TrimSpace(raw)
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, apperr.New("E_LABEL_RESERVED",
				"service %q declares label %q with an unknown timezone %q: %v (IANA location names such as \"Asia/Shanghai\"; defaults to UTC when omitted)",
				service, LabelCronTimezone, raw, err).
				WithContext("path", "services."+service+".labels."+LabelCronTimezone).
				WithContext("reason", "invalid_timezone")
		}
		out.Timezone = loc.String()
	}
	if raw, ok := labels[LabelCronTimeout]; ok {
		v := strings.TrimSpace(raw)
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, apperr.New("E_LABEL_RESERVED",
				"service %q declares label %q with an invalid timeout %q (a positive Go duration such as \"30m\"; defaults to the platform watchdog budget when omitted)",
				service, LabelCronTimeout, raw).
				WithContext("path", "services."+service+".labels."+LabelCronTimeout).
				WithContext("reason", "invalid_timeout")
		}
		out.Timeout = d.String()
	}
	return out, nil
}

// parseJobLabels 解析 fleetly.job label 家族（DT-4 部署期一次性作业）：
// 主 label 值词表仅 "init"（其他值一律拒——fail-loud，不静默当未声明，
// 与 parseS3Label 同一纪律）；时长为正 Go duration（缺省平台预算）。
// 两条交叉契约：
//   - 与 fleetly.cron 互斥：同一服务不能同时声明两种一次性执行语义
//     （E_LABEL_RESERVED + reason=job_cron_exclusive）；
//   - 孤儿时长 label（有 fleetly.job.timeout 无 fleetly.job）拒绝：静默
//     无机会生效的声明是「以为配了」的悬案（cron 孤儿纪律同款）。
//
// replicas 契约与 expose 禁令在 normalize（typed 层）执行——本函数只拥有
// label 值形态（与 parseCronSchedule 的分工一致）。
func parseJobLabels(service string, labels map[string]string) (bool, string, error) {
	raw, declared := labels[LabelJob]
	timeoutRaw, timeoutDeclared := labels[LabelJobTimeout]
	if !declared {
		if timeoutDeclared {
			return false, "", apperr.New("E_LABEL_RESERVED",
				"service %q declares label %q without %q (the timeout refines a one-shot init job; without the job label it would never take effect)",
				service, LabelJobTimeout, LabelJob).
				WithContext("path", "services."+service+".labels."+LabelJobTimeout).
				WithContext("reason", "job_without_declaration")
		}
		return false, "", nil
	}
	if _, cronDeclared := labels[LabelCron]; cronDeclared {
		return false, "", apperr.New("E_LABEL_RESERVED",
			"service %q declares both %q and %q (the two one-shot execution semantics are mutually exclusive: a service is either a scheduled cron job or a release-time init job)",
			service, LabelJob, LabelCron).
			WithContext("path", "services."+service+".labels."+LabelJob).
			WithContext("reason", "job_cron_exclusive")
	}
	value := strings.TrimSpace(raw)
	if value != jobLabelInitValue {
		return false, "", apperr.New("E_LABEL_RESERVED",
			"service %q declares label %q with value %q (the only accepted value is %q: a release-time one-shot job run before the new revision is promoted)",
			service, LabelJob, raw, jobLabelInitValue).
			WithContext("path", "services."+service+".labels."+LabelJob).
			WithContext("reason", "invalid_value")
	}
	out := ""
	if timeoutDeclared {
		v := strings.TrimSpace(timeoutRaw)
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return false, "", apperr.New("E_LABEL_RESERVED",
				"service %q declares label %q with an invalid timeout %q (a positive Go duration such as \"30m\"; defaults to the platform watchdog budget when omitted)",
				service, LabelJobTimeout, timeoutRaw).
				WithContext("path", "services."+service+".labels."+LabelJobTimeout).
				WithContext("reason", "invalid_timeout")
		}
		out = d.String()
	}
	return true, out, nil
}

// idnaProfile 是域名归一化档案：IDN → punycode（架构 §2.4 域名行）。Lookup
// 档案做大小写折叠与合法性校验（拒绝非法字符/超长标签），与证书 SAN 语义
// 对齐。
var idnaProfile = idna.Lookup

// normalizeDomainValue 是域名形态归一化的单点（parseDomainsLabel 与 API
// 域名资源面共用）：trim/小写/IDN→punycode；空条目、通配主机与非法形态
// 返回 reason（empty_entry | wildcard | invalid_form）+ 底层错误文本。
// 通配主机在 app 级 DNS-01 签发链就绪（W5）前不可表达——与 compose label
// 同口径。
func normalizeDomainValue(raw string) (string, string, error) {
	d := strings.TrimSpace(raw)
	if d == "" {
		return "", "empty_entry", fmt.Errorf("domain is empty")
	}
	if strings.HasPrefix(d, "*") {
		return "", "wildcard", fmt.Errorf("wildcard hosts require DNS-01 app-level issuance (W5); use a concrete host for now")
	}
	ascii, err := idnaProfile.ToASCII(strings.ToLower(d))
	if err != nil {
		return "", "invalid_form", err
	}
	return ascii, "", nil
}

// NormalizeDomain 是单域名归一化入口（API 域名资源面的形态契约与 compose
// label 同源）：归一化产物（小写 punycode）或 E_DOMAIN_UNSUPPORTED（reason
// 上下文点名空条目/通配/非法形态）。
func NormalizeDomain(raw string) (string, error) {
	ascii, reason, err := normalizeDomainValue(raw)
	if err != nil {
		return "", apperr.New("E_DOMAIN_UNSUPPORTED", "domain %q has an unsupported form: %v", raw, err).
			WithContext("reason", reason)
	}
	return ascii, nil
}

// parseDomainsLabel 解析 fleetly.domains label 值：逗号分隔列表 → trim/
// 小写/IDN→punycode 归一化、排序去重；通配符与非法形态 →
// E_DOMAIN_UNSUPPORTED；超上限 → E_DOMAIN_UNSUPPORTED（reason 上下文标注）。
// 返回归一化后的域名列表（保序输入、输出排序去重）。
func parseDomainsLabel(service, value string) ([]string, error) {
	rawItems := strings.Split(value, ",")
	domains := make([]string, 0, len(rawItems))
	seen := map[string]bool{}
	for i, raw := range rawItems {
		pathCtx := fmt.Sprintf("services.%s.labels.%s[%d]", service, LabelDomains, i)
		ascii, reason, err := normalizeDomainValue(raw)
		if err != nil {
			msg := fmt.Sprintf("domain %q of service %q has an unsupported form: %v", strings.TrimSpace(raw), service, err)
			switch reason {
			case "wildcard":
				msg = fmt.Sprintf("domain %q of service %q is a wildcard (wildcard certificates require DNS-01, supported from v0.2)", strings.TrimSpace(raw), service)
			case "empty_entry":
				msg = fmt.Sprintf("domain list of service %q contains an empty entry", service)
			}
			return nil, apperr.New("E_DOMAIN_UNSUPPORTED", "%s", msg).
				WithContext("path", pathCtx).WithContext("reason", reason)
		}
		if !seen[ascii] {
			seen[ascii] = true
			domains = append(domains, ascii)
		}
	}
	if len(domains) > maxDomainsPerService {
		return nil, apperr.New("E_DOMAIN_UNSUPPORTED", "service %q declares %d domains, exceeding the per-service limit of %d", service, len(domains), maxDomainsPerService).
			WithContext("path", fmt.Sprintf("services.%s.labels.%s", service, LabelDomains)).
			WithContext("reason", "per_service_limit")
	}
	sortStrings(domains)
	return domains, nil
}

// checkDomainContract 执行跨服务域名契约：同域名出现在两个服务 →
// E_DOMAIN_CONFLICT（409）；全 app 域名总数 ≤10。返回合并后的 app 域名集。
func checkDomainContracts(serviceDomains map[string][]string) error {
	owner := map[string]string{} // domain → 首个占用服务
	total := 0
	for _, svc := range sortedKeys(serviceDomains) {
		total += len(serviceDomains[svc])
		for _, d := range serviceDomains[svc] {
			if prev, ok := owner[d]; ok {
				return apperr.New("E_DOMAIN_CONFLICT", "domain %q appears in both service %q and %q (a domain belongs to exactly one service within an app)", d, prev, svc).
					WithContext("domain", d).
					WithContext("services", prev+","+svc)
			}
			owner[d] = svc
		}
	}
	if total > maxDomainsPerApp {
		return apperr.New("E_DOMAIN_UNSUPPORTED", "the app declares %d domains in total, exceeding the per-app limit of %d", total, maxDomainsPerApp).
			WithContext("path", "services.*.labels."+LabelDomains).
			WithContext("reason", "per_app_limit")
	}
	return nil
}

// checkPlacementLabel 校验 fleetly.placement.node 语法与跨服务一致性
// （stateful-placement §2.2/§2.3）：hostname 或 n_<ULID>；语法非法 →
// E_PLACEMENT_NODE_INVALID（422）；同 app 多服务指向不同节点 →
// E_PLACEMENT_LABEL_CONFLICT（422）。
func checkPlacementLabel(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	firstSvc, firstVal := "", ""
	for _, svc := range sortedKeys(values) {
		v := strings.TrimSpace(values[svc])
		if err := validatePlacementNodeRef(svc, v); err != nil {
			return err
		}
		if firstSvc == "" {
			firstSvc, firstVal = svc, v
			continue
		}
		if v != firstVal {
			return apperr.New("E_PLACEMENT_LABEL_CONFLICT",
				"placement.node of service %q and %q points to different nodes (%q vs %q) — placement granularity is app-level: all services of an app must pin the same node", firstSvc, svc, firstVal, v).
				WithContext("services", firstSvc+","+svc)
		}
	}
	return nil
}

// validatePlacementNodeRef 校验单个 placement.node 引用语法：hostname 或
// n_<ULID>（stateful-placement §2.2）；n_ 前缀必须是合法 ULID，否则 422
// E_PLACEMENT_NODE_INVALID。hostname 形态在此只做非空与非空白检查——名字
// 是否存在属部署前哨校验（E_PLACEMENT_NODE_NOT_FOUND，运行期），解析层不
// 拥有节点清单。
func validatePlacementNodeRef(service, ref string) error {
	pathCtx := "services." + service + ".labels." + LabelPlacementNode
	if ref == "" {
		return apperr.New("E_PLACEMENT_NODE_INVALID", "placement.node of service %q is empty (expected a hostname or n_<ULID>)", service).
			WithContext("path", pathCtx)
	}
	if strings.HasPrefix(ref, "n_") {
		if _, err := ulid.Parse(strings.TrimPrefix(ref, "n_")); err != nil {
			return apperr.New("E_PLACEMENT_NODE_INVALID", "placement.node %q of service %q is not a valid node ID (expected the n_<ULID> form)", service, ref).
				WithContext("path", pathCtx)
		}
		return nil
	}
	if strings.ContainsAny(ref, " \t\r\n") {
		return apperr.New("E_PLACEMENT_NODE_INVALID", "placement.node %q of service %q contains whitespace (expected a hostname or n_<ULID>)", service, ref).
			WithContext("path", pathCtx)
	}
	return nil
}

// sortStrings 原地字典序排序。
func sortStrings(s []string) { sort.Strings(s) }

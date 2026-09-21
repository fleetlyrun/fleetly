package compose

import (
	"fmt"
	"sort"
	"strings"

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
	// LabelS3 是对象存储凭证注入开关（E3 对象存储 §2.4/D-S3-6）：值为
	// "true" 的服务由发布引擎注入 S3 system env（source=system——env 三层
	// 合并链的最高层）；rustfs 模式额外牵线平台内部网络（E3-4）。label 出
	// 现而 s3.mode=unset → 部署规划期 E_S3_NOT_CONFIGURED（诚实拒绝）。
	LabelS3 = "fleetly.s3"

	// LabelNamespace 是平台保留 label 命名空间前缀：用户占用约定键之外的
	// fleetly.* 键 → E_LABEL_RESERVED（422）。
	LabelNamespace = "fleetly."

	// domain 契约上限（架构 §2.4）：每服务 ≤5、每 app ≤10。
	maxDomainsPerService = 5
	maxDomainsPerApp     = 10
)

// knownFleetlyLabels 是平台承认的平台约定键全集（cron 家族为 v0.2 契约，
// v0.1 出现时给警告级提示而非拒绝；s3 为 E3-4 起的生效契约键）。
var knownFleetlyLabels = map[string]bool{
	LabelDomains:       true,
	LabelPlacementNode: true,
	LabelCron:          true,
	LabelCronTimezone:  true,
	LabelCronTimeout:   true,
	LabelS3:            true,
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

// idnaProfile 是域名归一化档案：IDN → punycode（架构 §2.4 域名行）。Lookup
// 档案做大小写折叠与合法性校验（拒绝非法字符/超长标签），与证书 SAN 语义
// 对齐。
var idnaProfile = idna.Lookup

// parseDomainsLabel 解析 fleetly.domains label 值：逗号分隔列表 → trim/
// 小写/IDN→punycode 归一化、排序去重；通配符与非法形态 →
// E_DOMAIN_UNSUPPORTED；超上限 → E_DOMAIN_UNSUPPORTED（reason 上下文标注）。
// 返回归一化后的域名列表（保序输入、输出排序去重）。
func parseDomainsLabel(service, value string) ([]string, error) {
	rawItems := strings.Split(value, ",")
	domains := make([]string, 0, len(rawItems))
	seen := map[string]bool{}
	for i, raw := range rawItems {
		d := strings.TrimSpace(raw)
		pathCtx := fmt.Sprintf("services.%s.labels.%s[%d]", service, LabelDomains, i)
		if d == "" {
			return nil, apperr.New("E_DOMAIN_UNSUPPORTED", "domain list of service %q contains an empty entry", service).
				WithContext("path", pathCtx).WithContext("reason", "empty_entry")
		}
		if strings.HasPrefix(d, "*") {
			return nil, apperr.New("E_DOMAIN_UNSUPPORTED", "domain %q of service %q is a wildcard (wildcard certificates require DNS-01, supported from v0.2)", service, d).
				WithContext("path", pathCtx).WithContext("reason", "wildcard")
		}
		ascii, err := idnaProfile.ToASCII(strings.ToLower(d))
		if err != nil {
			return nil, apperr.New("E_DOMAIN_UNSUPPORTED", "domain %q of service %q has an unsupported form: %v", service, d, err).
				WithContext("path", pathCtx).WithContext("reason", "invalid_form")
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

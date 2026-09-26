package engine

// 入口路由发布（T2.15；架构 §2.5 不变量 + §2.6 入口行）：发布端口 +
// 「首健康→切流之后」的挂点逻辑。路由发布严格晚于健康门——端点入集晚于
// healthy（V1/B3 语义）；发布失败的语义是**部署不受影响**：deployment 照
// 常推进，route.publish_failed 单独告警事件 + 审计（错误码
// E_ROUTE_PUBLISH_FAILED），重试路径 = 幂等发布（观察窗终态再发布一次）
// 与下次部署。
//
// 路由声明的来源（IMPL-T1-1 起）：
//  1. 真值 = state 域名行（internal/ingress 在发布点现读；API CRUD 与首
//     部署种子是仅有的写入方）；
//  2. 本包只从 compose 重载提取 label 声明（spec_hash 复核——与期望态
//     同源）作为种子候选：state 无行时首部署播种；state 有行时一律忽略
//     并派事件（label 仅 bootstrap）。重载失败/hash 漂移 → 不提供种子
//     （没有可靠声明源时不猜——不因声明源缺失误删/误建路由）。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// RouteServiceSpec 是单个入口服务的 compose 声明（引擎 → 发布端口的种子
// 候选载荷）。IMPL-T1-1 起域名声明真值 = state 域名行（ingress 发布点
// 现读）；本载荷只承载 label 种子（state 无行时的首部署播种），Port 是
// compose expose 首端口。
type RouteServiceSpec struct {
	Service string
	// Port 是后端端口（compose expose 首端口）。
	Port string
	// Domains 是归一化域名集。
	Domains []string
}

// RoutePublishInput 是一次路由发布输入。TeamSlug/PrjSlug 是归属两个 slug
// （v0.3：ingress 的 per-app 网络接入与三段路由键公式参数，rbac-teams
// §4.3）。
type RoutePublishInput struct {
	AppID    string
	AppName  string
	TeamSlug string
	PrjSlug  string
	// Declared 是 compose label 域名声明（bootstrap 种子候选：state 无行
	// 时播种；state 有行时一律忽略并派事件——单一写点仲裁在 ingress 侧）。
	Declared []RouteServiceSpec
}

// RoutePublisher 是入口路由发布端口（internal/ingress.Manager 隐式实现；
// 接口在本包定义以便单测注入假发布器）。nil = 未接入口面（无域名部署
// 或单测环境，发布逻辑整体跳过）。
type RoutePublisher interface {
	PublishRoutes(ctx context.Context, in RoutePublishInput) error
}

// WithRoutePublisher 注入发布器（链式；nil 显式表示不发布）。
func (e *Engine) WithRoutePublisher(p RoutePublisher) *Engine { e.routes = p; return e }

// routePublishBudget 是单次路由发布的 tick 预算（H11 评审项：网络面慢不
// 再冻结全平台）：发布器内含 ingress 的证书签发（ACME 网络往返，CA 慢时
// 以分钟计），同步跑在引擎 tick goroutine 上——无预算时一次慢签发会拖停
// 全部部署推进并触发他人看门狗误判。预算内未完成按既有 route.publish_failed
// 语义告警（E_ROUTE_PUBLISH_FAILED 事件路径不变，部署不受影响）；恢复上界
// 由 ingress sweep 承担（renewDue 续期扫描周期 12h + 下次部署的幂等重发布，
// 两者都走同一 PublishRoutes 收敛路径）。
//
// v0.2 注记：完整异步化（发布挪出 tick goroutine，独立重试队列）留待
// v0.2；本预算是对协作型发布器（尊重 ctx 取消）的硬上界。
const routePublishBudget = 30 * time.Second

// publishRoutes 是发布挂点（enterObserving 首健康后与 succeedDeployment
// 终态各调用一次——首次满足「晚于健康门」，二次承担重试与移除同步）。
// 发布失败不改变部署状态机的任何字段。
func (e *Engine) publishRoutes(ctx context.Context, rec state.DeployRecord) {
	if e.routes == nil {
		return
	}
	in := e.routePublishInput(ctx, rec)
	// H11：发布调用包 tick 预算（routePublishBudget；单测经 routeBudget
	// 字段注入短预算）——预算耗尽返回的 ctx.Err 以发布失败语义处置。
	budget := e.routeBudget
	if budget <= 0 {
		budget = routePublishBudget
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	err := e.routes.PublishRoutes(pctx, in)
	if err != nil {
		e.log.Warn("engine: route publish failed (deployment unaffected; "+
			"route.publish_failed alerted)", "deployment", rec.ID, "app", rec.AppName, "error", err)
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			if err := appendEvents(ctx, tx, appEvent("route.publish_failed", rec.AppName,
				"deployment", rec.ID, "error", errMessageForEvent(err))); err != nil {
				return err
			}
			return auditDeployment(ctx, tx, "system", "route.publish", rec.ID,
				"error", "E_ROUTE_PUBLISH_FAILED",
				state.DiffSummary("app", rec.AppName, "error", errMessageForEvent(err))) // MG-6：构造器替换手拼 JSON
		}); err != nil {
			e.log.Warn("engine: record route publish failure", "deployment", rec.ID, "error", err)
		}
		return
	}
	if err := e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := appendEvents(ctx, tx, appEvent("route.published", rec.AppName,
			"deployment", rec.ID)); err != nil {
			return err
		}
		return auditDeployment(ctx, tx, "system", "route.publish", rec.ID,
			"ok", "", state.DiffSummary("app", rec.AppName, "declared_services", len(in.Declared))) // MG-6：构造器替换手拼 JSON（计数保持原生数值；真值 = state 行，本计数只记本次种子候选）
	}); err != nil {
		e.log.Warn("engine: record route published", "deployment", rec.ID, "error", err)
	}
}

// routePublishInput 构建发布输入：compose label 声明（种子候选——spec_hash
// 复核与期望态同源；不可读/hash 漂移则不提供种子，ingress 以 state 行为
// 准）+ 归属 slug 一次读取（v0.3：路由键与网络接入的三段公式参数）。
// 域名声明真值 = state 域名行（ingress 发布点现读——IMPL-T1-1 起台账不再
// 经本包直推）。
func (e *Engine) routePublishInput(ctx context.Context, rec state.DeployRecord) RoutePublishInput {
	out := RoutePublishInput{AppID: rec.AppID, AppName: rec.AppName}
	if spec, _, err := compose.Load(ctx, rec.ComposePath); err == nil && spec.SpecHash == rec.SpecHash {
		if declared, ok := declaredServicesFromSpec(spec); ok {
			out.Declared = declared
		}
	}
	if app, err := e.store.GetAppByID(ctx, rec.AppID); err == nil {
		out.TeamSlug = app.TeamSlug
		out.PrjSlug = app.ProjectSlug
	}
	return out
}

// declaredServicesFromSpec 从归一化 compose 提取 label 域名声明（domains
// 服务 → service/port/domains；expose 首端口为种子端口，架构 §2.4）。
// 声明集不可用（domains 服务无 expose 的纵深防御形态）→ false：不拿
// 「半截」声明播种（防误删/误建），调用方按无种子语义处置。
func declaredServicesFromSpec(spec *compose.Spec) ([]RouteServiceSpec, bool) {
	services := make([]RouteServiceSpec, 0, len(spec.Services))
	for i := range spec.Services {
		svc := &spec.Services[i]
		if len(svc.Domains) == 0 {
			continue
		}
		port := firstExposePort(svc.Expose)
		if port == "" {
			return nil, false
		}
		services = append(services, RouteServiceSpec{
			Service: svc.Name,
			Port:    port,
			Domains: append([]string{}, svc.Domains...),
		})
	}
	return services, true
}

// firstExposePort 取 expose 首端口（"8080/tcp" → "8080"；无 → ""）。
func firstExposePort(expose []string) string {
	if len(expose) == 0 {
		return ""
	}
	raw := expose[0]
	if idx := strings.Index(raw, "/"); idx >= 0 {
		return raw[:idx]
	}
	return raw
}

// errMessageForEvent 是事件 payload 的错误单行化（kv 载荷只收字符串；
// 禁换行——事件 JSON 脱敏契约）。
func errMessageForEvent(err error) string {
	msg := fmt.Sprint(err)
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

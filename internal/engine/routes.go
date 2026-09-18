package engine

// 入口路由发布（T2.15；架构 §2.5 不变量 + §2.6 入口行）：发布端口 +
// 「首健康→切流之后」的挂点逻辑。路由发布严格晚于健康门——端点入集晚于
// healthy（V1/B3 语义）；发布失败的语义是**部署不受影响**：deployment 照
// 常推进，route.publish_failed 单独告警事件 + 审计（错误码
// E_ROUTE_PUBLISH_FAILED），重试路径 = 幂等发布（观察窗终态再发布一次）
// 与下次部署。
//
// 路由声明的提取来源（优先级）：
//  1. compose 重载（spec_hash 复核——与期望态同源；服务移除/域名撤销随
//     本次声明集对账删除，「省略 = 删除」的路由面）；
//  2. 重载失败/hash 漂移（文件丢失、回滚目标与现盘文件不一致）→ 台账
//     直推（幂等不回退：没有可靠声明源时不猜——不因声明源缺失误删路由）。

import (
	"context"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// RouteServiceSpec 是单个入口服务的路由声明（引擎 → 发布端口的载荷）。
type RouteServiceSpec struct {
	Service string
	// Port 是后端端口（compose expose 首端口）。
	Port string
	// Domains 是归一化域名集。
	Domains []string
}

// RoutePublishInput 是一次路由发布输入。
type RoutePublishInput struct {
	AppID    string
	AppName  string
	Services []RouteServiceSpec
}

// RoutePublisher 是入口路由发布端口（internal/ingress.Manager 隐式实现；
// 接口在本包定义以便单测注入假发布器）。nil = 未接入口面（无域名部署
// 或单测环境，发布逻辑整体跳过）。
type RoutePublisher interface {
	PublishRoutes(ctx context.Context, in RoutePublishInput) error
}

// WithRoutePublisher 注入发布器（链式；nil 显式表示不发布）。
func (e *Engine) WithRoutePublisher(p RoutePublisher) *Engine { e.routes = p; return e }

// publishRoutes 是发布挂点（enterObserving 首健康后与 succeedDeployment
// 终态各调用一次——首次满足「晚于健康门」，二次承担重试与移除同步）。
// 发布失败不改变部署状态机的任何字段。
func (e *Engine) publishRoutes(ctx context.Context, rec state.DeployRecord) {
	if e.routes == nil {
		return
	}
	in := e.routePublishInput(ctx, rec)
	if err := e.routes.PublishRoutes(ctx, in); err != nil {
		e.log.Warn("engine: route publish failed (deployment unaffected; "+
			"route.publish_failed alerted)", "deployment", rec.ID, "app", rec.AppName, "error", err)
		if err := e.store.InTx(ctx, func(tx *state.Tx) error {
			if err := appEvent(ctx, tx, "route.publish_failed", rec.AppName,
				"deployment", rec.ID, "error", errMessageForEvent(err)); err != nil {
				return err
			}
			return auditDeployment(ctx, tx, "system", "route.publish", rec.ID,
				"error", "E_ROUTE_PUBLISH_FAILED",
				`{"app":"`+rec.AppName+`","error":"`+errMessageForEvent(err)+`"}`)
		}); err != nil {
			e.log.Warn("engine: record route publish failure", "deployment", rec.ID, "error", err)
		}
		return
	}
	if err := e.store.InTx(ctx, func(tx *state.Tx) error {
		if err := appEvent(ctx, tx, "route.published", rec.AppName,
			"deployment", rec.ID); err != nil {
			return err
		}
		return auditDeployment(ctx, tx, "system", "route.publish", rec.ID,
			"ok", "", `{"app":"`+rec.AppName+`","services":"`+fmt.Sprint(len(in.Services))+`"}`)
	}); err != nil {
		e.log.Warn("engine: record route published", "deployment", rec.ID, "error", err)
	}
}

// routePublishInput 构建发布输入（compose 同源优先，台账兜底）。
func (e *Engine) routePublishInput(ctx context.Context, rec state.DeployRecord) RoutePublishInput {
	if spec, _, err := compose.Load(ctx, rec.ComposePath); err == nil && spec.SpecHash == rec.SpecHash {
		if in, ok := routeInputFromSpec(rec, spec); ok {
			return in
		}
		// 声明集不可用（domains 服务无 expose 的纵深防御形态）：落台账
		// 兜底——不能拿「半截」声明对账（防误删路由）。
	}
	// 台账直推（声明源缺失：文件丢失/回滚文件漂移——保持现状不误删）。
	rows, err := e.store.ListAppDomains(ctx, rec.AppID)
	if err != nil {
		return RoutePublishInput{AppID: rec.AppID, AppName: rec.AppName}
	}
	byService := map[string]*RouteServiceSpec{}
	order := []string{}
	for _, row := range rows {
		svc, ok := byService[row.Service]
		if !ok {
			svc = &RouteServiceSpec{Service: row.Service, Port: row.Port}
			byService[row.Service] = svc
			order = append(order, row.Service)
		}
		svc.Domains = append(svc.Domains, row.Domain)
	}
	services := make([]RouteServiceSpec, 0, len(order))
	for _, name := range order {
		services = append(services, *byService[name])
	}
	return RoutePublishInput{AppID: rec.AppID, AppName: rec.AppName, Services: services}
}

// routeInputFromSpec 从归一化 compose 提取路由声明（domains 服务 →
// service/port/domains；expose 首端口为路由目标，architecture §2.4）。
func routeInputFromSpec(rec state.DeployRecord, spec *compose.Spec) (RoutePublishInput, bool) {
	services := make([]RouteServiceSpec, 0, len(spec.Services))
	for i := range spec.Services {
		svc := &spec.Services[i]
		if len(svc.Domains) == 0 {
			continue
		}
		port := firstExposePort(svc.Expose)
		if port == "" {
			return RoutePublishInput{}, false
		}
		services = append(services, RouteServiceSpec{
			Service: svc.Name,
			Port:    port,
			Domains: append([]string{}, svc.Domains...),
		})
	}
	return RoutePublishInput{
		AppID:    rec.AppID,
		AppName:  rec.AppName,
		Services: services,
	}, true
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

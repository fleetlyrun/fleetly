// Package eventsgrpc 实现 fleetly.telemetry.v1.EventsService：outbox 事件读面
// （List/Stream/Status/票据）与 SSE 原生入口的事件源。Team 行级过滤、断档
// 判定与订阅核心在此单源；HTTP 帧化在 assembly。
package eventsgrpc

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/grpc"

	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Tickets 是事件流票据的窄消费接口（fleetlygrpc.Services 实现）。
type Tickets interface {
	IssueEvents(scope string) (string, time.Duration, error)
	RedeemEvents(ticket string) (string, bool)
}

// Deps 是事件服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	Outbox  outbox.Repo
	Tickets Tickets
}

// Service 满足 telemetryv1.EventsServiceServer。
type Service struct {
	telemetryv1.UnimplementedEventsServiceServer
	deps Deps
}

// New 构造事件服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册事件服务。
func Register(srv *grpc.Server, deps Deps) {
	telemetryv1.RegisterEventsServiceServer(srv, New(deps))
}

var _ telemetryv1.EventsServiceServer = (*Service)(nil)

// ---- Events（F0.23 读路径；流式 follow N1） ----

// eventsScopeAll 是 SSE 票据 payload 的平台 owner 可见域标记（全量；
// Team ID 不取 "*" 形，无歧义）。
const eventsScopeAll = "*"

// eventsTeamFilterOf 解析调用方在事件面的 Team 过滤锚（P0-2，S2 止血）：
// 平台 owner（default Team 的 builtin-owner）→ ""（全量不过滤）；其余 →
// 本 Team（outbox 行级过滤 team_id = ? OR team_id = ”，” 行是平台级
// 事件、全员可见；归属解析失败的哨兵行不匹配任何 Team，Team 视角不可见、
// 平台 owner 仍可取证）。无 Team 的 Token fail-closed。
func TeamFilterOf(ctx context.Context) (string, error) {
	teamID, owner, err := grpcapi.CallerTeam(ctx)
	if err != nil {
		return "", err
	}
	if owner {
		return "", nil
	}
	if teamID == "" {
		return "", apperr.New("E_FORBIDDEN",
			"the caller's token carries no team; the events surface requires a team-scoped token")
	}
	return teamID, nil
}

// eventsTicketScope 把可见域铸为票据 payload（P0-2：SSE 票据与身份解绑
// 是已知缺口——兑换面无 authn ctx，Team 归属以铸造时烙印为准）。
func eventsTicketScope(team string) string {
	if team == "" {
		return eventsScopeAll // owner 全量
	}
	return team
}

// eventsTeamFromScope 反解票据 payload 的可见域（"*" → "" 全量）。
func eventsTeamFromScope(scope string) string {
	if scope == eventsScopeAll {
		return ""
	}
	return scope
}

func (svc *Service) ListEvents(ctx context.Context, req *telemetryv1.ListEventsRequest) (*telemetryv1.ListEventsResponse, error) {
	if err := svc.eventsGoneCheck(ctx, req.GetAfterSeq()); err != nil {
		return nil, err
	}
	team, err := TeamFilterOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := req.GetLimit()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	events, err := svc.deps.Outbox.ListAfter(ctx, req.GetAfterSeq(), int(limit), team)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "events")
	}
	lastSeq, err := svc.deps.Outbox.LastSeq(ctx)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "events")
	}
	out := &telemetryv1.ListEventsResponse{LastSeq: lastSeq}
	for _, ev := range events {
		out.Events = append(out.Events, eventMsg(&ev))
	}
	return out, nil
}

// streamEventsPollInterval 是 follow 模式的轮询拍（outbox 单调 seq 是权威
// 源；拍取舍：Console/Agent 人机尺度下 250ms 足够即时，DB 压力可忽略）。
//
// 性能阈值锚(P5-5)：follow 流面分析容量 ≈50 并发跟随流（分析容量锚，评
// 审容量结论非实测上限）。形态：拍轮询——每流每 250ms 拍一发 outbox 点
// 查；余量按单写者 SQLite 形态的拍间隔均摊估计，未经压测证实。重估触发：
// 常驻跟随流数常态逼近 50 量级。
const streamEventsPollInterval = 250 * time.Millisecond

// eventsGoneCheck 断档判定（ADR-0026）：after_seq 落在保留窗外 → 410 带
// earliest_seq 上下文（客户端重同步：重读所跟踪资源的列表 + 以
// GetEventStatus.last_seq 为新游标）。0 = 从保留窗最早开始，非断档。
// 窗被清空（earliest=0 且 last=0）时任何正游标都判档：游标指向的既成
// 事实已不可达，诚实答案就是重同步（fresh install 同形——resync 同样
// 正确）。seq 是 AUTOINCREMENT，清空后不复用，游标错位不会被掩盖。
func (svc *Service) eventsGoneCheck(ctx context.Context, afterSeq int64) error {
	if afterSeq <= 0 {
		return nil
	}
	earliest, err := svc.deps.Outbox.EarliestSeq(ctx)
	if err != nil {
		return grpcapi.MapStateError(err, "events")
	}
	last, err := svc.deps.Outbox.LastSeq(ctx)
	if err != nil {
		return grpcapi.MapStateError(err, "events")
	}
	gone := (earliest > 0 && afterSeq < earliest) || (earliest == 0 && last == 0)
	if gone {
		return apperr.New("E_EVENTS_GONE",
			"event cursor %d is older than the earliest retained event (the retention window trimmed it)", afterSeq).
			WithContext("earliest_seq", strconv.FormatInt(earliest, 10)).
			WithSuggestion("Resynchronize: re-read the current state of the resources you track, then continue from GetEventStatus.last_seq.")
	}
	return nil
}

// StreamEvents 是订阅面（gRPC server-streaming）：重放保留窗（after_seq
// 起升序），follow 时持续跟随直至客户端取消（流面不经 unary 超时拦截器，
// 生命周期由取消信号管理——架构 §7 等待原语的数据源）。Team 行级过滤
// 与 ListEvents 同锚（P0-2）。
func (svc *Service) StreamEvents(req *telemetryv1.StreamEventsRequest, stream telemetryv1.EventsService_StreamEventsServer) error {
	team, err := TeamFilterOf(stream.Context())
	if err != nil {
		return err
	}
	return svc.Subscribe(stream.Context(), team, req.GetAfterSeq(), req.GetFollow(), func(ev *telemetryv1.Event) error {
		return stream.Send(&telemetryv1.StreamEventsResponse{Event: ev})
	})
}

// GetEventStatus 是断档判定与快照重同步的基准（earliest_seq / last_seq）。
func (svc *Service) GetEventStatus(ctx context.Context, _ *telemetryv1.GetEventStatusRequest) (*telemetryv1.GetEventStatusResponse, error) {
	earliest, err := svc.deps.Outbox.EarliestSeq(ctx)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "events")
	}
	last, err := svc.deps.Outbox.LastSeq(ctx)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "events")
	}
	return &telemetryv1.GetEventStatusResponse{EarliestSeq: earliest, LastSeq: last}, nil
}

// IssueEventTicket 为 SSE 订阅路径换一次性短时票据（ADR-0026：浏览器
// EventSource 不能设自定义头）。payload 绑调用方 Team 可见域（P0-2）：
// 兑换面无 authn ctx，过滤锚以铸造时烙印为准。
func (svc *Service) IssueEventTicket(ctx context.Context, _ *telemetryv1.IssueEventTicketRequest) (*telemetryv1.IssueEventTicketResponse, error) {
	team, err := TeamFilterOf(ctx)
	if err != nil {
		return nil, err
	}
	ticket, ttl, err := svc.deps.Tickets.IssueEvents(eventsTicketScope(team))
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "ticket generation failed").WithCause(err)
	}
	return &telemetryv1.IssueEventTicketResponse{Ticket: ticket, ExpiresIn: int32(ttl.Seconds())}, nil //nolint:gosec // TTL 秒级
}

// subscribeEvents 是订阅核心（gRPC 流与 SSE 原生入口同源同口径）：断档
// 判定 → 批次重放 → follow 轮询（ctx 取消收口）。team 是行级过滤锚
// （P0-2）：” = 平台 owner 全量；否则 team_id = ? OR team_id = ”（哨兵
// 归属行不匹配任何 Team，Team 视角不可见、平台 owner 仍可取证）。
// send 返回错误即收流。
func (svc *Service) Subscribe(ctx context.Context, team string, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error {
	if err := svc.eventsGoneCheck(ctx, afterSeq); err != nil {
		return err
	}
	cursor := afterSeq
	for {
		batch, err := svc.deps.Outbox.ListAfter(ctx, cursor, 1000, team)
		if err != nil {
			return grpcapi.MapStateError(err, "events")
		}
		for i := range batch {
			if err := send(eventMsg(&batch[i])); err != nil {
				return err
			}
			cursor = batch[i].Seq
		}
		if !follow {
			return nil
		}
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(streamEventsPollInterval):
			}
		}
	}
}

// eventMsg 是 outbox 行 → 传输形态（单点，List 与订阅面共用）。
func eventMsg(ev *outbox.Event) *telemetryv1.Event {
	return &telemetryv1.Event{
		Seq: ev.Seq, Name: ev.Name, Aggregate: ev.Aggregate, AggregateId: ev.AggregateID,
		Payload: string(ev.Payload), CreatedAt: ev.CreatedAt,
	}
}

// EventStreamSource 是 SSE 原生入口（assembly 挂载，ADR-0026）的事件源：
// 票据兑换（单用途）+ 订阅投递——与 gRPC StreamEvents 同源同口径
// （Subscribe 单一实现，两面不得漂移）。HTTP 帧化在 assembly。
type EventStreamSource struct {
	svc     *Service
	tickets Tickets
}

// NewEventStreamSource 构造事件源。
func NewEventStreamSource(outbox outbox.Repo, tickets Tickets) *EventStreamSource {
	return &EventStreamSource{svc: New(Deps{Outbox: outbox, Tickets: tickets}), tickets: tickets}
}

// RedeemTicket 兑换一次性票据（单用途：命中即删；过期/未知一律拒绝），
// 回带铸造时烙印的 Team 可见域（P0-2：SSE 入口无 authn ctx，票据与身份
// 解绑的收口锚——归属以铸造时烙印为准）。
func (src *EventStreamSource) RedeemTicket(ticket string) (string, bool) {
	payload, ok := src.tickets.RedeemEvents(ticket)
	if !ok {
		return "", false
	}
	return eventsTeamFromScope(payload), true
}

// Gone 断档预检（SSE 入口必须在写出 200/SSE 头之前判定——状态行一旦
// 发出，错误只能以 SSE error 帧收口，410 语义就丢了）。
func (src *EventStreamSource) Gone(ctx context.Context, afterSeq int64) error {
	return src.svc.eventsGoneCheck(ctx, afterSeq)
}

// Subscribe 订阅事件（与 gRPC StreamEvents 同一核心）。team 是行级过滤
// 锚（P0-2）：空串 = 平台 owner 全量；否则本队 + 平台级。
func (src *EventStreamSource) Subscribe(ctx context.Context, team string, afterSeq int64, follow bool, send func(*telemetryv1.Event) error) error {
	return src.svc.Subscribe(ctx, team, afterSeq, follow, send)
}

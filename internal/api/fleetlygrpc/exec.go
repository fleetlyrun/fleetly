package fleetlygrpc

// ExecService 实现 runtime/v1 ExecService（F3.2，ADR-0049）：受理面
// （CreateExecSession 四件一拍——限额前置检查在 engine、审计带命令详情、
// exec.session_opened 事件、票据铸造）与 gRPC 会话流（CLI 消费径；Console
// WS 消费径在 assembly 原生入口，同一 engine 会话泵）。change freeze
// 豁免（ADR-0049 决策 4：exec 不变更资源状态——诊断面在冻结窗最需要）。

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
)

// ExecService 是 exec 会话的受理与流面。
type ExecService struct {
	runtimev1.UnimplementedExecServiceServer
	s *Services
}

// execEventPayload 是 exec.session_opened 的载荷（安全可见性：谁在何时
// 进入了哪个进程——actor/进程/实例/节点/命令；命令是调用方请求的 argv，
// 载体内后续操作不在面内）。
type execEventPayload struct {
	SessionID string   `json:"session_id"`
	Actor     string   `json:"actor"`
	Process   string   `json:"process"`
	Instance  string   `json:"instance"`
	NodeID    string   `json:"node_id"`
	Command   []string `json:"command"`
	TTY       bool     `json:"tty"`
}

// execDetailJSON 是审计 Detail（与事件载荷同源字段；process/app 补齐——
// 审计的 Resource 是 app 锚，进程与命令在 Detail）。
func execDetailJSON(p execEventPayload) string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// mapExecError 把 engine 哨兵映射为稳定信封。
func mapExecError(err error) error {
	switch {
	case errors.Is(err, engine.ErrExecUnsupported):
		return apperr.New("E_EXEC_UNSUPPORTED", "the runtime provider does not implement exec sessions")
	case errors.Is(err, engine.ErrExecNoInstance):
		return apperr.New("E_NOT_FOUND", "no running instance for the requested process: %v", err)
	case errors.Is(err, engine.ErrExecNodeUnanchored):
		return apperr.New("E_NODE_RELAY_OFFLINE", "the target node is not anchored yet (recently joined); retry shortly")
	case errors.Is(err, engine.ErrExecRelayOffline):
		return apperr.New("E_NODE_RELAY_OFFLINE", "the target node has no connected relay; run the relay command from `fleetly nodes enroll` on the node and retry")
	case errors.Is(err, engine.ErrExecTeamLimit):
		return apperr.New("E_QUOTA_EXCEEDED", "too many concurrent exec sessions for this team; close existing sessions or retry later")
	default:
		return mapStateError(err, "exec")
	}
}

// CreateExecSession 受理：输入校验 → 归属校验 → 子面检查 → engine 受理
// （解析/限额/注册）→ 四件一拍（审计 + 事件；失败即回滚会话活体）→
// 铸造票据（绑会话）。
func (svc *ExecService) CreateExecSession(ctx context.Context, req *runtimev1.CreateExecSessionRequest) (*runtimev1.CreateExecSessionResponse, error) {
	if req.GetAppId() == "" || req.GetProcess() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id and process must not be empty")
	}
	argv := req.GetCommand()
	if len(argv) == 0 {
		if !req.GetTty() {
			return nil, apperr.New("E_INVALID_ARGUMENT", "command must not be empty for non-tty sessions (tty sessions default to /bin/sh)")
		}
		argv = []string{"/bin/sh"}
	}
	if svc.s.Engine.ExecUnsupported() {
		return nil, apperr.New("E_EXEC_UNSUPPORTED", "the runtime provider does not implement exec sessions")
	}
	a, err := svc.s.authorizeAppID(ctx, req.GetAppId())
	if err != nil {
		return nil, err
	}
	team, err := svc.s.Anchor.TeamOfProjectID(ctx, svc.s.DB.Runner(), a.ProjectID)
	if err != nil {
		return nil, mapAnchorError(err)
	}

	info, err := svc.s.Engine.CreateSession(ctx, engine.CreateExecInput{
		TeamID: team, AppID: a.ID, Process: req.GetProcess(), Argv: argv, TTY: req.GetTty(),
	})
	if err != nil {
		return nil, mapExecError(err)
	}

	payload := execEventPayload{
		SessionID: info.ID,
		Actor:     authn.ActorFromContext(ctx),
		Process:   info.Process,
		Instance:  info.Instance,
		NodeID:    info.NodeID,
		Command:   info.Argv,
		TTY:       info.TTY,
	}
	if err := svc.s.commit(ctx, writeFact{
		events: []eventFact{{name: "exec.session_opened", aggregate: "app", id: info.AppID, payload: mustJSON(payload)}},
		audits: []*audit.Entry{{
			ID: newID(), Actor: payload.Actor, Source: authn.SourceFromContext(ctx),
			Action: "exec.session", Resource: "app/" + info.AppID,
			Detail: execDetailJSON(payload),
		}},
	}); err != nil {
		svc.s.Engine.AbandonSession(info.ID)
		return nil, err
	}

	ticket, ttl, err := svc.s.eventTickets.issue(ticketPurposeExec, info.ID)
	if err != nil {
		svc.s.Engine.AbandonSession(info.ID)
		return nil, mapStateError(err, "exec ticket")
	}
	return &runtimev1.CreateExecSessionResponse{
		Session:   execSessionMsg(info),
		Ticket:    ticket,
		ExpiresIn: int32(ttl.Seconds()),
	}, nil
}

// StreamExecSession 是 gRPC 会话流（CLI 消费径）：首帧 attach 绑会话，
// 随后与 engine 会话泵对接直至收口。退出码语义：error 帧 → 稳定退出码 1；
// 正常 exit 帧由 CLI 层透传（stream 本身以 OK 终止）。
func (svc *ExecService) StreamExecSession(stream runtimev1.ExecService_StreamExecSessionServer) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetAttachSessionId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "first frame must attach a session id")
	}
	if _, ok := svc.s.Engine.ExecSessionByID(first.GetAttachSessionId()); !ok {
		return apperr.New("E_NOT_FOUND", "exec session %s not found", first.GetAttachSessionId())
	}
	pipe := &execGRPCPipe{stream: stream, attached: true}
	if err := svc.s.Engine.AttachClient(ctx, first.GetAttachSessionId(), pipe); err != nil {
		return mapExecError(err)
	}
	return nil
}

// execGRPCPipe 把 gRPC 双向流适配为 engine 消费端管道。
type execGRPCPipe struct {
	stream   runtimev1.ExecService_StreamExecSessionServer
	attached bool
}

func (p *execGRPCPipe) Recv() (engine.ExecClientFrame, error) {
	msg, err := p.stream.Recv()
	if err != nil {
		return engine.ExecClientFrame{}, err
	}
	switch f := msg.GetFrame().(type) {
	case *runtimev1.StreamExecSessionRequest_AttachSessionId:
		if p.attached {
			return engine.ExecClientFrame{}, status.Error(codes.InvalidArgument, "session already attached")
		}
		p.attached = true
		return engine.ExecClientFrame{}, nil
	case *runtimev1.StreamExecSessionRequest_Stdin:
		return engine.ExecClientFrame{Stdin: f.Stdin}, nil
	case *runtimev1.StreamExecSessionRequest_Resize:
		return engine.ExecClientFrame{Resize: &capability.ExecSize{
			Cols: dimToU16(f.Resize.GetCols()), Rows: dimToU16(f.Resize.GetRows()),
		}}, nil
	default:
		return engine.ExecClientFrame{}, nil
	}
}

func (p *execGRPCPipe) Send(f engine.ExecServerFrame) error {
	msg := &runtimev1.StreamExecSessionResponse{}
	switch {
	case f.Meta != nil:
		msg.Frame = &runtimev1.StreamExecSessionResponse_Meta{Meta: &runtimev1.ExecSessionMeta{
			Instance: f.Meta.Instance, NodeId: f.Meta.NodeID,
		}}
	case f.Exit != nil:
		msg.Frame = &runtimev1.StreamExecSessionResponse_Exit{Exit: &runtimev1.ExecExit{Code: *f.Exit}}
	case f.Err != nil:
		msg.Frame = &runtimev1.StreamExecSessionResponse_Error{Error: &runtimev1.ExecStreamError{
			Code: f.Err.Code, Message: f.Err.Message,
		}}
	case f.Stderr != nil:
		msg.Frame = &runtimev1.StreamExecSessionResponse_Stderr{Stderr: f.Stderr}
	default:
		msg.Frame = &runtimev1.StreamExecSessionResponse_Stdout{Stdout: f.Stdout}
	}
	return p.stream.Send(msg)
}

func execSessionMsg(info *engine.ExecSessionInfo) *runtimev1.ExecSession {
	return &runtimev1.ExecSession{
		Id: info.ID, AppId: info.AppID, Process: info.Process,
		WorkloadId: info.WorkloadID, Instance: info.Instance, NodeId: info.NodeID,
		Command: info.Argv, Tty: info.TTY, CreatedAt: state.FormatTime(info.CreatedAt),
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ExecStreamSource 是 exec WS 流入口的消费面（assembly 原生入口用：
// 票据兑换 + engine 会话附着；EventStreamSource 同款暴露形态）。
type ExecStreamSource struct {
	s *Services
}

// NewExecStreamSource 构造。
func NewExecStreamSource(s *Services) *ExecStreamSource { return &ExecStreamSource{s: s} }

// RedeemExecTicket 兑换 exec 流票据（purpose+会话绑定、单用途）。
func (src *ExecStreamSource) RedeemExecTicket(sessionID, ticket string) bool {
	return src.s.eventTickets.redeem(ticketPurposeExec, sessionID, ticket)
}

// Engine 返回会话附着面（WS 适配器消费）。
func (src *ExecStreamSource) Engine() *engine.Engine { return src.s.Engine }

// dimToU16 钳制终端维度到 uint16 值域（G115 转换收口；负值/超界归 0）。
func dimToU16(v int32) uint16 {
	if v <= 0 {
		return 0
	}
	if v > 0xFFFF {
		return 0xFFFF
	}
	return uint16(v)
}

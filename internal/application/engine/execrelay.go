package engine

// Exec 中继 hub（F3.2，ADR-0049；assembly WS 适配器消费）：/v1/relay 服务
// 端握手与注册面（RelayAttach）、hub 上行投递（deliver）与下行下发
//（send/dropRelay）——路由半边；受理与生命周期见 exec.go。同节点重连顶
// 替旧连接：未附着会话重指新连接（受理面立即可用），已附着会话收口
//（exec 不可迁移，客户端重开——诚实边界）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// RelayAttach 是中继连接附着（H10 §4.5 校验序）：exec 面 → 载体节点锚定
// （ByCarrier）→ 内部 token（控制面进程自身）→ sha256 查节点个体凭证 →
// 凭证绑定轴与节点行双向匹配 → 注册写端。join token 不再被接受（鉴权
// 凭证不是集群成员权）。同节点重连顶替旧连接：未附着会话重指新连接（受
// 理面立即可用），已附着会话收口（exec 不可迁移，客户端重开——诚实
// 边界）。feed 供适配器上行投递；detach 在连接断开时调用。
func (e *Engine) RelayAttach(ctx context.Context, token, carrierNodeID, relayVersion string, w RelayWriter) (feed func(capability.RelayFrame), detach func(), err error) {
	if e.execFace == nil {
		return nil, nil, ErrExecUnsupported
	}
	n, err := e.repos.Nodes.ByCarrier(ctx, carrierNodeID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, nil, fmt.Errorf("%w: carrier %s", ErrExecNodeUnanchored, carrierNodeID)
		}
		return nil, nil, fmt.Errorf("%w: %w", ErrExecRelayStore, err)
	}
	if !e.exec.matchesInternal(token) {
		row, err := e.repos.Nodes.RelayCredentialBySHA256(ctx, node.HashRelayCredential(token))
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, nil, fmt.Errorf("%w: carrier %s", ErrExecRelayUnauthorized, carrierNodeID)
			}
			return nil, nil, fmt.Errorf("%w: %w", ErrExecRelayStore, err)
		}
		// 双向绑定：凭证行铸给哪个平台节点/载体，hello 就必须声明同一
		// 载体——伪造 carrier 或串号凭证在此失败（H10 的核心修复点）。
		if row.PlatformID != n.PlatformID || row.CarrierID != n.CarrierID {
			return nil, nil, fmt.Errorf("%w: credential bound to node %s", ErrExecRelayUnauthorized, row.PlatformID)
		}
	}

	a := &relayConn{nodeID: n.PlatformID, version: relayVersion, w: w}
	e.exec.mu.Lock()
	old := e.exec.relays[n.PlatformID]
	e.exec.relays[n.PlatformID] = a
	var attached []*execSession
	for _, s := range e.exec.sessions {
		if s.relay != old {
			continue
		}
		if s.attached {
			attached = append(attached, s) // 在途 exec 不可迁移：收口
		} else {
			s.relay = a // 未附着：重指新连接
		}
	}
	e.exec.mu.Unlock()
	if old != nil {
		e.log.Info("node relay replaced", "node", n.PlatformID, "version", relayVersion)
	} else {
		e.log.Info("node relay connected", "node", n.PlatformID, "version", relayVersion)
	}
	for _, s := range attached {
		e.exec.finish(s, relayGoneFrame(nil))
	}

	feed = func(f capability.RelayFrame) { e.exec.deliver(f) }
	detach = func() { e.exec.dropRelay(a) }
	return feed, detach, nil
}

// deliver 是 hub 上行投递（中继读循环逐帧调用）。
func (x *execs) deliver(f capability.RelayFrame) {
	x.mu.Lock()
	s := x.sessions[f.SessionID]
	x.mu.Unlock()
	if s == nil {
		return // 会话已收口（迟到帧丢弃）
	}
	var out ExecServerFrame
	switch f.Kind {
	case capability.RelayFrameAck:
		return // 受理回执（instance 已在 meta 首帧回显）
	case capability.RelayFrameStdout:
		out.Stdout = f.Payload
	case capability.RelayFrameStderr:
		out.Stderr = f.Payload
	case capability.RelayFrameExit:
		pl, err := capability.DecodeRelayJSON[capability.RelaySessionExit](f.Payload)
		if err != nil {
			return
		}
		// 退出码透传（int→int32：POSIX 退出码值域 0-255，钳制边界是
		// 纵深防御——代理侧是我们自己的 ExecWorkload 返回值）。
		code := int32(clampExitCode(pl.Code))
		out.Exit = &code
	case capability.RelayFrameError:
		pl, err := capability.DecodeRelayJSON[capability.RelaySessionError](f.Payload)
		if err != nil {
			return
		}
		out.Err = &ExecErrFrame{Code: pl.Code, Message: pl.Message}
	default:
		return
	}
	// 发送与 closed 复查同临界区（K2，2026-10-14 P0-1）：finish 置 closed
	// 后才 close(s.out)，发送方持锁复查即被排除——close 恒独占，
	// send-on-closed 竞态结构性消除。
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return // 会话已收口（迟到帧丢弃）
	}
	select {
	case s.out <- out:
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		// 慢消费端：缓冲满即收口（内存膨胀防护，ADR-0049 决策 4 注记）。
		x.finish(s, &ExecErrFrame{Code: "client_stalled", Message: "client is not draining session output"})
	}
}

// send 下发一帧到中继连接；失败即注销该连接并收口其名下会话。
func (x *execs) send(a *relayConn, kind capability.RelayFrameKind, sessionID string, payload []byte) error {
	if err := a.w.WriteRelayFrame(capability.MarshalRelayFrame(kind, sessionID, payload)); err != nil {
		x.dropRelay(a)
		return err
	}
	return nil
}

// dropRelay 注销中继连接（身份校验：已被顶替的连接不误删继任者）并收口
// 其名下会话（relay_disconnected 如实告知）。
func (x *execs) dropRelay(a *relayConn) {
	x.mu.Lock()
	current := x.relays[a.nodeID]
	if current == a {
		delete(x.relays, a.nodeID)
	}
	var affected []*execSession
	for _, s := range x.sessions {
		if s.relay == a {
			affected = append(affected, s)
		}
	}
	x.mu.Unlock()
	if current != a {
		return // 顶替路径已处理会话重指/收口
	}
	x.log.Info("node relay disconnected", "node", a.nodeID)
	for _, s := range affected {
		x.finish(s, relayGoneFrame(nil))
	}
}

// DropRelay 断开某平台节点的在连中继（H10 重铸/吊销的即时失效面）：
// 从 relays 摘除并收口其名下全部会话（relay_disconnected 如实告知——复用
// dropRelay 的身份校验与收口语义）。
func (e *Engine) DropRelay(platformID string) {
	e.exec.mu.Lock()
	a := e.exec.relays[platformID]
	e.exec.mu.Unlock()
	if a != nil {
		e.exec.dropRelay(a)
	}
}

// DropAllRelays 断开全部在连中继（rotate 全量作废面）：在 DB 事务提交后
// 调用（先让旧凭证失效，再断连接；即使断开失败，重连也会 401）。
func (e *Engine) DropAllRelays() {
	e.exec.mu.Lock()
	rels := make([]*relayConn, 0, len(e.exec.relays))
	for _, a := range e.exec.relays {
		rels = append(rels, a)
	}
	e.exec.mu.Unlock()
	for _, a := range rels {
		e.exec.dropRelay(a)
	}
}

// RelayStatus 是节点中继在连状态（节点视图回显面）。
type RelayStatus struct {
	Online  bool
	Version string
}

// RelayStatuses 返回全量在连快照（ListNodes 回显消费）。
func (e *Engine) RelayStatuses() map[string]RelayStatus {
	e.exec.mu.Lock()
	defer e.exec.mu.Unlock()
	out := make(map[string]RelayStatus, len(e.exec.relays))
	for id, a := range e.exec.relays {
		out[id] = RelayStatus{Online: true, Version: a.version}
	}
	return out
}

func relayGoneFrame(err error) *ExecErrFrame {
	msg := "node relay connection lost"
	if err != nil {
		msg += ": " + err.Error()
	}
	return &ExecErrFrame{Code: "relay_disconnected", Message: msg}
}

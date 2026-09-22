package execrelay

// relay 运行时单测（会话执行面）：label 卫兵 403（无 fleetly.app label 的
// 容器拒绝——D19 原文，relay 本地强制）、shell 白名单探测、stdin/stdout 桥
// 接、resize 透传、时限注入缝（空闲/硬上限缩短驱动——e2e 不等真实 10min
// 的时限验收面）、控制面 close 与注册帧序。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// startFakeRelay 以假拨号 + 假 docker 起 relay（返回 relay 侧连接——测试
// 持控制面角色读 written / 注入 inbound）。
func startFakeRelay(t *testing.T, docker execDocker, limits SessionLimits) (*Relay, *fakeConn) {
	t.Helper()
	relayConn := newFakeConn()
	r, err := NewRelay(RelayConfig{
		ControlAddr: "10.0.0.1:8420",
		Token:       "cluster-token",
		Hostname:    "abc123def456",
		DockerPort:  docker,
		Dial: func(_ context.Context, _ RelayConfig, token string) (MessageConn, error) {
			if token != "cluster-token" {
				return nil, errors.New("unexpected token")
			}
			return relayConn, nil
		},
		SessionLimits: limits,
	})
	if err != nil {
		t.Fatalf("NewRelay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = r.Run(ctx) }()
	return r, relayConn
}

func waitRegister(t *testing.T, conn *fakeConn) {
	t.Helper()
	conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeRegister {
				return true
			}
		}
		return false
	})
}

// TestRelayLabelGuard 403：无 fleetly.app label 的容器 → session.close
// （403）且不发生任何 exec attach（D19 安全面：relay 本地强制）。
func TestRelayLabelGuard(t *testing.T) {
	fd := &fakeDocker{appLabel: ""}
	stream := newFakeExecStream()
	fd.stream = stream
	_, conn := startFakeRelay(t, fd, SessionLimits{})
	waitRegister(t, conn)

	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "deadbeef", Shell: "/bin/sh"})
	conn.feed(open)
	frames := conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeSessionClose {
				return true
			}
		}
		return false
	})
	var closeF *SessionCloseFrame
	for _, f := range frames {
		if f.typ == TypeSessionClose {
			c := closeFrameOf(t, f)
			closeF = &c
		}
	}
	if closeF == nil || closeF.ID != "s1" || closeF.Code != CloseDenied {
		t.Fatalf("close frame = %+v, want 403 for s1", closeF)
	}
	fd.mu.Lock()
	attached := fd.attachCalls
	fd.mu.Unlock()
	// 卫兵拒绝发生在 attach 之前——零 attach（卫兵在 inspect 后短路）。
	if attached != 0 {
		t.Fatalf("attach calls = %d, want 0 for a denied container", attached)
	}
}

// TestRelayShellProbe 白名单探测：bash 不在 → 退 sh；显式白名单外 shell 拒。
func TestRelayShellProbe(t *testing.T) {
	fd := &fakeDocker{appLabel: "demo", probeCodes: map[string]int{"/bin/sh": 0}}
	stream := newFakeExecStream()
	fd.stream = stream
	_, conn := startFakeRelay(t, fd, SessionLimits{})
	waitRegister(t, conn)

	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "deadbeef"})
	conn.feed(open)
	// attach 成功即有输出通道——以 stdout 帧驱动确认在途（bash 缺席，sh 被选）。
	stream.readCh <- []byte("ok")
	conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeStdout {
				return true
			}
		}
		return false
	})
	fd.mu.Lock()
	gotShell := fd.attachedShell
	fd.mu.Unlock()
	if gotShell != "/bin/sh" {
		t.Fatalf("attached shell = %q, want /bin/sh (bash probe must fail first)", gotShell)
	}
	// 白名单外的显式 shell → 400。
	fd2 := &fakeDocker{appLabel: "demo", probeCodes: map[string]int{"/bin/sh": 0, "/bin/bash": 0}}
	fd2.stream = newFakeExecStream()
	_, conn2 := startFakeRelay(t, fd2, SessionLimits{})
	waitRegister(t, conn2)
	bad, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s2", ContainerID: "deadbeef", Shell: "/usr/bin/fish"})
	conn2.feed(bad)
	frames := conn2.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeSessionClose {
				return true
			}
		}
		return false
	})
	for _, f := range frames {
		if f.typ == TypeSessionClose {
			if c := closeFrameOf(t, f); c.ID != "s2" || c.Code != CloseBadShell {
				t.Fatalf("close = %+v, want 400 for s2", c)
			}
		}
	}
	fd2.mu.Lock()
	attached := fd2.attachCalls
	fd2.mu.Unlock()
	if attached != 0 {
		t.Fatalf("whitelist-violating shell must not attach (calls = %d)", attached)
	}
}

// TestRelayBridgeAndResize stdin/stdout 桥接与 resize 透传。
func TestRelayBridgeAndResize(t *testing.T) {
	fd := &fakeDocker{appLabel: "demo", probeCodes: map[string]int{"/bin/bash": 0, "/bin/sh": 0}}
	stream := newFakeExecStream()
	fd.stream = stream
	_, conn := startFakeRelay(t, fd, SessionLimits{})
	waitRegister(t, conn)

	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "deadbeef"})
	conn.feed(open)
	// 容器输出 → stdout 帧。
	stream.readCh <- []byte("term-ok-8f2c\r\n")
	conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeStdout {
				id, data, err := DecodeStreamPayload(f.payload)
				if err != nil || id != "s1" || string(data) != "term-ok-8f2c\r\n" {
					t.Fatalf("stdout payload mismatch: id=%q err=%v", id, err)
				}
				return true
			}
		}
		return false
	})
	// 控制面 stdin → exec 流。
	stdin, _ := EncodeFrame(TypeStdin, mustStream(t, "s1", "echo hi\n"))
	conn.feed(stdin)
	eventually(t, 2*time.Second, "stdin written to exec stream", func() bool {
		return writtenStdin(stream) == "echo hi\n"
	})
	// resize → ExecResize 透传。
	resize, _ := EncodeResizeFrame(ResizeFrame{ID: "s1", Cols: 120, Rows: 40})
	conn.feed(resize)
	eventually(t, 2*time.Second, "resize propagated", func() bool {
		fd.mu.Lock()
		defer fd.mu.Unlock()
		return len(fd.resized) == 1 && fd.resized[0] == [2]uint16{120, 40}
	})
}

// TestRelayIdleTimeout 时限注入缝：空闲 80ms 无数据帧 → close 408 + exec
// 流关闭（生产常量 10min 由 DefaultIdleTimeout 常量测试钉住——见
// TestSessionLimitDefaults）。
func TestRelayIdleTimeout(t *testing.T) {
	fd := &fakeDocker{appLabel: "demo", probeCodes: map[string]int{"/bin/bash": 0}}
	stream := newFakeExecStream()
	fd.stream = stream
	_, conn := startFakeRelay(t, fd, SessionLimits{Idle: 80 * time.Millisecond, Hard: 10 * time.Second})
	waitRegister(t, conn)
	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "deadbeef"})
	conn.feed(open)
	frames := conn.waitForWritten(t, 5*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeSessionClose {
				return true
			}
		}
		return false
	})
	var closeF *SessionCloseFrame
	for _, f := range frames {
		if f.typ == TypeSessionClose {
			c := closeFrameOf(t, f)
			closeF = &c
		}
	}
	if closeF == nil || closeF.Code != CloseTimeout || closeF.Reason != idleTimeoutReason {
		t.Fatalf("close = %+v, want 408 idle timeout", closeF)
	}
	eventually(t, time.Second, "exec stream closed", stream.isClosed)
}

// TestRelayHardTimeout 硬上限注入：Hard 100ms → close 429（即使持续有活动
// ——硬上限不受活动复位，双保险的 relay 半边语义）。
func TestRelayHardTimeout(t *testing.T) {
	fd := &fakeDocker{appLabel: "demo", probeCodes: map[string]int{"/bin/bash": 0}}
	stream := newFakeExecStream()
	fd.stream = stream
	_, conn := startFakeRelay(t, fd, SessionLimits{Idle: 10 * time.Second, Hard: 100 * time.Millisecond})
	waitRegister(t, conn)
	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "deadbeef"})
	conn.feed(open)
	// 活动不断注入（stdin 帧持续复位空闲计时）——硬上限仍触发。
	go func() {
		for i := 0; i < 6; i++ {
			f, _ := EncodeFrame(TypeStdin, mustStream(t, "s1", "x"))
			conn.feed(f)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	frames := conn.waitForWritten(t, 5*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeSessionClose {
				return true
			}
		}
		return false
	})
	var closeF *SessionCloseFrame
	for _, f := range frames {
		if f.typ == TypeSessionClose {
			c := closeFrameOf(t, f)
			closeF = &c
		}
	}
	if closeF == nil || closeF.Code != CloseHardLimit || closeF.Reason != hardTimeoutReason {
		t.Fatalf("close = %+v, want 429 hard limit", closeF)
	}
}

// TestSessionLimitDefaults 生产时限常量（设计 §2.4 原文 10min/30min）。
func TestSessionLimitDefaults(t *testing.T) {
	limits := SessionLimits{}.normalized()
	if limits.Idle != DefaultIdleTimeout || limits.Hard != DefaultHardTimeout {
		t.Fatalf("normalized limits = %v/%v", limits.Idle, limits.Hard)
	}
	if DefaultIdleTimeout != 10*time.Minute || DefaultHardTimeout != 30*time.Minute {
		t.Fatalf("default constants drifted: %v/%v", DefaultIdleTimeout, DefaultHardTimeout)
	}
}

// TestRelayMissingContainer 容器不存在 → 404 关闭。
func TestRelayMissingContainer(t *testing.T) {
	fd := &fakeDocker{missing: true}
	_, conn := startFakeRelay(t, fd, SessionLimits{})
	waitRegister(t, conn)
	open, _ := EncodeOpenFrame(SessionOpenFrame{ID: "s1", ContainerID: "nosuch"})
	conn.feed(open)
	frames := conn.waitForWritten(t, 2*time.Second, func(frames []frameTuple) bool {
		for _, f := range frames {
			if f.typ == TypeSessionClose {
				return true
			}
		}
		return false
	})
	for _, f := range frames {
		if f.typ == TypeSessionClose {
			if c := closeFrameOf(t, f); c.ID != "s1" || c.Code != CloseNoTarget {
				t.Fatalf("close = %+v, want 404 for s1", c)
			}
		}
	}
}

func mustStream(t *testing.T, id string, data string) []byte {
	t.Helper()
	p, err := EncodeStreamPayload(id, []byte(data))
	if err != nil {
		t.Fatalf("EncodeStreamPayload: %v", err)
	}
	return p
}

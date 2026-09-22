package execrelay

// WS 消息连接的最小抽象（conn.go）：relay 侧（出站拨号）与控制面侧
//（native 端点 Accept）共用同一形态——单二进制消息读写 + 关闭。
//
// coder/websocket 的并发契约：**同一时刻至多一个写入者**。中继连接上多
// 个会话 goroutine + ping 循环都会写帧，故真实连接统一包 lockedConn（写
// 互斥）；读侧每连接单读循环，天然串行。

import (
	"context"
	"fmt"
	"sync"

	"github.com/coder/websocket"
)

// MessageConn 是帧协议下层的二进制消息连接（双方各自实现/包装）。
type MessageConn interface {
	// Read 读一条完整消息（本协议只发 binary 帧——text 消息按错误处理）。
	Read(ctx context.Context) ([]byte, error)
	// Write 写一条完整消息（实现保证并发安全）。
	Write(ctx context.Context, payload []byte) error
	// Close 关闭连接（幂等；错误不重试——关闭面的失败只日志）。
	Close() error
}

// lockedConn 是 MessageConn 的写互斥包装（coder/websocket 单写者契约的
// 满足点；读侧由各使用方的单读循环保证）。
type lockedConn struct {
	inner *websocket.Conn
	mu    sync.Mutex
}

// newLockedConn 包装 coder/websocket 连接。
func newLockedConn(c *websocket.Conn) *lockedConn { return &lockedConn{inner: c} }

// Read 读一条完整消息（coder/websocket 整消息读形态）。
func (c *lockedConn) Read(ctx context.Context) ([]byte, error) {
	kind, data, err := c.inner.Read(ctx)
	if err != nil {
		return nil, err
	}
	if kind != websocket.MessageBinary {
		return nil, fmt.Errorf("execrelay: unexpected text message (%d bytes) on a binary frame channel", len(data))
	}
	return data, nil
}

// Write 写一条 binary 消息（写互斥——多会话帧与 ping 共享连接的唯一入口）。
func (c *lockedConn) Write(ctx context.Context, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inner.Write(ctx, websocket.MessageBinary, payload)
}

// Close 关闭底层连接（正常关闭码——帧层语义由 close 帧承载）。
func (c *lockedConn) Close() error {
	return c.inner.Close(websocket.StatusNormalClosure, "")
}

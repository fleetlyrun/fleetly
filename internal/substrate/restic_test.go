package substrate

import (
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/statebackup"
)

// restic 一次性容器执行器的单元测试（E3-3）：多路复用日志流拆解、子命令
// 提取、尾部截断，以及端口断言（Client 满足 statebackup.ResticRunner——
// 结构性断言的运行期镜像）。

// frame 编码一帧多路复用流（fd 1=stdout 2=stderr；8 字节头 + 载荷）。
func frame(fd byte, payload string) []byte {
	out := make([]byte, 8+len(payload))
	out[0] = fd
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload))) //nolint:gosec // G115：测试载荷远小于 2^32
	copy(out[8:], payload)
	return out
}

// TestDemuxContainerStream stdout/stderr 按帧分离、乱序交错可还原、EOF
// 收口；EOF 中途截断报错（诚实失败，不冒充完整输出）。
func TestDemuxContainerStream(t *testing.T) {
	stream := append(
		frame(2, "saving snapshot "),
		append(
			frame(1, `{"message_type":"status"}`+"\n"),
			frame(2, "done\n")...,
		)...,
	)
	stream = append(stream, frame(1, `{"message_type":"summary","snapshot_id":"abc"}`+"\n")...)
	stdout, stderr, err := demuxContainerStream(io.NopCloser(strings.NewReader(string(stream))))
	if err != nil {
		t.Fatalf("demuxContainerStream: %v", err)
	}
	if !strings.Contains(stdout, `"snapshot_id":"abc"`) || strings.Contains(stdout, "saving") {
		t.Fatalf("stdout = %q, want json lines only", stdout)
	}
	if !strings.Contains(stderr, "saving snapshot") || strings.Contains(stderr, "snapshot_id") {
		t.Fatalf("stderr = %q, want progress text only", stderr)
	}

	// 半帧截断：报错而非静默。
	if _, _, err := demuxContainerStream(io.NopCloser(strings.NewReader(string(stream[:len(stream)-3])))); err == nil {
		t.Fatal("truncated stream must error (never impersonate complete output)")
	}
}

// TestDemuxOversizeFrame 超限帧拒绝（防御面）。
func TestDemuxOversizeFrame(t *testing.T) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[4:8], resticMaxLogFrame+1)
	if _, _, err := demuxContainerStream(io.NopCloser(strings.NewReader(string(hdr)))); err == nil {
		t.Fatal("oversize frame must be rejected")
	}
}

// TestFirstTokenAndTailText 错误文本辅助：全局选项（-o 带值）跳过后取
// 子命令；tailText 按行边界截齐。
func TestFirstTokenAndTailText(t *testing.T) {
	if got := firstToken([]string{"-o", "s3.bucket-lookup=path", "backup", "/data"}); got != "backup" {
		t.Fatalf("firstToken = %q, want backup", got)
	}
	if got := firstToken([]string{"snapshots", "--json", "abc"}); got != "snapshots" {
		t.Fatalf("firstToken = %q, want snapshots", got)
	}
	if got := firstToken(nil); got != "restic" {
		t.Fatalf("firstToken(nil) = %q, want restic", got)
	}
	long := strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 100)
	got := tailText(long, 60)
	if len(got) != 60 || !strings.HasPrefix(strings.Repeat("b", 100), got) {
		t.Fatalf("tailText = %q (%d), want the last 60 bytes (no line boundary within the tail window)", got, len(got))
	}
	// 尾窗内有行边界时截齐（丢弃残行首段）。
	if got := tailText("aaa\nbbb", 5); got != "bbb" {
		t.Fatalf("tailText with boundary = %q, want bbb", got)
	}
	if tailText("short", 100) != "short" {
		t.Fatal("tailText below budget must be identity")
	}
}

// TestClientImplementsResticRunner 编译期断言（包内 var 已有）的显式镜像，
// 防断言行被误删后端口漂移（实现体为空——满足性由编译器证明）。
func TestClientImplementsResticRunner(t *testing.T) {
	var runner statebackup.ResticRunner = &Client{}
	_ = runner // 满足 ResticRunner 接口即通过（方法集由编译期检查承载）
}

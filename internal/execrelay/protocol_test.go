package execrelay

// 帧协议编解码单测：往返、载荷边界、违例 fail-closed（长度前缀/未知类型/
// 超限/流载荷 ID 形态）。

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFrameRoundTripJSON(t *testing.T) {
	open := SessionOpenFrame{ID: "01SESS", ContainerID: "abcdef123456", Cols: 120, Rows: 40}
	frame, err := EncodeOpenFrame(open)
	if err != nil {
		t.Fatalf("EncodeOpenFrame: %v", err)
	}
	t1, payload, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if t1 != TypeSessionOpen {
		t.Fatalf("type = %v, want session.open", t1)
	}
	var got SessionOpenFrame
	if err := DecodeJSONPayload(payload, &got); err != nil {
		t.Fatalf("DecodeJSONPayload: %v", err)
	}
	if got != open {
		t.Fatalf("round-trip mismatch: %+v != %+v", got, open)
	}
}

func TestFrameRoundTripStream(t *testing.T) {
	data := []byte("root@web:/# echo term-ok\n")
	frame, err := EncodeStreamPayload("01SESS", data)
	if err != nil {
		t.Fatalf("EncodeStreamPayload: %v", err)
	}
	msg, err := EncodeFrame(TypeStdout, frame)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	t1, payload, err := DecodeFrame(msg)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if t1 != TypeStdout {
		t.Fatalf("type = %v, want stdout", t1)
	}
	id, out, err := DecodeStreamPayload(payload)
	if err != nil {
		t.Fatalf("DecodeStreamPayload: %v", err)
	}
	if id != "01SESS" || !bytes.Equal(out, data) {
		t.Fatalf("stream round-trip mismatch: id=%q data=%q", id, out)
	}
}

func TestFrameRegisterRoundTrip(t *testing.T) {
	frame, err := EncodeRegisterFrame(RegisterFrame{Hostname: "abc123def456"})
	if err != nil {
		t.Fatalf("EncodeRegisterFrame: %v", err)
	}
	t1, payload, err := DecodeFrame(frame)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if t1 != TypeRegister {
		t.Fatalf("type = %v, want register", t1)
	}
	var reg RegisterFrame
	if err := json.Unmarshal(payload, &reg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if reg.Hostname != "abc123def456" {
		t.Fatalf("hostname = %q", reg.Hostname)
	}
}

func TestFrameRejectsShortMessage(t *testing.T) {
	if _, _, err := DecodeFrame([]byte{0, 0, 0}); err == nil {
		t.Fatal("short message must be rejected")
	}
}

func TestFrameRejectsLengthMismatch(t *testing.T) {
	frame, _ := EncodeFrame(TypePing, nil)
	frame[0] = 9 // 篡改长度前缀
	if _, _, err := DecodeFrame(frame); err == nil {
		t.Fatal("length prefix mismatch must be rejected")
	}
}

func TestFrameRejectsOversize(t *testing.T) {
	if _, err := EncodeFrame(TypeStdout, make([]byte, maxPayload+1)); err == nil {
		t.Fatal("oversize encode must fail")
	}
	if _, _, err := DecodeFrame(append([]byte{0xff, 0xff, 0xff, 0xff, byte(TypeStdout)}, make([]byte, 16)...)); err == nil {
		t.Fatal("oversize payload must be rejected")
	}
}

func TestFrameRejectsUnknownType(t *testing.T) {
	if _, _, err := DecodeFrame([]byte{0, 0, 0, 0, 0x7f}); err == nil {
		t.Fatal("unknown frame type must be rejected")
	}
}

func TestStreamPayloadRejectsBadID(t *testing.T) {
	if _, _, err := DecodeStreamPayload([]byte{0, 0}); err == nil {
		t.Fatal("zero-length id must be rejected")
	}
	long := strings.Repeat("x", maxSessionIDLen+1)
	if _, err := EncodeStreamPayload(long, nil); err == nil {
		t.Fatal("over-long id must be rejected at encode")
	}
	if _, _, err := DecodeStreamPayload(append([]byte{0xff, 0xff}, []byte("x")...)); err == nil {
		t.Fatal("id length beyond payload must be rejected")
	}
}

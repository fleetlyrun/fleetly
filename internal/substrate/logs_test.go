package substrate

import (
	"strings"
	"testing"
	"time"
)

// TestSplitTimestamp Docker timestamps=true 头部解析（RFC3339Nano）。
func TestSplitTimestamp(t *testing.T) {
	at, rest := splitTimestamp("2026-09-18T12:00:00.123456789Z hello world")
	if at.IsZero() {
		t.Fatal("timestamp not parsed")
	}
	if want := time.Date(2026, 9, 18, 12, 0, 0, 123456789, time.UTC); !at.Equal(want) {
		t.Fatalf("at = %v, want %v", at, want)
	}
	if rest != "hello world" {
		t.Fatalf("rest = %q", rest)
	}
	// 无头部（防御形态）原样通过。
	at2, rest2 := splitTimestamp("plain line")
	if !at2.IsZero() || rest2 != "plain line" {
		t.Fatalf("plain: %v %q", at2, rest2)
	}
	// 时间戳后无空格结尾（空内容）。
	at3, rest3 := splitTimestamp("2026-09-18T12:00:00Z ")
	if at3.IsZero() || rest3 != "" {
		t.Fatalf("trailing: %v %q", at3, rest3)
	}
	if strings.Contains(rest3, " ") {
		t.Fatal("rest should be trimmed of header")
	}
}

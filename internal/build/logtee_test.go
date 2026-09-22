package build

// 构建日志行分流的 hermetic 单测（W5-S1）：文件写入逐字透传、行切分与
// 元数据携带、sink panic 吞掉不伤构建本体、\r 修剪与无尾换行残行。

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// fakeSink 是 BuildLogSink 假件（可编程 panic）。
type fakeSink struct {
	lines []string
	apps  []string
	svcs  []string
	panicked bool
}

func (f *fakeSink) IngestBuildLine(appID, app, service string, _ time.Time, line string) {
	f.apps = append(f.apps, app+"/"+service)
	f.svcs = append(f.svcs, appID)
	if f.panicked {
		panic("sink exploded")
	}
	f.lines = append(f.lines, line)
}

// TestLogTeePassthrough 文件写入逐字不变（分流不影响权威审计面）。
func TestLogTeePassthrough(t *testing.T) {
	var buf bytes.Buffer
	sink := &fakeSink{}
	tw := newLogTee(&buf, sink, "appID", "app", "web")
	if _, ok := tw.(*logTee); !ok {
		t.Fatal("newLogTee with sink should wrap")
	}
	if _, err := io.WriteString(tw, "#1 [4/4] RUN go build\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if buf.String() != "#1 [4/4] RUN go build\n" {
		t.Fatalf("passthrough broken: %q", buf.String())
	}
	if len(sink.lines) != 1 || sink.lines[0] != "#1 [4/4] RUN go build" {
		t.Fatalf("sink lines = %v", sink.lines)
	}
	if len(sink.apps) != 1 || sink.apps[0] != "app/web" || sink.svcs[0] != "appID" {
		t.Fatalf("metadata = %v %v", sink.apps, sink.svcs)
	}
}

// TestLogTeeNilSinkIsRawWriter sink 未装配 = 零差异透传（不包 tee）。
func TestLogTeeNilSinkIsRawWriter(t *testing.T) {
	var buf bytes.Buffer
	tw := newLogTee(&buf, nil, "a", "b", "c")
	if _, ok := tw.(*logTee); ok {
		t.Fatal("nil sink must return the raw writer")
	}
}

// TestLogTeeSplitsLinesAndTrimsCR 多行一次写入按行切分；\r 修剪
//（buildkit 进度刷新形态）；空行不分流。
func TestLogTeeSplitsLinesAndTrimsCR(t *testing.T) {
	var buf bytes.Buffer
	sink := &fakeSink{}
	tw := newLogTee(&buf, sink, "id", "app", "svc")
	input := "line one\r\nline two\r\n\nline three\n"
	if _, err := io.WriteString(tw, input); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := []string{"line one", "line two", "line three"}
	if len(sink.lines) != len(want) {
		t.Fatalf("lines = %v, want %v", sink.lines, want)
	}
	for i := range want {
		if sink.lines[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, sink.lines[i], want[i])
		}
	}
	if buf.String() != input {
		t.Fatal("passthrough diverged from input")
	}
}

// TestLogTeePanicSwallowed sink panic 被吞（观测面不拖垮构建本体——分流
// 降级为纯透传，后续行不再触发 sink）。
func TestLogTeePanicSwallowed(t *testing.T) {
	var buf bytes.Buffer
	sink := &fakeSink{panicked: true}
	tw := newLogTee(&buf, sink, "id", "app", "svc")
	if _, err := io.WriteString(tw, "first\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := io.WriteString(tw, "second\n"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if buf.String() != "first\nsecond\n" {
		t.Fatalf("passthrough after panic broken: %q", buf.String())
	}
	if len(sink.lines) != 0 {
		t.Fatalf("sink should have recorded nothing: %v", sink.lines)
	}
}

// TestLogTeeResidualWithoutNewline 无尾换行的残行驻留待补（不丢、不重复）。
func TestLogTeeResidualWithoutNewline(t *testing.T) {
	var buf bytes.Buffer
	sink := &fakeSink{}
	tw := newLogTee(&buf, sink, "id", "app", "svc")
	if _, err := io.WriteString(tw, "half "); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(sink.lines) != 0 {
		t.Fatalf("residual flushed early: %v", sink.lines)
	}
	if _, err := io.WriteString(tw, "line\n"); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if len(sink.lines) != 1 || sink.lines[0] != "half line" {
		t.Fatalf("lines = %v, want [half line]", sink.lines)
	}
}

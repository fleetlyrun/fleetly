package build

// 构建日志行的下游分流（W5-S1，E6 观测专项设计 §2.3「build 日志与写入
// 咽喉点接入批量器，source=build」）：咽喉点 = build.log 的行级写入
//（Builder.run 的 logw io.Writer——buildkit solve 的进度输出唯一落点）。
// Tee 语义：文件写入逐字不变（builds 表 log_path 产物是权威审计面），
// 分流出的每一行同步喂给 IngestBackend（实现 = logs.Manager——入湖前过
// 该 app 的脱敏值集）。分流**尽力而为**：sink panic/慢/错都不影响构建
// 本体（观测面不反向施压构建管线——H9 联动回调同款纪律）。

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// BuildLogSink 是构建日志行的下游消费端口（在 build 定义、logs 实现——
// 方向纪律与 engine.RoutePublisher 同型）。at 为行到达时刻（构建日志行
// 无原生时间戳——诚实缺失，与 History 读侧「行时间取构建开始时刻」的
// 口径在此收敛为逐行墙钟，检索面排序粒度更细）。
type BuildLogSink interface {
	IngestBuildLine(appID, app, service string, at time.Time, line string)
}

// logTee 是 io.Writer 形态的行分流器（包装原文件 writer，写行为逐字
// 透传；行边界按 '\n' 切分，'\r' 修剪——buildkit 进度输出含 \r 刷新）。
type logTee struct {
	w io.Writer
	// sink/appID/app/service 是分流目标（每构建行共用元数据）。
	sink                     BuildLogSink
	appID, app, service      string
	mu                       sync.Mutex
	buf                      []byte
}

// newLogTee 构造分流 writer（sink 为 nil 时退化为纯透传——零行为差异）。
func newLogTee(w io.Writer, sink BuildLogSink, appID, app, service string) io.Writer {
	if sink == nil {
		return w
	}
	return &logTee{w: w, sink: sink, appID: appID, app: app, service: service}
}

func (t *logTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	n, err := t.w.Write(p)
	t.feed(p)
	t.mu.Unlock()
	return n, err
}

// feed 按行切分并分流（尽力而为：sink 调用 panic 被吞——观测面不拖垮
// 构建本体；行内容不复制给 sink 之外的面）。
func (t *logTee) feed(p []byte) {
	defer func() {
		if r := recover(); r != nil {
			// sink 异常按无 sink 处理（分流是 best-effort 观测面）。
			t.sink = nil
		}
	}()
	t.buf = append(t.buf, p...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			// 防御：无换行的超长残行不无限驻留（buildkit 行均以换行结尾；
			// 残行上限 = 单行 1MiB 扫描口径，超限丢弃）。
			if len(t.buf) > 1024*1024 {
				t.buf = t.buf[:0]
			}
			return
		}
		line := string(t.buf[:idx])
		t.buf = t.buf[idx+1:]
		line = trimCR(line)
		if line == "" {
			continue
		}
		at := time.Now()
		func() {
			defer func() { _ = recover() }()
			t.sink.IngestBuildLine(t.appID, t.app, t.service, at, line)
		}()
	}
}

func trimCR(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

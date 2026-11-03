// read.go 是镜像读取面（ADR-0091 决策 3）：tail/时间窗/子串过滤的窗口读
// 与轮转感知的尾随。Read 拼接全部在档轮转文件（旧→新），Follow 只投调用
// 时刻起的新增（存量由调用方经 Read 先投——缝隙容忍在 ADR best-effort
// 契约内）。半截行（读到写到一半的 JSON）静默跳过：单写者 append 形态下
// 只可能出现在活跃文件尾部。
package platformlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrNoMirror 报告镜像缺失（logs/ 目录或任何镜像文件不在场）——
// interfaces 层译 E_LOG_STORE_UNAVAILABLE（与「无日志行」区分）。
var ErrNoMirror = errors.New("platform log mirror is not present under the data root")

// Query 是镜像读取过滤面：TailLines 0 = 不截尾；Since/Until 零值 = 不限；
// Text 非空 = 子串匹配（bytes.Contains，大小写敏感——logs 面检索路径同
// 口径）。
type Query struct {
	TailLines int64
	Since     time.Time
	Until     time.Time
	Text      string
}

// Frame 是一帧镜像日志：Time/Level/Msg 是 JSON 行投影，Line 是原始行
// 字节（零改写——method/duration/error/error_id 全量字段保留）。
type Frame struct {
	Time  time.Time
	Level string
	Msg   string
	Line  []byte
}

// mirrorPath 返回数据根下活跃镜像文件路径。
func mirrorPath(dataRoot string) string {
	return filepath.Join(dataRoot, LogDirName, FileName)
}

// archivedPaths 返回轮转历史文件路径（旧→新：.log.3, .log.2, .log.1）。
func archivedPaths(dataRoot string) []string {
	active := mirrorPath(dataRoot)
	out := make([]string, 0, MaxFiles-1)
	for i := MaxFiles - 1; i >= 1; i-- {
		out = append(out, fmt.Sprintf("%s.%d", active, i))
	}
	return out
}

// Read 读镜像窗口（旧→新）。TailLines 只作用于过滤后的结果（先窗后尾）。
func Read(dataRoot string, q Query) ([]Frame, error) {
	files := archivedPaths(dataRoot)
	files = append(files, mirrorPath(dataRoot))
	present := files[:0]
	for _, p := range files {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			present = append(present, p)
		}
	}
	if len(present) == 0 {
		return nil, ErrNoMirror
	}
	var frames []Frame
	for _, p := range present {
		fs, err := readFileFrames(p, q)
		if err != nil {
			return nil, err
		}
		frames = append(frames, fs...)
	}
	if q.TailLines > 0 && int64(len(frames)) > q.TailLines {
		frames = frames[int64(len(frames))-q.TailLines:]
	}
	return frames, nil
}

// readFileFrames 读单文件并按 Query 过滤（半截/坏行静默跳过）。
func readFileFrames(path string, q Query) ([]Frame, error) {
	f, err := os.Open(path) //nolint:gosec // 数据根内常量路径（mirrorPath 派生）
	if err != nil {
		return nil, fmt.Errorf("platformlog: open %s: %w", filepath.Base(path), err)
	}
	defer func() { _ = f.Close() }()
	var out []Frame
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // 单行上限 1MB（超长行截断跳过）
	for sc.Scan() {
		frame, ok := parseFrame(sc.Bytes())
		if !ok || !q.matches(frame) {
			continue
		}
		out = append(out, frame)
	}
	return out, sc.Err()
}

// matches 施加时间窗与子串过滤。
func (q Query) matches(fr Frame) bool {
	if !q.Since.IsZero() && fr.Time.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && fr.Time.After(q.Until) {
		return false
	}
	if q.Text != "" && !bytes.Contains(fr.Line, []byte(q.Text)) {
		return false
	}
	return true
}

// parseFrame 解析镜像行（ts/level/msg 投影 + 原始行字节；非 JSON/缺 ts
// = 跳过）。ts 按 RFC3339 宽容（写入面毫秒 ISO 形态是其子集）。
func parseFrame(line []byte) (Frame, bool) {
	var head struct {
		Ts    string `json:"ts"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(line, &head); err != nil || head.Ts == "" {
		return Frame{}, false
	}
	ts, err := time.Parse(time.RFC3339, head.Ts)
	if err != nil {
		return Frame{}, false
	}
	return Frame{
		Time:  ts,
		Level: head.Level,
		Msg:   head.Msg,
		Line:  append([]byte(nil), line...),
	}, true
}

// rotatedSince 报告活跃路径是否已相对打开句柄轮转（inode 变更或 size
// 回缩），并回传当前盘上 size。
func rotatedSince(path string, f *os.File) (bool, int64, error) {
	var onDisk syscall.Stat_t
	if err := syscall.Stat(path, &onDisk); err != nil {
		return false, 0, err
	}
	var openFi os.FileInfo
	openFi, err := f.Stat()
	if err != nil {
		return false, 0, err
	}
	open, ok := openFi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, 0, fmt.Errorf("platformlog: unsupported stat shape")
	}
	rotated := open.Ino != onDisk.Ino || onDisk.Size < open.Size
	return rotated, int64(onDisk.Size), nil
}

// pollInterval 是 Follow 的文件增长轮询拍。
const pollInterval = 250 * time.Millisecond

// Follow 尾随活跃镜像文件的增长（轮转感知：size 回缩/inode 变更 = 重开
// 新文件从零续读）。只投调用时刻起的新增行；ctx 取消即返回 ctx.Err()。
// fn 返回错误短路尾随（错误原样上抛）。
func Follow(ctx context.Context, dataRoot string, q Query, fn func(Frame) error) error {
	path := mirrorPath(dataRoot)
	f, err := os.Open(path) //nolint:gosec // 数据根内常量路径
	if err != nil {
		return ErrNoMirror
	}
	defer func() { _ = f.Close() }()
	// 从当前末尾起（存量不经此面——调用方经 Read 先投）。
	offset, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("platformlog: seek mirror: %w", err)
	}
	var pending []byte // 跨读次的半截行缓冲
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			offset += int64(n) // 消费进度只按实际读到的字节推进（不跳 stat 值）
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := pending[:i]
				pending = pending[i+1:]
				if frame, ok := parseFrame(line); ok && q.matches(frame) {
					if err := fn(frame); err != nil {
						return err
					}
				}
			}
		}
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, os.ErrClosed) {
			return fmt.Errorf("platformlog: read mirror: %w", rerr)
		}
		// 轮转检测：inode 变更或盘上 size 回缩 = 活跃文件已轮转，重开从零
		// 续读（pending 是旧文件半截行，丢弃——轮转边界必是完整行尾）。
		rotated, diskSize, serr := rotatedSince(path, f)
		if serr != nil {
			return fmt.Errorf("platformlog: stat mirror: %w", serr)
		}
		if rotated {
			_ = f.Close()
			nf, oerr := os.Open(path) //nolint:gosec // 数据根内常量路径
			if oerr != nil {
				return ErrNoMirror
			}
			f = nf
			pending = pending[:0]
			offset = 0
			continue
		}
		if n == 0 && diskSize == offset {
			// 无增长：退避等待（ctx 感知）。
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
		}
		// n>0 或盘上还有未读字节（diskSize>offset）：立即续读不退避。
	}
}

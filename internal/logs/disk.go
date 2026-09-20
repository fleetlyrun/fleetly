package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 落盘存储（T2.20 验收：历史落盘检索 + 7 天轮转）：目录布局
//
//	<dir>/<app>/<YYYYMMDD>.jsonl
//
// 按天分文件（UTC 日界）；每行一条 Entry JSON。轮转 = 清理日期早于
// 保留窗起点的文件与空 app 目录。History 检索按时间窗覆盖的日期文件
// 线性扫描（v0.1 单机规模：7 天 × 每日文件，线性可接受）。

type diskStore struct {
	dir string
}

func newDiskStore(dir string) *diskStore { return &diskStore{dir: dir} }

// dayFormat 是日期文件名前缀格式（UTC）。
const dayFormat = "20060102"

func dayName(at time.Time) string { return at.UTC().Format(dayFormat) }

// append 追加一条（目录按需创建；单行 JSON + \n）。
func (d *diskStore) append(ctx context.Context, e Entry) error {
	appDir := filepath.Join(d.dir, e.App)
	if err := os.MkdirAll(appDir, 0o750); err != nil {
		return fmt.Errorf("logs: mkdir %s: %w", appDir, err)
	}
	path := filepath.Join(appDir, dayName(e.At)+".jsonl")
	//nolint:gosec // G304：路径 = 受管根目录 + 日期白名单名（dayName）拼出，无用户可控穿越面
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logs: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("logs: marshal entry: %w", err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("logs: write entry: %w", err)
	}
	return nil
}

// query 扫描时间窗覆盖的日期文件，返回过滤后的条目（升序；超过 limit 时
// 取最新 limit 条）。app 必填；service/source 空 = 不过滤；since/until 零
// 值 = 不设界（until 缺省 = 现在）。
func (d *diskStore) query(ctx context.Context, app, service, source string, since, until time.Time, limit int) ([]Entry, error) {
	if until.IsZero() {
		until = time.Now().UTC()
	}
	appDir := filepath.Join(d.dir, app)
	days, err := daysInWindow(appDir, since, until)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // 无落盘 = 空结果（未采集过属正常）
		}
		return nil, err
	}
	var out []Entry
	for _, day := range days {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		path := filepath.Join(appDir, day+".jsonl")
		rows, err := readDayFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range rows {
			if !since.IsZero() && e.At.Before(since) {
				continue
			}
			if e.At.After(until) {
				continue
			}
			if service != "" && e.Service != service {
				continue
			}
			if source != "" && e.Source != source {
				continue
			}
			out = append(out, e)
		}
	}
	// 时间过滤后仍可能远超 limit：升序保留最新 limit 条。
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// daysInWindow 列出窗口覆盖且实际存在的日期文件名（升序）。M7-3：since
// 零值 = 不设下界（query 契约「零值 = 不设界」）——start 置空串，列出
// ≤ until 的全部保留日文件；旧实现把起点折叠到 until 当日，since 缺省的
// 检索只命中单日文件，既往历史全部丢失（api ListHistoryLogs 缺省不传
// since，默认路径即受害形态）。M7-14：until 统一 UTC 归一后再格式化
// （本地时区与 UTC 跨日时，本地形态的 end 会把 until 当日的 UTC 文件
// 错排除在界外）。
func daysInWindow(appDir string, since, until time.Time) ([]string, error) {
	start := ""
	if !since.IsZero() {
		start = since.UTC().Format(dayFormat)
	}
	end := until.UTC().Format(dayFormat)
	names, err := listDayFiles(appDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if n >= start && n <= end {
			out = append(out, n)
		}
	}
	return out, nil
}

// listDayFiles 返回 app 目录下的日期文件名（升序）。
func listDayFiles(appDir string) ([]string, error) {
	entries, err := os.ReadDir(appDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if len(day) != 8 {
			continue
		}
		out = append(out, day)
	}
	sort.Strings(out)
	return out, nil
}

// readDayFile 读取单日文件的全部条目（坏行跳过并继续——日志面尽力而为，
// 不因单行损坏丢整日）。
func readDayFile(path string) ([]Entry, error) {
	f, err := os.Open(path) //nolint:gosec // G304：路径由受管根目录 + 日期白名单名拼出
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // 坏行跳过
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("logs: scan %s: %w", path, err)
	}
	return out, nil
}

// prune 删除保留窗外的日期文件与空 app 目录，返回清理文件数。
func (d *diskStore) prune(ctx context.Context, retention time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-retention).Format(dayFormat)
	appDirs, err := os.ReadDir(d.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("logs: read root %s: %w", d.dir, err)
	}
	removed := 0
	for _, ad := range appDirs {
		if !ad.IsDir() {
			continue
		}
		select {
		case <-ctx.Done():
			return removed, ctx.Err()
		default:
		}
		appDir := filepath.Join(d.dir, ad.Name())
		days, err := listDayFiles(appDir)
		if err != nil {
			continue
		}
		for _, day := range days {
			if day >= cutoff {
				continue
			}
			if err := os.Remove(filepath.Join(appDir, day+".jsonl")); err == nil {
				removed++
			}
		}
		// 空目录顺手清（无文件且无子目录）。
		if empty, err := dirIsEmpty(appDir); err == nil && empty {
			_ = os.Remove(appDir)
		}
	}
	return removed, nil
}

func dirIsEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

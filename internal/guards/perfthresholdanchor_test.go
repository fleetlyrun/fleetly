package guards

// G5「性能阈值锚律」——执法手册 G5 条目的反推形态。
//
// 渊源：G5 条目名已随执法手册底稿失考（同 P4-3 G17 先例、P5-1 G7/G8 同
// 款反推），形态由 P5-5 阈值锚批反推——三个性能敏感机制的容量结论此前只
// 活在评审对话里，本守卫把它钉进代码注释成为唯一载体：
//   - SSE/follow 事件流面（fleetlygrpc/contexts.go 的
//     streamEventsPollInterval）锚 ≈50 并发跟随流；
//   - task 轮询面（engine/task.go 的 taskStep ListDriving 全量单查询）锚
//     ≈500 driving task；
//   - 冻结 Spec blob（engine/deployspec.go 的 protojson.Marshal 单行物化）
//     锚 ≈3k task/冻结 Spec。
//
// 律文：上述三处的锚注释必须在场——统一以「性能阈值锚(P5-5)」开头，容
// 量数字逐字落在含标记的注释块内。锚注释漂移或删除即容量结论失考（后续
// 评审在无凭据的代码上从零重来）。注释措辞本身声明为「分析容量锚」非实
// 测上限，本守卫只执法在场性，不执法数值推导。
//
// 防盲：三文件路径在册、文件缺失即红；数字必须落在标记所在注释块内——
// 文件其他位置的巧合数字不计数（"50" 恰是 "501" 的子串）。

import (
	"fmt"
	"strings"
	"testing"
)

// perfAnchorMarker 是三处锚注释的统一开头标记（守卫扫描锚与读者识别锚
// 同串）。
const perfAnchorMarker = "性能阈值锚(P5-5)"

// perfThresholdAnchor 是一处性能阈值锚的执法面。
type perfThresholdAnchor struct {
	path   string // 仓库相对路径（斜杠分隔）
	number string // 容量数字（逐字，须与标记同注释块在场）
	label  string // 机制名（报错指认用）
}

// perfThresholdAnchors 是三锚路径表（P5-5）：文件移动须随本表同批更新。
var perfThresholdAnchors = []perfThresholdAnchor{
	{
		path:   "internal/interfaces/grpc/fleetlygrpc/contexts.go",
		number: "50",
		label:  "SSE/follow 事件流面（streamEventsPollInterval）",
	},
	{
		path:   "internal/application/engine/task.go",
		number: "500",
		label:  "task 轮询面（taskStep ListDriving 全量单查询）",
	},
	{
		path:   "internal/application/engine/deployspec.go",
		number: "3k",
		label:  "冻结 Spec blob（protojson.Marshal 单行物化）",
	},
}

// goCommentBlockAround 返回 src 中包含偏移 idx 所在行的连续 // 注释块
// （相邻注释行视为同块；空行或非注释行截断）。容量数字必须落在标记所在
// 块内：文件其他位置的巧合数字不计数。
func goCommentBlockAround(src string, idx int) string {
	lines := strings.Split(src, "\n")
	line := strings.Count(src[:idx], "\n")
	start := line
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "//") {
		start--
	}
	end := line
	for end+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[end+1]), "//") {
		end++
	}
	return strings.Join(lines[start:end+1], "\n")
}

// scanPerfThresholdAnchors 纯核（shapes 红灯实验直测）：files 是路径→内
// 容（缺键=文件缺）；返回违规清单，空 = 绿。
func scanPerfThresholdAnchors(files map[string]string) []string {
	var errs []string
	for _, a := range perfThresholdAnchors {
		src, ok := files[a.path]
		if !ok {
			errs = append(errs, fmt.Sprintf(
				"%s: file missing — %s 的容量锚执法面失明，文件移动须随守卫路径表同批更新",
				a.path, a.label))
			continue
		}
		i := strings.Index(src, perfAnchorMarker)
		if i < 0 {
			errs = append(errs, fmt.Sprintf(
				"%s: 性能阈值锚(P5-5) 标记缺席 — %s 的容量锚注释必须在场（锚注释是评审容量结论的唯一载体，删除即结论失考）",
				a.path, a.label))
			continue
		}
		if !strings.Contains(goCommentBlockAround(src, i), a.number) {
			errs = append(errs, fmt.Sprintf(
				"%s: 容量数字 %q 不在锚注释块内 — 数字须与标记同块逐字在场（块外巧合数字不计数）",
				a.path, a.number))
		}
	}
	return errs
}

// TestPerfThresholdAnchorsPresent：对账真文件——三锚在场、数字与标记同块
// （readFileLF 对缺失文件即刻 Fatal，路径表失明不静默）。
func TestPerfThresholdAnchorsPresent(t *testing.T) {
	files := make(map[string]string, len(perfThresholdAnchors))
	for _, a := range perfThresholdAnchors {
		files[a.path] = readFileLF(t, a.path)
	}
	if errs := scanPerfThresholdAnchors(files); len(errs) > 0 {
		t.Fatalf("performance threshold anchor(s) drifted:\n%s", strings.Join(errs, "\n"))
	}
}

// TestPerfThresholdAnchorShapes：纯核四态红灯实验（守卫自身失明即红）——
// 标记在=绿 / 标记缺=红 / 数字缺=红 / 文件缺=红。
func TestPerfThresholdAnchorShapes(t *testing.T) {
	anchorFile := func(number string) string {
		return "// 性能阈值锚(P5-5)：分析容量 ≈" + number + " 量级（分析容量锚，非实测上限）。\n\nfunc f() {}\n"
	}
	goodFiles := func() map[string]string {
		m := make(map[string]string, len(perfThresholdAnchors))
		for _, a := range perfThresholdAnchors {
			m[a.path] = anchorFile(a.number)
		}
		return m
	}

	// 绿：三锚标记 + 数字同块在场。
	if errs := scanPerfThresholdAnchors(goodFiles()); len(errs) != 0 {
		t.Fatalf("marker+number in block must be green, got %v", errs)
	}

	// 红：标记缺（容量结论失去唯一载体；数字在场也救不回）。
	noMarker := goodFiles()
	a := perfThresholdAnchors[0]
	noMarker[a.path] = "// 普通注释：分析容量 ≈" + a.number + " 量级，但无统一标记。\n"
	errs := scanPerfThresholdAnchors(noMarker)
	if len(errs) != 1 || !strings.Contains(errs[0], a.path) || !strings.Contains(errs[0], "标记缺席") {
		t.Fatalf("missing marker must be red with locating message, got %v", errs)
	}

	// 红：数字缺——块内无数字即红；块外巧合数字（"5001" 含 "500"）不计数。
	noNumber := goodFiles()
	b := perfThresholdAnchors[1]
	noNumber[b.path] = "// 性能阈值锚(P5-5)：分析容量锚在场，但数字被删。\n\nvar n = 5001\n"
	errs = scanPerfThresholdAnchors(noNumber)
	if len(errs) != 1 || !strings.Contains(errs[0], b.path) || !strings.Contains(errs[0], `"500"`) {
		t.Fatalf("missing number must be red, got %v", errs)
	}

	// 红：文件缺（键缺席 = 路径表失明）。
	noFile := goodFiles()
	c := perfThresholdAnchors[2]
	delete(noFile, c.path)
	errs = scanPerfThresholdAnchors(noFile)
	if len(errs) != 1 || !strings.Contains(errs[0], c.path) || !strings.Contains(errs[0], "file missing") {
		t.Fatalf("missing file must be red, got %v", errs)
	}
}

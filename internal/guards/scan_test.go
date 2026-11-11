// Package guards 承载结构契约测试（架构由测试承载，架构文档 §11）：
// 编排器 SDK 圈禁、model/spec 叶子纯度、providers 隔离、词汇禁词扫描。
// 扫描 idiom：runtime.Caller 定位仓库根 → WalkDir 枚举 → CRLF 归一 →
// 命中对照白名单（条目带理由；不再命中即红——双向保鲜）。
package guards

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot 返回仓库根（internal/guards/scan_test.go 上溯两层）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// generatedFile 报告 rel 是否生成物（buf/protoc/wire）。import 契约测试不扫
// 生成物：import 结构由 buf/wire 生成模板决定，改它必须走再生成而不是改
// 依赖（手改生成物本身就是错，AGENTS.md 生成物纪律）。词汇扫描（
// wording_test.go）裁决相反——生成文本随仓发布、面向 SDK/网关消费方，是
// 全仓文本面的一部分，禁词契约不豁免生成物（批 D 裁决，2026-10-03）。
func generatedFile(rel string) bool {
	return strings.HasSuffix(rel, ".pb.go") ||
		strings.HasSuffix(rel, ".pb.gw.go") ||
		strings.HasSuffix(rel, "wire_gen.go") ||
		strings.HasSuffix(rel, ".swagger.json")
}

// scanModulePrefixes 是契约测试执法面的 module/目录前缀集（批 D 扩面：主
// module 之外纳入 sdk/go 与 genproto 两 module——SDK 与契约生成 module
// 同在契约测试契约面上）。
var scanModulePrefixes = []string{
	"internal/",
	"cmd/",
	"sdk/go/",
	"genproto/",
	"e2e/", // P1a-4（ADR-0075 验收锚）：dind helper 的 main 包进 Go 执法面
}

// scanGoModulePath 报告 rel（斜杠分隔）是否落 Go 执法面（词汇扫描与
// 调用级扫描共用的前缀裁决；词汇扫描另加 proto/skills/install.sh 面）。
func scanGoModulePath(rel string) bool {
	for _, p := range scanModulePrefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// readFileLF 读文件并归一 CRLF（Windows 检出漂移教训）。
func readFileLF(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

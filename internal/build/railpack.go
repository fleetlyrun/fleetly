package build

// railpack 驱动（D6）：以 Go 库形式驱动 railwayapp/railpack（钉版常量，
// 与 go.mod 版本由 railpack_version_test.go 互钉——Spike A E1：不钉版则
// plan 两天内漂移）。plan JSON 归档保证可复现取证（E1 结论）。

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	rkbuildkit "github.com/railwayapp/railpack/buildkit"
	rkcore "github.com/railwayapp/railpack/core"
	rkapp "github.com/railwayapp/railpack/core/app"
	rkconfig "github.com/railwayapp/railpack/core/config"
	"github.com/railwayapp/railpack/core/logger"
	"github.com/railwayapp/railpack/core/plan"

	"github.com/moby/buildkit/client/llb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// RailpackVersion 是平台钉死的 railpack 版本（唯一真源：本常量与 go.mod
// require 行，单测互钉；平台注入 GenerateBuildPlanOptions.RailpackVersion，
// 不信任环境）。
const RailpackVersion = "v0.39.0"

// railpackPlan 生成构建计划：返回 LLB 定义、镜像配置 JSON（docker 导出
// 携带）、以及归档用的 plan JSON 字节（E1 形态：plan + $schema 链接，
// MarshalIndent 稳定缩进）。
func railpackPlan(ctx context.Context, req Request) (*llb.Definition, string, []byte, error) {
	app, err := rkapp.NewApp(req.ContextDir)
	if err != nil {
		return nil, "", nil, fmtErr("railpack: open app %s: %w", req.ContextDir, err)
	}
	// 构建期环境 = 平台声明的构建凭证集（v0.1 恒为空集；契约见 Request.Secrets）。
	env, err := rkapp.FromEnvs(nil)
	if err != nil {
		return nil, "", nil, fmtErr("railpack: prepare env: %w", err)
	}
	result, err := rkcore.GenerateBuildPlan(app, env, &rkcore.GenerateBuildPlanOptions{
		// 平台注入钉版版本（E1：unpinned 时 mise 静默解析最新版漂移）。
		RailpackVersion: RailpackVersion,
	})
	if err != nil {
		return nil, "", nil, fmtErr("railpack: %w", err)
	}
	if !result.Success {
		// 失败原因在 BuildResult.Logs（railpack 失败形态：Success=false +
		// 诊断日志、err 为 nil）；尾部拼接进错误信息供 E_BUILD_FAILED 带出。
		if detail := railpackLogTail(result.Logs); detail != "" {
			return nil, "", nil, fmtErr("railpack: plan generation failed: %s (service language/framework not detected by railpack? fall back to build.dockerfile)", detail)
		}
		return nil, "", nil, fmtErr("railpack: plan generation not successful (service language/framework not detected by railpack? fall back to build.dockerfile)")
	}

	planJSON, err := archivePlanJSON(result.Plan)
	if err != nil {
		return nil, "", nil, err
	}

	buildPlatform, err := rkbuildkit.ParsePlatformWithDefaults(req.Platform)
	if err != nil {
		return nil, "", nil, fmtErr("railpack: parse platform %q: %w", req.Platform, err)
	}
	llbState, image, err := rkbuildkit.ConvertPlanToLLB(result.Plan, rkbuildkit.ConvertPlanOptions{
		BuildPlatform: buildPlatform,
		// 缓存键前缀按应用稳定（cache mount 跨构建复用、跨应用隔离）。
		CacheKey: CacheKeyPrefix(req.AppName),
		// secrets-hash 戳（E2a）：凭证集变化即相关层失效；确定性哈希见
		// SecretsHash（修正上游 map 迭代序缺陷）。
		SecretsHash: SecretsHash(req.Secrets),
	})
	if err != nil {
		return nil, "", nil, fmtErr("railpack: convert plan to LLB: %w", err)
	}
	marshaled, err := llbState.Marshal(ctx, llb.Platform(ocispecs.Platform(buildPlatform)))
	if err != nil {
		return nil, "", nil, fmtErr("railpack: marshal LLB: %w", err)
	}
	imageConfig, err := json.Marshal(image)
	if err != nil {
		return nil, "", nil, fmtErr("railpack: marshal image config: %w", err)
	}
	return marshaled, string(imageConfig), planJSON, nil
}

// archivePlanJSON 把 plan 序列化为归档形态（E1 byte-reproducible 形态：
// plan JSON + $schema 链接 + 两空格缩进；键序由 encoding/json 保证）。
func archivePlanJSON(p *plan.BuildPlan) ([]byte, error) {
	planBytes, err := json.Marshal(p)
	if err != nil {
		return nil, fmtErr("railpack: marshal plan: %w", err)
	}
	var planMap map[string]any
	if err := json.Unmarshal(planBytes, &planMap); err != nil {
		return nil, fmtErr("railpack: remap plan: %w", err)
	}
	planMap["$schema"] = rkconfig.SchemaUrl
	out, err := json.MarshalIndent(planMap, "", "  ")
	if err != nil {
		return nil, fmtErr("railpack: indent plan: %w", err)
	}
	return out, nil
}

// writePlanArchive 落盘 plan JSON（仅 railpack 路径调用）。
func writePlanArchive(path string, planJSON []byte) error {
	if err := os.WriteFile(path, planJSON, 0o600); err != nil {
		return fmtErr("write plan archive %s: %w", path, err)
	}
	return nil
}

// railpackLogTail 拼接 plan 生成诊断日志的尾部（最多 8 条非空日志——错误
// 原因在最后几条；单条超长截断防错误信息爆炸）。
func railpackLogTail(logs []logger.Msg) string {
	var lines []string
	for _, m := range logs {
		if m.Msg == "" {
			continue
		}
		msg := m.Msg
		if len(msg) > 300 {
			msg = msg[:300]
		}
		lines = append(lines, msg)
	}
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, " | ")
}

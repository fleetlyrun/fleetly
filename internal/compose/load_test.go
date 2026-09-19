package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// writeCompose 把 YAML 内容写入临时目录并返回文件路径（每个用例独立目录，
// 避免 env_file/相对路径串扰）。
func writeCompose(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return path
}

// writeComposeWith 是 writeCompose 的多文件版（支持 env_file 夹具）。
func writeComposeWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return filepath.Join(dir, "compose.yaml")
}

// loadOK 断言加载成功并返回 Spec。
func loadOK(t *testing.T, path string) *Spec {
	t.Helper()
	spec, _, err := Load(context.Background(), path)
	if err != nil {
		t.Fatalf("Load(%s) 意外失败: %v", path, err)
	}
	return spec
}

// loadErr 断言加载失败且错误码为 wantCode，返回 *apperr.Error 供路径断言。
func loadErr(t *testing.T, content, wantCode string) *apperr.Error {
	t.Helper()
	spec, _, err := Load(context.Background(), writeCompose(t, content))
	if err == nil {
		t.Fatalf("Load 意外成功（期望 %s）：%+v", wantCode, spec)
	}
	var ae *apperr.Error
	if !asAppErr(err, &ae) {
		t.Fatalf("错误类型不是 *apperr.Error: %T %v", err, err)
	}
	if ae.Code() != wantCode {
		t.Fatalf("错误码 = %s, 期望 %s（message: %s）", ae.Code(), wantCode, ae.Message())
	}
	return ae
}

// asAppErr 是 errors.As 的包内薄封装（测试可读性）。
func asAppErr(err error, target **apperr.Error) bool {
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestLoadArchitectureExample 验收 2：架构 §2.4 的 my-api 示例解析+校验+
// 归一化全链通过。
func TestLoadArchitectureExample(t *testing.T) {
	spec, warnings, err := Load(context.Background(), filepath.Join("testdata", "valid", "my-api.compose.yaml"))
	if err != nil {
		t.Fatalf("load §2.4 example: %v", err)
	}
	if spec.Name != "my-api" {
		t.Errorf("Name = %q, 期望 my-api", spec.Name)
	}
	if len(spec.Services) != 2 {
		t.Fatalf("services = %d, 期望 2", len(spec.Services))
	}
	web := spec.Services[0]
	if web.Name != "web" {
		t.Fatalf("首个服务 = %q, 期望 web（字典序）", web.Name)
	}
	if web.Build == nil || web.Build.Context != "." || web.Build.Dockerfile != "" {
		t.Errorf("web.Build = %+v, 期望 {context:.}（无 dockerfile → Railpack 模式）", web.Build)
	}
	if len(web.Expose) != 1 || web.Expose[0] != "8080" {
		t.Errorf("web.Expose = %v", web.Expose)
	}
	if strings.Join(web.Domains, ",") != "api.example.com,www.api.example.com" {
		t.Errorf("web.Domains = %v", web.Domains)
	}
	if web.Healthcheck == nil || web.Healthcheck.StartPeriod != "10s" {
		t.Errorf("web.Healthcheck = %+v", web.Healthcheck)
	}
	if web.Deploy == nil || web.Deploy.UpdateConfig == nil || web.Deploy.UpdateConfig.Order != "start-first" {
		t.Errorf("web.Deploy = %+v", web.Deploy)
	}
	if web.Deploy.Resources == nil || web.Deploy.Resources.Limits == nil ||
		web.Deploy.Resources.Limits.CPUS != 0.5 || web.Deploy.Resources.Limits.MemoryBytes != 268435456 {
		t.Errorf("web 资源限额 = %+v, 期望 0.5 CPU / 256M", web.Deploy.Resources)
	}
	// 受管字段校验通过后不进快照（failure_action 恒为 pause 平台常量）。
	if web.Deploy.UpdateConfig.Order != "" && rawJSONContains(t, spec, "failure_action") {
		t.Errorf("归一化结果不应携带受管字段 failure_action")
	}
	worker := spec.Services[1]
	if strings.Join(worker.Command, " ") != "node worker.js" {
		t.Errorf("worker.Command = %v", worker.Command)
	}
	if strings.Join(spec.Secrets, ",") != "" {
		t.Errorf("Secrets = %v, 期望空（S16-C1：secrets 校验层显式拒绝，归一化结构性不含）", spec.Secrets)
	}
	if len(spec.Volumes) != 1 || spec.Volumes[0].Key != "data" {
		t.Errorf("Volumes = %+v", spec.Volumes)
	}
	if spec.SpecHash == "" {
		t.Error("spec_hash 未计算")
	}
	// worker 无 healthcheck → W_DEPLOY_NO_HEALTHCHECK 警告（注册码）。
	var hasHC bool
	for _, w := range warnings {
		if w.Code == "W_DEPLOY_NO_HEALTHCHECK" && w.Service == "worker" {
			hasHC = true
		}
	}
	if !hasHC {
		t.Errorf("warnings 缺少 worker 的 W_DEPLOY_NO_HEALTHCHECK: %+v", warnings)
	}
}

// rawJSONContains 判断 Spec canonical JSON 是否包含子串。
func rawJSONContains(t *testing.T, s *Spec, sub string) bool {
	t.Helper()
	raw, err := s.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	return strings.Contains(string(raw), sub)
}

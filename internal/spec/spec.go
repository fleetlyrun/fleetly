// Package spec 承接 IR 的校验、归一化与包装（原始生成类型住 genproto
// module，tech-stack §2）。叶子纯度：不 import 任何 fleetly internal 包
// （守卫见 internal/guards）。
package spec

import (
	"fmt"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

// SchemaVersion 是当前 IR 版本（读入 check-strategy：可读旧版+提示，
// 拒绝跳代——只升不降，架构 §3）。
const SchemaVersion int32 = 1

// ValidationError 是校验失败（字段路径 + 理由；调用方转 apperr）。
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func invalidf(field, format string, args ...any) *ValidationError {
	return &ValidationError{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// ValidateApp 校验 AppSpec（归一化后的期望状态）。
func ValidateApp(s *specv1.AppSpec) error {
	if s == nil {
		return invalidf("app", "spec is nil")
	}
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("app.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if s.GetApp().GetId() == "" {
		return invalidf("app.id", "must not be empty")
	}
	if s.GetApp().GetProject() == "" {
		return invalidf("app.project", "must not be empty")
	}
	if err := validateSource(s.GetSource()); err != nil {
		return err
	}
	if len(s.GetProcesses()) == 0 {
		return invalidf("app.processes", "at least one process is required")
	}
	seen := map[string]bool{}
	for i, p := range s.GetProcesses() {
		if err := ValidateProcess(fmt.Sprintf("app.processes[%d]", i), p); err != nil {
			return err
		}
		if seen[p.GetName()] {
			return invalidf("app.processes[%d].name", "duplicate process name %q", p.GetName())
		}
		seen[p.GetName()] = true
	}
	return nil
}

// ValidateProcess 校验单个进程模板（TaskSpec 单元素复用同口径）。
func ValidateProcess(field string, p *specv1.ProcessSpec) error {
	if p == nil {
		return invalidf(field, "process is nil")
	}
	if p.GetName() == "" {
		return invalidf(field+".name", "must not be empty")
	}
	switch origin := p.GetImageOrigin().(type) {
	case *specv1.ProcessSpec_Image:
		if origin.Image == "" {
			return invalidf(field+".image", "must not be empty")
		}
	case *specv1.ProcessSpec_FromBuild:
		if origin.FromBuild == "" {
			return invalidf(field+".from_build", "must not be empty")
		}
	default:
		return invalidf(field, "image or from_build is required")
	}
	for i, port := range p.GetPorts() {
		if port.GetPort() < 1 || port.GetPort() > 65535 {
			return invalidf(fmt.Sprintf("%s.ports[%d].port", field, i), "port %d out of range", port.GetPort())
		}
		switch port.GetProtocol() {
		case specv1.Protocol_PROTOCOL_HTTP, specv1.Protocol_PROTOCOL_H2C, specv1.Protocol_PROTOCOL_TCP:
		default:
			return invalidf(fmt.Sprintf("%s.ports[%d].protocol", field, i), "protocol must be http, h2c or tcp")
		}
	}
	if p.GetReplicas() < 0 {
		return invalidf(field+".replicas", "must not be negative")
	}
	if err := validateHealthcheck(field+".healthcheck", p.GetHealthcheck()); err != nil {
		return err
	}
	for i, net := range p.GetNetworks() {
		if net == "" {
			return invalidf(fmt.Sprintf("%s.networks[%d]", field, i), "must not be empty")
		}
	}
	return nil
}

func validateHealthcheck(field string, h *specv1.HealthcheckSpec) error {
	if h == nil {
		return nil // 探针可选
	}
	kinds := 0
	if h.GetHttpPath() != "" {
		kinds++
	}
	if h.GetTcpPort() != 0 {
		kinds++
	}
	if h.GetExec() != nil {
		kinds++
	}
	if kinds == 0 {
		return invalidf(field, "one of http_path, tcp_port or exec is required")
	}
	if kinds > 1 {
		return invalidf(field, "at most one probe kind is allowed")
	}
	if h.GetRetries() < 0 {
		return invalidf(field+".retries", "must not be negative")
	}
	return nil
}

func validateSource(src *specv1.Source) error {
	switch origin := src.GetKind().(type) {
	case *specv1.Source_Git:
		if origin.Git.GetRepo() == "" {
			return invalidf("app.source.git.repo", "must not be empty")
		}
		if origin.Git.GetRef() == "" {
			return invalidf("app.source.git.ref", "must not be empty")
		}
	case *specv1.Source_Image:
		if origin.Image.GetRef() == "" {
			return invalidf("app.source.image.ref", "must not be empty")
		}
	case *specv1.Source_Upload:
		if origin.Upload.GetId() == "" {
			return invalidf("app.source.upload.id", "must not be empty")
		}
	default:
		return invalidf("app.source", "one of git, image or upload is required")
	}
	return nil
}

// ValidateTask 校验 TaskSpec。
func ValidateTask(s *specv1.TaskSpec) error {
	if s == nil {
		return invalidf("task", "spec is nil")
	}
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("task.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if s.GetTask().GetId() == "" {
		return invalidf("task.id", "must not be empty")
	}
	if s.GetTask().GetProject() == "" {
		return invalidf("task.project", "must not be empty")
	}
	if err := ValidateProcess("task.process", s.GetProcess()); err != nil {
		return err
	}
	// ADR-0018：TTL 上限 86400s。
	if ttl := s.GetTtlSeconds(); ttl < 0 || ttl > 86400 {
		return invalidf("task.ttl_seconds", "must be within [0, 86400]")
	}
	if s.GetDesiredConcurrency() < 0 {
		return invalidf("task.desired_concurrency", "must not be negative")
	}
	return nil
}

// ValidateDatabase 校验 DatabaseSpec。
func ValidateDatabase(s *specv1.DatabaseSpec) error {
	if s == nil {
		return invalidf("database", "spec is nil")
	}
	if v := s.GetSchemaVersion(); v <= 0 || v > SchemaVersion {
		return invalidf("database.schema_version", "unsupported schema version %d (current %d)", v, SchemaVersion)
	}
	if s.GetDatabase().GetId() == "" {
		return invalidf("database.id", "must not be empty")
	}
	if s.GetEngine() == "" {
		return invalidf("database.engine", "must not be empty")
	}
	if s.GetCredentialsRef() == "" {
		return invalidf("database.credentials_ref", "must reference a project secret")
	}
	return nil
}

// IsNetworkGroupRef 报告网络附件是否 Task Network Group 跨挂形态
// （taskGroup:<name>）。
func IsNetworkGroupRef(net string) bool {
	return strings.HasPrefix(net, "taskGroup:")
}

// NetworkGroupName 剥离前缀返回组名。
func NetworkGroupName(net string) string {
	return strings.TrimPrefix(net, "taskGroup:")
}

package apitest_test

// REST 400 契约新钉（P4-1 批 1）：CreateProjectRequest.name 的手写校验
// 退役为 protovalidate CEL 注解后，validate 拒绝经 assembly 信封包装
// （newValidateInterceptor）统一入 E_INVALID_ARGUMENT 信封——REST 面必须
// 与手写时代同 wire 形态：HTTP 400 + 信封 code/message/suggestion/docs +
// x-fleetly-error-id 头。文案逐字节钉死（对齐真源见 entityid 的 descriptor
// 对齐测试）；gRPC 面同步断言 InvalidArgument + 信封 detail 可还原。

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/errcode"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// dnsLabelMessage 是 string.dns_label 规则的注册文案（与 structure.proto
// 注解、mapping.go 手写时代文案三方同源；此处整段钉死防漂移）。
const dnsLabelMessage = `name: must be a dns label (lowercase letters, digits, hyphens; start and end alphanumeric, e.g. "web" or "nightly-backup")`

// requireEnvelope 解码 REST 错误体并钉信封四字段（逐字节）：code/message/
// suggestion/docs。suggestion/docs 以注册表为源（E_INVALID_ARGUMENT 原文）。
func requireEnvelope(t *testing.T, body string, message string) {
	t.Helper()
	var envelope struct {
		Code       string            `json:"code"`
		Message    string            `json:"message"`
		Suggestion string            `json:"suggestion"`
		Docs       string            `json:"docs"`
		Context    map[string]string `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &envelope), "error body must be an apperr envelope: %s", body)
	suggestion, _ := errcode.Get("E_INVALID_ARGUMENT")
	assert.Equal(t, "E_INVALID_ARGUMENT", envelope.Code)
	assert.Equal(t, message, envelope.Message)
	assert.Equal(t, suggestion.Suggestion, envelope.Suggestion)
	assert.Equal(t, errcode.DocsURL("E_INVALID_ARGUMENT"), envelope.Docs)
	assert.NotEmpty(t, envelope.Context["error_id"], "信封 context 必须携带 error_id")
}

// TestRESTCreateProjectValidationReturns400：空名与坏形态两态的 REST 400
// 契约（信封 + error_id 头），gRPC 面同请求同判（InvalidArgument + 信封
// detail 还原，message 逐字节一致）。
func TestRESTCreateProjectValidationReturns400(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	rest := h.REST()
	projects := structurev1.NewProjectsServiceClient(h.Conn)

	// 空名（显式 ""）：required 规则拒绝，REST 400 + 信封原文。
	rec := rest.Post("/v1/projects", `{"name":""}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"code":"E_INVALID_ARGUMENT"`)
	requireEnvelope(t, rec.Body.String(), "name: must not be empty")
	assert.NotEmpty(t, rec.Header().Get(apperrstatus.ErrorIDHeader), "x-fleetly-error-id 头必须在场")
	// gRPC 面：InvalidArgument 且信封 detail 可还原（message 逐字节一致）。
	_, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: ""})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	e, ok := apperrstatus.FromGRPCStatus(status.Convert(err))
	require.True(t, ok, "bare InvalidArgument must carry the apperr envelope after assembly wrapping")
	assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
	assert.Equal(t, "name: must not be empty", e.Message())

	// 坏形态：shape（dns label）规则拒绝，文案逐字节。
	rec = rest.Post("/v1/projects", `{"name":"Bad_Name"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	requireEnvelope(t, rec.Body.String(), dnsLabelMessage)
	assert.NotEmpty(t, rec.Header().Get(apperrstatus.ErrorIDHeader))
	// gRPC 面：同判。
	_, err = projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "Bad_Name"})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	e, ok = apperrstatus.FromGRPCStatus(status.Convert(err))
	require.True(t, ok)
	assert.Equal(t, "E_INVALID_ARGUMENT", e.Code())
	assert.Equal(t, dnsLabelMessage, e.Message())
}

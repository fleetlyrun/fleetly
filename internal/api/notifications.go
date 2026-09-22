package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/notify"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// NotificationsService 实现 server.v1.NotificationsService（E6 观测专项
// 设计 §5，W5-S4）：订阅端点 CRUD + 投递台账读面 + TestWebhook。
//
// secret 纪律（§5.1 + state-model §2.9）：密钥平台生成（32B base64），
// envelope 加密落库（加密边界在本服务——box.Encrypt/Decrypt，与 s3 设置
// 同型）；明文只在 Create/Rotate 响应一次性出现；读面只出指纹（sha256
// 前 8 hex，s3 卡同口径）。URL/name 形状校验在本面做 400 形状门（退化信
// 封——形状违约不走注册表码），patterns 白名单校验在 state 层（422
// E_WEBHOOK_PATTERN_INVALID 原样透传）。投递本身由 internal/notify duty
// 承载——本服务是纯受理/投影面。
//
// scope：读 = read（台账与端点是事实面，指纹非凭据）；写 = admin（端点
// 是平台级凭据面——创建/轮换/删除/测试与 s3 设置同级）。
type NotificationsService struct {
	serverv1.UnimplementedNotificationsServiceServer
	st  *state.Store
	box *secrets.Box
}

// NewNotificationsService 构造 NotificationsService（box 供 secret 加解密
// ——nil 不可：无密钥面即无端点面）。
func NewNotificationsService(st *state.Store, box *secrets.Box) *NotificationsService {
	return &NotificationsService{st: st, box: box}
}

// ListWebhookEndpoints 端点清单（无敏感投影）。
func (s *NotificationsService) ListWebhookEndpoints(ctx context.Context, _ *serverv1.ListWebhookEndpointsRequest) (*serverv1.ListWebhookEndpointsResponse, error) {
	rows, err := s.st.ListWebhookEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.WebhookEndpointView, 0, len(rows))
	for _, e := range rows {
		out = append(out, webhookEndpointView(e))
	}
	return &serverv1.ListWebhookEndpointsResponse{Endpoints: out}, nil
}

// GetWebhookEndpoint 单端点视图（不存在 → E_WEBHOOK_NOT_FOUND 404）。
func (s *NotificationsService) GetWebhookEndpoint(ctx context.Context, req *serverv1.GetWebhookEndpointRequest) (*serverv1.GetWebhookEndpointResponse, error) {
	e, err := s.st.GetWebhookEndpoint(ctx, req.GetId())
	if err != nil {
		return nil, mapWebhookErr(err, req.GetId())
	}
	return &serverv1.GetWebhookEndpointResponse{Endpoint: webhookEndpointView(e)}, nil
}

// CreateWebhookEndpoint 创建端点：密钥平台生成 + envelope 加密 + 指纹，
// **明文仅本次响应可见**。
func (s *NotificationsService) CreateWebhookEndpoint(ctx context.Context, req *serverv1.CreateWebhookEndpointRequest) (*serverv1.CreateWebhookEndpointResponse, error) {
	if err := shapeWebhookName(req.GetName()); err != nil {
		return nil, err
	}
	if err := shapeWebhookURL(req.GetUrl()); err != nil {
		return nil, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = req.GetEnabled()
	}
	plaintext, err := generateWebhookSecret()
	if err != nil {
		return nil, err
	}
	cipher, err := s.box.Encrypt([]byte(plaintext))
	if err != nil {
		return nil, errors.New("webhook secret encryption failed (platform key error): " + err.Error())
	}
	e, err := s.st.CreateWebhookEndpoint(ctx, state.WebhookEndpointWrite{
		Name:              req.GetName(),
		URL:               req.GetUrl(),
		SecretCipher:      string(cipher),
		SecretFingerprint: secretFingerprint([]byte(plaintext)),
		EventPatterns:     req.GetEventPatterns(),
		Enabled:           enabled,
		ActorTokenID:      callerTokenID(ctx),
	})
	if err != nil {
		return nil, mapWebhookErr(err, req.GetName())
	}
	return &serverv1.CreateWebhookEndpointResponse{
		Endpoint: webhookEndpointView(e),
		Secret:   plaintext, // 仅此一次
	}, nil
}

// UpdateWebhookEndpoint 部分更新（optional 字段语义——未提供不变）。
func (s *NotificationsService) UpdateWebhookEndpoint(ctx context.Context, req *serverv1.UpdateWebhookEndpointRequest) (*serverv1.UpdateWebhookEndpointResponse, error) {
	u := state.WebhookEndpointUpdate{ActorTokenID: callerTokenID(ctx)}
	if req.Name != nil {
		if err := shapeWebhookName(req.GetName()); err != nil {
			return nil, err
		}
		name := req.GetName()
		u.Name = &name
	}
	if req.Url != nil {
		if err := shapeWebhookURL(req.GetUrl()); err != nil {
			return nil, err
		}
		url := req.GetUrl()
		u.URL = &url
	}
	if len(req.GetEventPatterns()) > 0 {
		u.EventPatterns = req.GetEventPatterns()
	}
	if req.Enabled != nil {
		enabled := req.GetEnabled()
		u.Enabled = &enabled
	}
	e, err := s.st.UpdateWebhookEndpoint(ctx, req.GetId(), u)
	if err != nil {
		return nil, mapWebhookErr(err, req.GetId())
	}
	return &serverv1.UpdateWebhookEndpointResponse{Endpoint: webhookEndpointView(e)}, nil
}

// DeleteWebhookEndpoint 删除端点（台账行同事务清理）。
func (s *NotificationsService) DeleteWebhookEndpoint(ctx context.Context, req *serverv1.DeleteWebhookEndpointRequest) (*serverv1.DeleteWebhookEndpointResponse, error) {
	if err := s.st.DeleteWebhookEndpoint(ctx, req.GetId(), "human", callerTokenID(ctx)); err != nil {
		return nil, mapWebhookErr(err, req.GetId())
	}
	return &serverv1.DeleteWebhookEndpointResponse{Id: req.GetId()}, nil
}

// RotateWebhookSecret 轮换签名密钥：新密钥**明文仅本次响应可见**。
func (s *NotificationsService) RotateWebhookSecret(ctx context.Context, req *serverv1.RotateWebhookSecretRequest) (*serverv1.RotateWebhookSecretResponse, error) {
	plaintext, err := generateWebhookSecret()
	if err != nil {
		return nil, err
	}
	cipherBytes, err := s.box.Encrypt([]byte(plaintext))
	if err != nil {
		return nil, errors.New("webhook secret encryption failed (platform key error): " + err.Error())
	}
	fp := secretFingerprint([]byte(plaintext))
	cipher := string(cipherBytes)
	if _, err := s.st.UpdateWebhookEndpoint(ctx, req.GetId(), state.WebhookEndpointUpdate{
		SecretCipher:      &cipher,
		SecretFingerprint: &fp,
		ActorTokenID:      callerTokenID(ctx),
	}); err != nil {
		return nil, mapWebhookErr(err, req.GetId())
	}
	return &serverv1.RotateWebhookSecretResponse{Secret: plaintext, SecretFingerprint: fp}, nil
}

// TestWebhook 发送 type=test 载荷（设计 §5.2）：库内解密 + 同链路签名
// POST，同步返回单次投递结论；不落台账。
func (s *NotificationsService) TestWebhook(ctx context.Context, req *serverv1.TestWebhookRequest) (*serverv1.TestWebhookResponse, error) {
	e, err := s.st.GetWebhookEndpoint(ctx, req.GetId())
	if err != nil {
		return nil, mapWebhookErr(err, req.GetId())
	}
	secret, err := s.box.Decrypt([]byte(e.SecretCipher))
	if err != nil {
		return nil, apperr.New("E_WEBHOOK_NOT_FOUND",
			"webhook endpoint %s secret cannot be decrypted (platform key mismatch): rotate the secret to restore testability", req.GetId())
	}
	ok, code, errText := notify.SendTestPayload(ctx, e.URL, secret, e.ID)
	return &serverv1.TestWebhookResponse{Ok: ok, StatusCode: int32(code), Error: errText}, nil //nolint:gosec // G115：HTTP 状态码量级极小
}

// ListWebhookDeliveries 投递台账（按端点/状态过滤，最新在前）。
func (s *NotificationsService) ListWebhookDeliveries(ctx context.Context, req *serverv1.ListWebhookDeliveriesRequest) (*serverv1.ListWebhookDeliveriesResponse, error) {
	rows, err := s.st.ListWebhookDeliveries(ctx, req.GetEndpointId(), req.GetStatus(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.WebhookDeliveryView, 0, len(rows))
	for _, d := range rows {
		v := &serverv1.WebhookDeliveryView{
			Id:         d.ID,
			EventSeq:   d.EventSeq,
			EndpointId: d.EndpointID,
			Status:     d.Status,
			Attempts:   int32(d.Attempts), //nolint:gosec // G115：尝试次数量级极小
			LastError:  d.LastError,
		}
		if d.ResponseCode != nil {
			code := int32(*d.ResponseCode) //nolint:gosec // G115：HTTP 状态码量级极小
			v.ResponseCode = &code
		}
		if d.NextRetryAt != nil {
			v.NextRetryAt = tstamp(*d.NextRetryAt)
		}
		v.CreatedAt = tstamp(d.CreatedAt)
		v.UpdatedAt = tstamp(d.UpdatedAt)
		out = append(out, v)
	}
	return &serverv1.ListWebhookDeliveriesResponse{Deliveries: out}, nil
}

// webhookEndpointView 构造端点无敏感投影。
func webhookEndpointView(e state.WebhookEndpoint) *serverv1.WebhookEndpointView {
	return &serverv1.WebhookEndpointView{
		Id:                e.ID,
		Name:              e.Name,
		Url:               e.URL,
		EventPatterns:     e.EventPatterns,
		Enabled:           e.Enabled,
		SecretFingerprint: e.SecretFingerprint,
		CreatedAt:         tstamp(e.CreatedAt),
		UpdatedAt:         tstamp(e.UpdatedAt),
	}
}

// mapWebhookErr 把 state 哨兵映射为注册表码信封（NotFound 404 /
// NameConflict 409；E_WEBHOOK_PATTERN_INVALID apperr 原样透传）。
func mapWebhookErr(err error, ref string) error {
	switch {
	case errors.Is(err, state.ErrWebhookNotFound):
		return apperr.New("E_WEBHOOK_NOT_FOUND",
			"webhook endpoint not found: %s", ref).
			WithContext("endpoint", ref)
	case errors.Is(err, state.ErrWebhookNameConflict):
		return apperr.New("E_WEBHOOK_NAME_CONFLICT",
			"a webhook endpoint named %q already exists (names are unique)", ref).
			WithContext("name", ref)
	default:
		return err
	}
}

// shapeWebhookName / shapeWebhookURL 是 400 形状门（state 层白名单是
// 防御性第二道闸——本面先拦，形状违约走退化信封不走注册表码）。
func shapeWebhookName(name string) error {
	if err := state.ValidateWebhookName(name); err != nil {
		return statusInvalidArgument(err.Error())
	}
	return nil
}

func shapeWebhookURL(raw string) error {
	if err := state.ValidateWebhookURL(raw); err != nil {
		return statusInvalidArgument(err.Error())
	}
	return nil
}

// generateWebhookSecret 生成签名密钥明文（crypto/rand 32 字节 → base64
// ——43 字符无填充；HMAC 密钥熵源，base64 只是传输形态）。
func generateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("generate webhook secret: " + err.Error())
	}
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

var _ serverv1.NotificationsServiceServer = (*NotificationsService)(nil)

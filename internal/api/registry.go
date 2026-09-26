package api

// 平台 registry 凭证设置面（IMPL-T1-2/DT-2，设计冻结 §2）：SystemService
// 增两 RPC——GetRegistrySettings/UpdateRegistrySettings（platform_settings
// 的 registry.* 设置，S3/ACME 设置面同型先例）。整体 admin scope +
// requirePlatformWriteFace（平台凭据面，W2-S4 收口口径）。
//
// 凭证纪律：密码明文只在写入请求与服务端内存瞬时出现（envelope 加密落库，
// 调用方加密边界——state 层存密文不解释）；读面只回指纹；错误/审计/事件
// 零凭据材料（事件 registry.updated 只带 host 与指纹）。password 留空 =
// 保留已存密码（ACME api_token 同款先例）；host 留空 = 清除全部设置。

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetRegistrySettings 平台 registry 设置只读面：host/用户名 + 密码指纹
//（明文零出现——指纹在保存时与密文同事务落库）。
func (s *SystemService) GetRegistrySettings(ctx context.Context, _ *serverv1.GetRegistrySettingsRequest) (*serverv1.GetRegistrySettingsResponse, error) {
	// 平台面敏感读门（W2-S4）：registry 设置 = 平台凭据面（S3/ACME 同门）。
	if err := requirePlatformWriteFace(ctx, s.st); err != nil {
		return nil, err
	}
	in, err := s.st.LoadRegistrySettings(ctx)
	if err != nil {
		return nil, err
	}
	return &serverv1.GetRegistrySettingsResponse{Settings: registrySettingsView(in)}, nil
}

// UpdateRegistrySettings 保存平台 registry 设置（host + 用户名 + 密码）。
// host 空 = 清除（四键齐清）；password 留空 = 保留已存密码（密文原样
// 沿用——不重复加密不可读密文）；password 非空 = envelope 加密 + 指纹同
// 事务落库。保存 + 审计 registry.updated + 事件 registry.updated 同事务
//（凭据材料零出现）。
func (s *SystemService) UpdateRegistrySettings(ctx context.Context, req *serverv1.UpdateRegistrySettingsRequest) (*serverv1.UpdateRegistrySettingsResponse, error) {
	// 平台面写门（W2-S4）：registry 设置保存 = 平台管理员。
	if err := requirePlatformWriteFace(ctx, s.st); err != nil {
		return nil, err
	}
	rawHost := strings.TrimSpace(req.GetHost())
	host := state.NormalizeRegistryHost(rawHost)
	if rawHost != "" && (host == "" || strings.ContainsAny(host, " \t/")) {
		return nil, statusInvalidArgument(fmt.Sprintf(
			"registry host %s is not a bare host[:port] form (omit the scheme and any path; docker.io is normalized to registry-1.docker.io)",
			strconv.Quote(rawHost)))
	}
	in := state.RegistrySettings{Host: host, Username: strings.TrimSpace(req.GetUsername())}
	switch {
	case host == "":
		in = state.RegistrySettings{} // 清除形态：四键齐清
	case req.GetPassword() != "":
		if s.box == nil {
			return nil, status.Error(codes.Unavailable, "secrets box unavailable (not assembled)")
		}
		ciphertext, err := s.box.Encrypt([]byte(req.GetPassword()))
		if err != nil {
			return nil, err
		}
		in.PasswordCipher = string(ciphertext)
		in.PasswordFingerprint = secretFingerprint([]byte(req.GetPassword()))
	default:
		// 留空 = 保留已存密码（ACME api_token 同款先例）。
		stored, err := s.st.LoadRegistrySettings(ctx)
		if err != nil {
			return nil, err
		}
		in.PasswordCipher = stored.PasswordCipher
		in.PasswordFingerprint = stored.PasswordFingerprint
	}
	opts := state.RegistrySaveOptions{Actor: "human"}
	if p, ok := PrincipalFromContext(ctx); ok {
		opts.ActorTokenID = p.TokenID
	}
	if err := s.st.SaveRegistrySettings(ctx, in, opts); err != nil {
		return nil, err
	}
	return &serverv1.UpdateRegistrySettingsResponse{Settings: registrySettingsView(in)}, nil
}

// registrySettingsView 把设置构造为脱敏读面投影（密码指纹已随保存落库，
// 读面无需解密——明文不出服务端边界）。
func registrySettingsView(in state.RegistrySettings) *serverv1.RegistrySettingsView {
	return &serverv1.RegistrySettingsView{
		Host:                in.Host,
		Username:            in.Username,
		PasswordFingerprint: in.PasswordFingerprint,
		UpdatedAt:           tstamp(in.UpdatedAt),
	}
}

package cmd

// s3 命令渲染口径的单测（v0.2.x 收尾票，2026-09-21）：公网域名行取服务端
// 派生实值（S3SettingsView.public_domain），旧 daemon / 无 base_domain 形态
// 退回字面 s3.<base>——回退与 v0.2.0 前的显示逐字一致（诚实降级不臆造）。

import (
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
)

func TestS3PublicDomainLabel(t *testing.T) {
	cases := []struct {
		name     string
		settings *serverv1.S3SettingsView
		want     string
	}{
		{
			name:     "server-derived actual domain",
			settings: &serverv1.S3SettingsView{PublicExposed: true, PublicDomain: "s3.example.test"},
			want:     "s3.example.test",
		},
		{
			name:     "legacy daemon without the field falls back to the literal form",
			settings: &serverv1.S3SettingsView{PublicExposed: true},
			want:     "s3.<base>",
		},
		{
			name:     "empty domain on a zero view falls back to the literal form",
			settings: &serverv1.S3SettingsView{},
			want:     "s3.<base>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s3PublicDomainLabel(tc.settings); got != tc.want {
				t.Fatalf("s3PublicDomainLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

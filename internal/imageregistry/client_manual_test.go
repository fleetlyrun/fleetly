//go:build manual

package imageregistry

// IMPL-T1-2/DT-2 的真实 registry 匿名解析探针（默认不跑；需要出网到
// registry-1.docker.io 与 ghcr.io）：
//
//	FLEETLY_MANUAL_REGISTRY=1 go test -tags manual ./internal/imageregistry -run TestManualResolve -v
//
// 语义：公共镜像零预拉部署的解析腿真实 token flow（Docker Hub 与 ghcr 的
// 匿名 Bearer 挑战 → token 服务 → manifest digest）。失败即镜像/网络环境
// 问题，不视为实现缺陷（全形态 httptest 单测已覆盖）；本探针把「真实
// registry 兼容性」钉成可复跑的一手证据。

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestManualResolvePublicRegistryImages(t *testing.T) {
	if os.Getenv("FLEETLY_MANUAL_REGISTRY") != "1" {
		t.Skip("set FLEETLY_MANUAL_REGISTRY=1 to probe real registries")
	}
	c := NewClient()
	for _, raw := range []string{
		"alpine:3.19",                       // Docker Hub（匿名 Bearer token flow + library/ 补齐）
		"ghcr.io/linuxserver/sonarr:latest", // ghcr（公共镜像匿名 flow）
	} {
		ref, err := Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		digest, err := c.Resolve(ctx, ref, nil)
		cancel()
		if err != nil {
			t.Fatalf("resolve %s: %v", raw, err)
		}
		if !digestRe.MatchString(digest) {
			t.Fatalf("resolve %s: digest %q is not a manifest digest", raw, digest)
		}
		t.Logf("%s -> %s", raw, digest)
	}
}

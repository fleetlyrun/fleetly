package guards

// 守卫 A 的 gRPC 镜像（批 0.5 裁决 C4，2026-10-01）：genproto 服务面 ↔
// 注册清单一一对应。生产面以生成物为单源（*_grpc.pb.go 每个服务一个
// Register*Server 函数声明）；消费面扫 internal/api 与 internal/assembly
// 的 Register*Server 引用（register.go 的 RegisterAll 清单 + assembly 对
// SystemService 的直挂）。两侧 go/parser 提取真实代码符号（AST 级，注释
// 免疫）。"proto 服务加了、忘进 RegisterAll"（identity REST 六服务整面
// 404 的同型缺口，P1-13/A-9 已付过学费）在此红并列缺口服务名；反向死条
// 目同样红。豁免表带理由、双向保鲜。AssertAllRegisteredHavePolicy 是单向
// 的（注册了才查策略），漏注册对其不可见——本守卫补的就是这一向。

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// grpcServiceSurface 枚举 genproto 下全部 gRPC 服务名（*_grpc.pb.go 的
// Register*Server 函数声明，生成物单源）。
func grpcServiceSurface(t *testing.T) map[string]bool {
	t.Helper()
	services := map[string]bool{}
	err := walkGoDecls(t, filepath.Join("genproto"), "_grpc.pb.go", false, func(name string) {
		if svc, ok := strings.CutPrefix(name, "Register"); ok {
			if svc, ok := strings.CutSuffix(svc, "Server"); ok {
				services[svc] = true
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatal("grpc service surface scan found no *_grpc.pb.go registrations — guard is blind, fix the scan")
	}
	return services
}

// grpcRegisteredSurface 枚举 internal/api 与 internal/assembly 非生成物、
// 非测试 .go 里引用的 Register*Server 服务名（RegisterAll 清单成员与
// assembly 的直挂）。
func grpcRegisteredSurface(t *testing.T) map[string]bool {
	t.Helper()
	services := map[string]bool{}
	collect := func(name string) {
		if svc, ok := strings.CutPrefix(name, "Register"); ok {
			if svc, ok := strings.CutSuffix(svc, "Server"); ok {
				services[svc] = true
			}
		}
	}
	for _, root := range []string{filepath.Join("internal", "api"), filepath.Join("internal", "assembly")} {
		if err := walkGoDecls(t, root, ".go", true, collect); err != nil {
			t.Fatal(err)
		}
	}
	if len(services) == 0 {
		t.Fatal("registration surface scan found no Register*Server references — guard is blind, fix the scan")
	}
	return services
}

// grpcExemptions 是服务面 → 注册面差异的豁免表（服务名 → 理由）。当前
// 无例外：全部 proto 服务都应经 RegisterAll 或 assembly 直挂注册。豁免
// 条目不再命中（差异消失）即红。
var grpcExemptions = map[string]string{}

// TestGRPCRegistrationMatchesGenprotoSurface：服务面与注册面双向对账。
func TestGRPCRegistrationMatchesGenprotoSurface(t *testing.T) {
	declared := grpcServiceSurface(t)
	registered := grpcRegisteredSurface(t)
	usedExemptions := map[string]bool{}

	var missing []string
	for svc := range declared {
		if registered[svc] {
			continue
		}
		if reason, ok := grpcExemptions[svc]; ok && reason != "" {
			usedExemptions[svc] = true
			continue
		}
		missing = append(missing, svc)
	}
	sort.Strings(missing)
	for _, svc := range missing {
		t.Errorf("service %s is declared by genproto (Register%sServer in *_grpc.pb.go) but is never registered — a proto service omitted from RegisterAll serves Unimplemented at runtime; add it to fleetlygrpc RegisterAll (or exempt with a reason)", svc, svc)
	}

	// 反向：注册面出现服务面不存在的服务 = 死条目（生成物已消失）。
	var dead []string
	for svc := range registered {
		if declared[svc] {
			continue
		}
		if reason, ok := grpcExemptions[svc]; ok && reason != "" {
			usedExemptions[svc] = true
			continue
		}
		dead = append(dead, svc)
	}
	sort.Strings(dead)
	for _, svc := range dead {
		t.Errorf("internal registers %s but genproto declares no such service — remove the stale registration or exempt with a reason", svc)
	}

	// 双向保鲜：豁免不再命中（差异消失）即红，清走死条目。
	for svc, reason := range grpcExemptions {
		if reason == "" {
			t.Errorf("grpc exemption %s must carry a reason", svc)
		}
		if !usedExemptions[svc] {
			t.Errorf("grpc exemption %s no longer matches a real gap; remove the entry (declared=%v registered=%v)", svc, declared[svc], registered[svc])
		}
	}

	// 防呆：双向非空，防 WalkDir 根路径失配后空集静默通过。
	if len(declared) < 20 || len(registered) < 20 {
		t.Fatalf("surfaces suspiciously small: declared=%d registered=%d — guard is blind, fix the scan", len(declared), len(registered))
	}
}

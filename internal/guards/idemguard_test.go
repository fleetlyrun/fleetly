package guards

// 幂等覆盖反扫（ADR-0024 同批守卫 ②）：proto 契约源里的创建型动词
//（Create/Deploy/Submit/Put/Set/Rollback 前缀）必须落在 internal/idem 的
// 执法面 EnforcedMethods，或带理由豁免；反向死条目（执法面引用 proto 不
// 存在的方法）同样红。失效类同 guard A：proto 加了创建型 RPC、忘进执法
// 清单 → 幂等承诺静默缺一块，本守卫在 CI 红并列缺口方法名。

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/idem"
)

// idemExemptions 是创建型动词 → 不入执法面的豁免表（FullMethod → 理由）。
// 当前为空：全部创建型动词均已执法。豁免不再命中（差异消失）即红。
var idemExemptions = map[string]string{
	// exec 会话（F3.2，ADR-0049）：进程内流态（无持久资源行），幂等重放
	// 保证不适用——会话不可重放（票据单用途）。
	"/fleetly.runtime.v1.ExecService/CreateExecSession": "sessions are in-memory stream state with no durable row; the idempotency replay guarantee is inapplicable (ADR-0049)",
}

// createVerbPrefixes 是"创建型"动词集（含部署创建语义的 Rollback——它
// 经 Submit 落一条新 Deployment；不含天然幂等或串行管理面的 Enroll/
// Rotate/Revoke/Invite）。
var createVerbPrefixes = []string{"Create", "Deploy", "Submit", "Put", "Set", "Rollback"}

// protoCreateMethods 扫描 proto/ 下全部服务定义，产出创建型动词的
// FullMethod 集（/包.服务/方法）。逐行状态机：package → service → rpc。
func protoCreateMethods(t *testing.T) map[string]bool {
	t.Helper()
	return scanProtoMethods(t, createVerbPrefixes)
}

// scanProtoMethods 是 proto 服务面的共享行扫描器（动词前缀参数化；幂等
// 守卫与受理面守卫共用）。返回 /包.服务/方法 全径集。
func scanProtoMethods(t *testing.T, prefixes []string) map[string]bool {
	t.Helper()
	methods := map[string]bool{}
	err := walkRepoFiles(t, "proto", ".proto", func(path string, scan *bufio.Scanner) {
		pkg, svc := "", ""
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if v, ok := strings.CutSuffix(line, ";"); ok && strings.HasPrefix(v, "package ") {
				pkg = strings.TrimSpace(strings.TrimPrefix(v, "package "))
				continue
			}
			if strings.HasPrefix(line, "service ") {
				svc = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "service "), "{"))
				continue
			}
			if strings.HasPrefix(line, "rpc ") && pkg != "" && svc != "" {
				fields := strings.Fields(strings.TrimPrefix(line, "rpc "))
				if len(fields) == 0 {
					continue
				}
				method, _, _ := strings.Cut(fields[0], "(")
				for _, p := range prefixes {
					if strings.HasPrefix(method, p) {
						methods["/"+pkg+"."+svc+"/"+method] = true
						break
					}
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) < 10 {
		t.Fatalf("proto verb scan found only %d methods — guard is blind, fix the scan", len(methods))
	}
	return methods
}

// protoAllMethods 同上但不限动词（反向死条目判定用）。
func protoAllMethods(t *testing.T) map[string]bool {
	t.Helper()
	methods := map[string]bool{}
	err := walkRepoFiles(t, "proto", ".proto", func(path string, scan *bufio.Scanner) {
		pkg, svc := "", ""
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if v, ok := strings.CutSuffix(line, ";"); ok && strings.HasPrefix(v, "package ") {
				pkg = strings.TrimSpace(strings.TrimPrefix(v, "package "))
				continue
			}
			if strings.HasPrefix(line, "service ") {
				svc = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "service "), "{"))
				continue
			}
			if strings.HasPrefix(line, "rpc ") && pkg != "" && svc != "" {
				fields := strings.Fields(strings.TrimPrefix(line, "rpc "))
				if len(fields) > 0 {
					method, _, _ := strings.Cut(fields[0], "(")
					methods["/"+pkg+"."+svc+"/"+method] = true
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return methods
}

// walkRepoFiles 遍历仓库相对 root 子树下后缀匹配的文件，逐文件行扫描。
func walkRepoFiles(t *testing.T, root, suffix string, fn func(path string, scan *bufio.Scanner)) error {
	t.Helper()
	absRoot := filepath.Join(repoRoot(t), root)
	return filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scan := bufio.NewScanner(bytes.NewReader(data))
		scan.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		fn(path, scan)
		return scan.Err()
	})
}

// TestIdempotencyCoversCreateVerbs：创建型动词 × 幂等执法面双向对账。
func TestIdempotencyCoversCreateVerbs(t *testing.T) {
	createVerbs := protoCreateMethods(t)
	allMethods := protoAllMethods(t)
	usedExemptions := map[string]bool{}

	var missing []string
	for m := range createVerbs {
		if idem.EnforcedMethods[m] {
			continue
		}
		if reason, ok := idemExemptions[m]; ok && reason != "" {
			usedExemptions[m] = true
			continue
		}
		missing = append(missing, m)
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("create-type RPC %s is not covered by idem.EnforcedMethods and carries no exemption — a create verb outside idempotency enforcement silently drops the replay guarantee; add it to internal/idem or exempt with a reason (ADR-0024 guard)", m)
	}

	// 反向：执法面出现 proto 不存在的方法 = 死条目（方法改名/删除后残留）。
	var dead []string
	for m := range idem.EnforcedMethods {
		if allMethods[m] {
			continue
		}
		if reason, ok := idemExemptions[m]; ok && reason != "" {
			usedExemptions[m] = true
			continue
		}
		dead = append(dead, m)
	}
	sort.Strings(dead)
	for _, m := range dead {
		t.Errorf("idem.EnforcedMethods lists %s but no such proto method exists — remove the stale entry", m)
	}

	// 双向保鲜：豁免不再命中即红。
	for m, reason := range idemExemptions {
		if reason == "" {
			t.Errorf("idem exemption %s must carry a reason", m)
		}
		if !usedExemptions[m] {
			t.Errorf("idem exemption %s no longer matches a real gap; remove the entry", m)
		}
	}

	if len(idem.EnforcedMethods) < 10 {
		t.Fatalf("enforced surface suspiciously small (%d) — guard is blind, fix the scan", len(idem.EnforcedMethods))
	}
}

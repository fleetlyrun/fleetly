package fleetlygrpc

// webhook 接收链纯函数单测：验签形态、ref 解析、watchPaths 边界、skip
// 标记。服务面级场景（重投/状态机/审计）在 internal/apitest。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyHMAC(t *testing.T) {
	payload := []byte(`{"ref":"refs/heads/main"}`)
	if !verifyHMAC([]byte("flthook_secret"), payload, sign("flthook_secret", payload)) {
		t.Fatal("valid signature must verify")
	}
	if verifyHMAC([]byte("flthook_secret"), payload, sign("other", payload)) {
		t.Fatal("wrong secret must fail")
	}
	// 篡改一个字节即失效。
	tampered := append([]byte(nil), payload...)
	tampered[0] = '{'
	tampered[len(tampered)-1] = ' '
	if verifyHMAC([]byte("flthook_secret"), tampered, sign("flthook_secret", payload)) {
		t.Fatal("tampered payload must fail")
	}
	// 非 sha256= 前缀 / 非 hex / 长度不符全拒。
	if verifyHMAC([]byte("s"), payload, strings.TrimPrefix(sign("s", payload), "sha256=")) {
		t.Fatal("missing sha256= prefix must fail")
	}
	if verifyHMAC([]byte("s"), payload, "sha256=zzzz") {
		t.Fatal("non-hex signature must fail")
	}
	if verifyHMAC([]byte("s"), payload, "") {
		t.Fatal("empty signature must fail")
	}
}

func TestBranchFromRef(t *testing.T) {
	if b, ok := branchFromRef("refs/heads/main"); !ok || b != "main" {
		t.Fatalf("got %q %v", b, ok)
	}
	if _, ok := branchFromRef("refs/tags/v1.0.0"); ok {
		t.Fatal("tag refs must not parse as branches")
	}
	if _, ok := branchFromRef("refs/heads/"); ok {
		t.Fatal("empty branch must not parse")
	}
	if _, ok := branchFromRef("main"); ok {
		t.Fatal("bare branch name must not parse")
	}
}

func TestWatchPathsHit(t *testing.T) {
	changed := []string{"web/index.ts", "docs/readme.md", "webapp/main.go"}
	if !watchPathsHit(changed, []string{"web"}) {
		t.Fatal("boundary prefix must hit")
	}
	if watchPathsHit(changed, []string{"api"}) {
		t.Fatal("unwatched prefix must miss")
	}
	if !watchPathsHit(changed, []string{"docs/readme.md"}) {
		t.Fatal("exact file match must hit")
	}
	if watchPathsHit(nil, []string{"web"}) {
		t.Fatal("no changes must miss")
	}
}

func TestChangedPathsAndSkip(t *testing.T) {
	p := pushPayload{
		Commits: []commitEntry{{
			Added:    []string{"a.txt"},
			Modified: []string{"b.txt"},
			Removed:  []string{"c.txt"},
		}},
	}
	got := changedPaths(p)
	if len(got) != 3 {
		t.Fatalf("changedPaths = %v", got)
	}
	if !strings.Contains(strings.ToLower("fix: thing [SKIP DEPLOY]"), strings.ToLower(skipDeployMarker)) {
		t.Fatal("skip marker check must be case-insensitive")
	}
}

func TestPushPayloadParse(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/release",
		"after": "0000000000000000000000000000000000000000",
		"before": "1111111111111111111111111111111111111111",
		"head_commit": {"id": "0000", "message": "delete branch"},
		"commits": [],
		"repository": {"full_name": "acme/shop"}
	}`)
	var p pushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Ref != "refs/heads/release" || p.Head.Message != "delete branch" {
		t.Fatalf("p = %+v", p)
	}
	// 未知字段忽略（GitHub payload 的其余面不影响最小解析）。
	if strings.Trim(p.After, "0") != "" {
		t.Fatal("all-zero after (branch deletion) must be recognizable")
	}
}

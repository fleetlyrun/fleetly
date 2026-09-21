package compose

// fleetly.s3 label 契约测试（E3-4）：登记后保留命名空间放行、值契约
// fail-loud、归一化字段进 spec（参与 spec_hash——label 变更即期望态变更）。

import (
	"context"
	"strings"
	"testing"
)

// TestS3LabelNormalized fleetly.s3=true 归一为 Service.S3 开关位，进归一化
// 快照（canonical JSON 含 s3 字段 → spec_hash 感知 label 变更）。
func TestS3LabelNormalized(t *testing.T) {
	with := loadOK(t, writeCompose(t, `name: lblapp
services:
  web:
    image: repo/web:1
    labels:
      fleetly.s3: "true"
`))
	if len(with.Services) != 1 || !with.Services[0].S3 {
		t.Fatalf("S3 flag = %+v, want true on web", with.Services)
	}
	if !rawJSONContains(t, with, `"s3":true`) {
		t.Fatal("normalized snapshot must carry the s3 flag (desired-hash participates)")
	}

	without := loadOK(t, writeCompose(t, `name: lblapp
services:
  web:
    image: repo/web:1
`))
	if without.Services[0].S3 {
		t.Fatal("absent label must normalize to s3=false")
	}
}

// TestS3LabelInvalidValueRejected 值契约 fail-loud：非 "true" 值（含
// "false"——显式 opt-out 不是契约形态，静默吞掉拼写错误更危险）在解析期
// 以保留命名空间码拒绝。
func TestS3LabelInvalidValueRejected(t *testing.T) {
	for _, value := range []string{"false", "yes", "ture", "TRUE", ""} {
		_, _, err := Load(context.Background(), writeCompose(t, `name: lblapp
services:
  web:
    image: repo/web:1
    labels:
      fleetly.s3: "`+value+`"
`))
		if err == nil || !strings.Contains(err.Error(), "E_LABEL_RESERVED") {
			t.Errorf("value %q: err = %v, want E_LABEL_RESERVED", value, err)
		}
	}
}

// TestS3LabelKnownNamespace 登记 fleetly.s3 后，保留命名空间守卫不再拒绝
// 该键（既有守卫对其它未知键依旧生效——负面对照）。
func TestS3LabelKnownNamespace(t *testing.T) {
	_ = loadOK(t, writeCompose(t, `name: lblapp
services:
  web:
    image: repo/web:1
    labels:
      fleetly.s3: "true"
`))
	_, _, err := Load(context.Background(), writeCompose(t, `name: lblapp
services:
  web:
    image: repo/web:1
    labels:
      fleetly.unknown-key: "x"
`))
	if err == nil || !strings.Contains(err.Error(), "E_LABEL_RESERVED") {
		t.Fatalf("unknown fleetly.* key: err = %v, want E_LABEL_RESERVED", err)
	}
}

package backup

// manifest 锚定对象集（B4a，ADR-0096 决策 6「统一转储契约」的 manifest 半边
// + design §4.5 物理约束三条〔红队 A2/A3〕）：备份产物不再是单对象，而是
// manifest 锚定的对象集——`backups/<projectID>/<databaseID>/<utc-compact-ts>
// -<id>/manifest.json`（成员键列表 + 逐件 sha256 + family/engine/
// engine_version + 总 digest）+ 成员对象（`.../<member>`）。单流引擎（pg/
// mysql/mongo/redis，现役五模板全员）= 1 成员（成员名 data）；NATS 每流一
// .tgz（B4b）；未来分卷形态零机制变更。
//
// 台账行语义随批换形：ObjectKey = manifest 键；SizeBytes = 成员合计（不含
// manifest 自身）；Digest = total_digest（成员 sha256 的有序聚合摘要）。
// 无兼容层前提：直上 manifest 形，旧单对象键不迁移不承诺（ADR-0096 决策 6；
// staging 旧键孤儿化由 manifest flag-day 重建窗承载，design §7.6）。
//
// 写序铁律（红队 A3③）：成员逐件先 Put，**manifest 最后写**（全成员成功才
// 落 manifest + 台账 ObjectKey）；失败路径显式删已 Put 成员（幂等容错），
// 漏网残留由 backups 环的 List 孤儿对账 pass 兜底回收（宽限窗防在途竞态）。
//
// 纯度：类型与公式零 I/O（时间/摘要输入全由调用方传入，domainpurity 同
// 包既有口径）；JSON 键序稳定 = 结构体字段声明序（encoding/json 契约）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// RootPrefix 是备份对象键的根前缀（KeyMint 单源；List 孤儿对账的扫描面）。
const RootPrefix = "backups/"

// ManifestObjectName 是 manifest 在对象集内的成员名（锚定件恒住
// <set>/manifest.json）。
const ManifestObjectName = "manifest.json"

// DefaultMemberName 是单流引擎的成员名（工具容器 stdout 单产物；多流形态
// 成员名 = 流名，B4b NATS 逐流）。
const DefaultMemberName = "data"

// Manifest 是备份产物集的锚定清单（JSON 序列化键序 = 字段声明序：
// family, engine, engine_version, members, total_digest, created_at——
// 稳定键序是对象可比对性的一部分）。
type Manifest struct {
	// Family 是产物集的家族词面（enginetemplate.Family*；台账行源的族）。
	Family string `json:"family"`
	// Engine 是铸造时刻源实例的引擎名。
	Engine string `json:"engine"`
	// EngineVersion 是铸造时刻源实例的版本短名（ADR-0094 决策 5 的源版本
	// 锚同源；RestoreCompat 数据面）。
	EngineVersion string `json:"engine_version"`
	// Members 是成员清单（成员序 = 写序 = verify 逐件序 = Download 回传序
	// = restore 拉取序——四序一源）。
	Members []ManifestMember `json:"members"`
	// TotalDigest 是全成员 sha256 的有序聚合摘要（TotalDigest 公式单源）。
	TotalDigest string `json:"total_digest"`
	// CreatedAt 是 manifest 落位时刻（RFC3339 UTC）。
	CreatedAt string `json:"created_at"`
}

// ManifestMember 是对象集内一枚成员的回执行（Put 回执三元组的清单形态）。
type ManifestMember struct {
	// Key 是成员对象全键（恒住集前缀下——WithinSet 执法）。
	Key string `json:"key"`
	// SHA256 是成员对象内容摘要（Put 流式铸造的回执；verify 重算比对锚）。
	SHA256 string `json:"sha256"`
	// SizeBytes 是成员对象字节数。
	SizeBytes int64 `json:"size_bytes"`
}

// ManifestKeyOf 铸 manifest 对象键（集前缀 + manifest.json）。set 必须是
// KeyMint 产出的集前缀形态（调用方单源铸造，本公式纯拼接）。
func ManifestKeyOf(set string) string { return set + "/" + ManifestObjectName }

// MemberKeyOf 铸成员对象键（集前缀 + 成员名）。成员名是键的一段：安全闸
// 拒空名/点段/斜杠/反斜杠（纵深防御——ObjectStore 端口的 safeKey 是终执法
// 面，域闸保证拼装面不铸造病态键）。
func MemberKeyOf(set, member string) (string, error) {
	if err := ValidMemberName(member); err != nil {
		return "", err
	}
	return set + "/" + member, nil
}

// ValidMemberName 是成员名的键段安全闸。
func ValidMemberName(member string) error {
	switch member {
	case "":
		return fmt.Errorf("backup: manifest member name must not be empty")
	case ".", "..":
		return fmt.Errorf("backup: manifest member name %q is a path traversal segment", member)
	}
	if len(member) > 128 {
		return fmt.Errorf("backup: manifest member name exceeds 128 bytes")
	}
	if strings.ContainsAny(member, "/\\") {
		return fmt.Errorf("backup: manifest member name %q carries a path separator", member)
	}
	return nil
}

// SplitSetKey 把对象集内一枚对象键拆为（集前缀，成员名）。集前缀 = 恰四段
// （backups/<projectID>/<databaseID>/<ts>-<id>）；非集形态——legacy 单对象
// 键（B4a 前形态，恰四段本体）或更浅/更深的键——ok=false（List 孤儿对账的
// 分类判据：legacy 键不在对账射程，仍由保留滚动按行/对象成对律出清）。
func SplitSetKey(key string) (set, name string, ok bool) {
	idx := strings.LastIndex(key, "/")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", false
	}
	set, name = key[:idx], key[idx+1:]
	if len(strings.Split(set, "/")) != 4 {
		return "", "", false
	}
	return set, name, true
}

// TotalDigest 铸全成员聚合摘要：成员 sha256 hex（定长 64，拼接无歧义）按
// manifest 成员序原序拼接后取一次 sha256。有序聚合——成员序变化即 digest
// 变化（成员序是对象集的身份面之一）。
func TotalDigest(members []ManifestMember) string {
	h := sha256.New()
	for i := range members {
		h.Write([]byte(members[i].SHA256))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TotalSize 返回成员 size 合计（台账 SizeBytes 语义 = 成员合计，不含
// manifest 自身——manifest 是锚不是产物）。
func (m *Manifest) TotalSize() int64 {
	var total int64
	for i := range m.Members {
		total += m.Members[i].SizeBytes
	}
	return total
}

// WithinSet 报告全部成员键是否都住在给定集前缀下（manifest 完整性闸：
// 防篡改 manifest 把消费面（verify/download/restore/prune）指向集外任意键）。
func (m *Manifest) WithinSet(set string) bool {
	for i := range m.Members {
		if !strings.HasPrefix(m.Members[i].Key, set+"/") {
			return false
		}
	}
	return true
}

// MemberKeys 返回成员键清单（整集删除/失败清理的消费面）。
func (m *Manifest) MemberKeys() []string {
	out := make([]string, 0, len(m.Members))
	for i := range m.Members {
		out = append(out, m.Members[i].Key)
	}
	return out
}

// Encode 序列化 manifest（键序稳定 = 字段声明序；缩进零——对象字节形态
// 是可比对面）。结构全标量，Marshal 失败即编程错误。
func (m *Manifest) Encode() ([]byte, error) { return json.Marshal(m) }

// ParseManifest 解析并校验 manifest（fail-closed：形状坏/成员摘要非 64 hex/
// total_digest 不自洽即拒——损坏的 manifest 对消费面不可行动，诚实报错胜过
// 半恢复）。错误文本用户可见（英文）。
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("backup: manifest is not valid JSON: %w", err)
	}
	if m.Family == "" {
		return nil, fmt.Errorf("backup: manifest carries no family")
	}
	if m.Engine == "" {
		return nil, fmt.Errorf("backup: manifest carries no engine")
	}
	if len(m.Members) == 0 {
		return nil, fmt.Errorf("backup: manifest carries no members")
	}
	if m.CreatedAt == "" {
		return nil, fmt.Errorf("backup: manifest carries no created_at")
	}
	for i := range m.Members {
		mm := &m.Members[i]
		if mm.Key == "" {
			return nil, fmt.Errorf("backup: manifest member %d carries no key", i)
		}
		if len(mm.SHA256) != 64 {
			return nil, fmt.Errorf("backup: manifest member %d carries a malformed sha256 digest", i)
		}
		if _, err := hex.DecodeString(mm.SHA256); err != nil {
			return nil, fmt.Errorf("backup: manifest member %d carries a non-hex sha256 digest", i)
		}
		if mm.SizeBytes < 0 {
			return nil, fmt.Errorf("backup: manifest member %d carries a negative size", i)
		}
	}
	if m.TotalDigest == "" {
		return nil, fmt.Errorf("backup: manifest carries no total_digest")
	}
	if got := TotalDigest(m.Members); got != m.TotalDigest {
		return nil, fmt.Errorf("backup: manifest total_digest %s does not match the recomputed member digest %s", m.TotalDigest, got)
	}
	return &m, nil
}

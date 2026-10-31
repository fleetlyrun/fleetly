package composeintake_test

// B3a compose corpus 语料闸门（docs/design/2026-10-30-zeabur-oss-borrowing.md §3）：
// 受理面行为钉子——语料不改 fail-closed 受理律，只钉行为。四层断言：
//
//  1. 永不 panic：任何输入最坏是错误（recover 捕获并归因到具体文件）；
//  2. 逐文件 golden：受理结果与 manifest.json 相等（accept = 归一化 Spec 的
//     稳定 digest；reject = 错误稳定标识 "field: reason"）；漂移须 -update
//     再生成 + commit 理由；
//  3. manifest↔目录一致性：孤儿文件 / 漏登记 / 重复登记 / 缺头两行来源
//     标注即红；
//  4. 双跑防随机：同文件两次受理的规范序列化逐字节相同（钉归一化确定性，
//     map 遍历序无处可藏）。
//
// 附带第五层（内嵌目录防漂移）：builtin-*.yaml 与 apptemplate 内嵌目录的
// 预览渲染逐字节一致——目录模板变更后跑 -update 重导出并同 commit 提交。
//
// manifest 形态：outcome/derived 字段全部由 -update 生成；vars 是唯一的
// 手工输入（dokploy env 面重放等插值环境），再生成时从旧 manifest 原样携带。
//
// 再生成：go test ./internal/application/composeintake/ -update

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/application/composeintake"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

var corpusUpdate = flag.Bool("update", false, "regenerate the compose corpus manifest (and the builtin-catalog exports)")

// 语料目录与受理锚（app/project 恒定——digest 是这些锚的函数，改动即全量漂移）。
const (
	corpusDir        = "testdata/corpus"
	corpusManifest   = "testdata/corpus/manifest.json"
	corpusAppAnchor  = "corpus-app"
	corpusProjAnchor = "corpus-project"
	// corpusFloor 是裁决的语料下界（Apache/MIT 外部样例 + 仓内优先，30-50
	// 文件起步）——静默缩水即红。
	corpusFloor = 30
)

// corpusEntry 是 manifest 的逐文件行。Outcome=accept 携带 digest（revision
// digest 同源，见 corpusDigest）；reject 携带 error（ValidationError 的
// "field: reason" 稳定标识——受理面错误不落 errcode 注册表，稳定标识即此）。
type corpusEntry struct {
	File    string            `json:"file"`
	Source  string            `json:"source"`
	License string            `json:"license"`
	Vars    map[string]string `json:"vars,omitempty"`
	Outcome string            `json:"outcome"`          // "accept" | "reject"
	Digest  string            `json:"digest,omitempty"` // accept: sha256 hex（规范序列化指纹）
	Error   string            `json:"error,omitempty"`  // reject: "field: reason"
}

type corpusManifestDoc struct {
	Version int           `json:"version"`
	Note    string        `json:"note"`
	Entries []corpusEntry `json:"entries"`
}

// corpusResult 是一次受理的产物（四层断言共用的观测值）。
type corpusResult struct {
	specBody []byte // 归一化 Spec 的确定性序列化（与冻结咽喉同参）
	digest   string
	spec     *specv1.AppSpec
	err      error
	panicked bool
}

// corpusSpecMarshal 是规范序列化的同参声明：与 engine 冻结咽喉
// （SubmitSpec）与 api 层 marshalSpec 完全同参（EmitUnpopulated=false +
// UseProtoNames=true）——digest 与 Spec digest 同源，不造第二套序列化。
// 其上叠一层 json.Compact 空白归一（ADR-0090 确定性律补钉）：protojson 的
// 空白形态由 detrand 按测试二进制撒种——同进程内恒定、跨构建翻转，committed
// golden 会因任意无关代码改动全体漂移（W4 修复 intake.go 时咬出：26 条 accept
// digest 齐翻）。归一手法与 gateway writeHookProtoJSON 同先例；compact 形态
// 即 W5c 首档 manifest 的落盘形态，归一后 accept 行逐字节不动。
var corpusSpecMarshal = protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}

// corpusDigest 复用 revision.Digest（冻结指纹公式单源：sha256 hex）。
func corpusDigest(body []byte) string { return revision.Digest(body) }

// admitCorpus 受理一份语料并观测四层断言所需的全部事实。永不 panic 层在
// 此实现：recover 把 panic 归因为该文件的观测错误（任何输入最坏是错误）。
func admitCorpus(body []byte, vars composeintake.VariableLookup) (res corpusResult) {
	defer func() {
		if r := recover(); r != nil {
			res = corpusResult{panicked: true, err: fmt.Errorf("intake panicked: %v", r)}
		}
	}()
	app, err := composeintake.FromYAML(body, corpusAppAnchor, corpusProjAnchor, vars)
	if err != nil {
		res.err = err
		return res
	}
	marshaled, err := corpusSpecMarshal.Marshal(app)
	if err != nil {
		res.err = fmt.Errorf("normalized spec failed canonical marshal: %w", err)
		return res
	}
	// detrand 空白归一：digest 与双跑断言都以 compact 形态为观测值（见
	// corpusSpecMarshal 注释——跨构建稳定是 committed golden 的存在前提）。
	var compact bytes.Buffer
	if jerr := json.Compact(&compact, marshaled); jerr != nil {
		res.err = fmt.Errorf("normalized spec failed whitespace normalization: %w", jerr)
		return res
	}
	res.specBody = compact.Bytes()
	res.digest = corpusDigest(res.specBody)
	res.spec = app
	return res
}

// corpusVarLookup 把 manifest 行的 vars（手工输入面）接成受理插值环境。
func corpusVarLookup(e corpusEntry) composeintake.VariableLookup {
	if len(e.Vars) == 0 {
		return nil
	}
	return func(name string) (string, bool) {
		v, ok := e.Vars[name]
		return v, ok
	}
}

// parseCorpusHeader 剥离并校验头两行来源标注（# source: / # license:）。
// 返回标注对与去头正文（受理输入恰是正文——标注不参与受理）。
func parseCorpusHeader(t *testing.T, rel string, body []byte) (source, license string, rest []byte) {
	t.Helper()
	text := string(body)
	lineEnd := strings.IndexByte(text, '\n')
	if lineEnd < 0 {
		t.Fatalf("%s: missing two-line provenance header (# source: / # license:)", rel)
	}
	first := text[:lineEnd]
	remainder := text[lineEnd+1:]
	secondEnd := strings.IndexByte(remainder, '\n')
	var second string
	if secondEnd < 0 {
		second = remainder
		rest = nil
	} else {
		second = remainder[:secondEnd]
		rest = []byte(remainder[secondEnd+1:])
	}
	const sourcePrefix = "# source: "
	const licensePrefix = "# license: "
	if !strings.HasPrefix(first, sourcePrefix) || len(first) == len(sourcePrefix) {
		t.Fatalf("%s: first line must be %q<provenance>, got %q", rel, sourcePrefix, first)
	}
	if !strings.HasPrefix(second, licensePrefix) || len(second) == len(licensePrefix) {
		t.Fatalf("%s: second line must be %q<license>, got %q", rel, licensePrefix, second)
	}
	return strings.TrimSpace(first[len(sourcePrefix):]), strings.TrimSpace(second[len(licensePrefix):]), rest
}

// corpusOutcome 把一次受理观测折叠成 manifest 行的 outcome 派生字段。
func corpusOutcome(res corpusResult) (outcome, digest, errIdentity string) {
	if res.err != nil {
		return "reject", "", res.err.Error()
	}
	return "accept", res.digest, ""
}

// TestCorpusGate 是 B3a 语料闸门的四层断言（+ 内嵌目录防漂移第五层）。
func TestCorpusGate(t *testing.T) {
	// ---- 语料盘点：目录在、纯 yaml 样本 + manifest、量不破下界。 ----
	entriesOnDisk, err := filepath.Glob(filepath.Join(corpusDir, "*.yaml"))
	require.NoError(t, err, "corpus directory %s must exist with samples", corpusDir)
	require.GreaterOrEqual(t, len(entriesOnDisk), corpusFloor,
		"corpus shrank below the adjudicated floor of %d samples (30-50 起步, 仓内优先)", corpusFloor)
	stray, err := os.ReadDir(corpusDir)
	require.NoError(t, err)
	for _, f := range stray {
		if f.IsDir() {
			t.Errorf("corpus directory carries subdirectory %s (flat samples only)", f.Name())
			continue
		}
		if strings.HasSuffix(f.Name(), ".yaml") || f.Name() == "manifest.json" {
			continue
		}
		t.Errorf("corpus directory carries stray file %s (samples are *.yaml; the ledger is manifest.json)", f.Name())
	}

	// ---- 第五层（先于 manifest）：builtin-*.yaml 与内嵌目录防漂移。----
	// -update 时先重导出（新目录条目落文件），随后 manifest 再登记。
	oldManifest := loadCorpusManifest(t, false)
	syncBuiltinExports(t)

	// ---- manifest 装载与第三层：manifest↔目录一致性。----
	// -update 时先重导出内嵌目录（可能新增 builtin 文件），盘点必须重取。
	manifest := loadCorpusManifest(t, true)
	if *corpusUpdate {
		manifest = regenerateCorpusManifest(t, oldManifest, listCorpusYAML(t))
	}
	entriesOnDisk = listCorpusYAML(t)
	require.Equal(t, 1, manifest.Version, "manifest schema version must be 1")
	byFile := map[string]corpusEntry{}
	for _, e := range manifest.Entries {
		if _, dup := byFile[e.File]; dup {
			t.Errorf("manifest registers %s twice", e.File)
			continue
		}
		byFile[e.File] = e
	}
	for _, path := range entriesOnDisk {
		rel := filepath.Base(path)
		e, ok := byFile[rel]
		if !ok {
			t.Errorf("corpus sample %s is not registered in manifest.json (漏登记；登记或删除后跑 -update)", rel)
			continue
		}
		body, rerr := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有语料夹具
		require.NoError(t, rerr)
		src, lic, _ := parseCorpusHeader(t, rel, body)
		// manifest 行的来源/许可与文件头两行互为镜像（防手改漂移）。
		assert.Equal(t, src, e.Source, "%s: manifest source drifts from the file header", rel)
		assert.Equal(t, lic, e.License, "%s: manifest license drifts from the file header", rel)
		delete(byFile, rel)
	}
	for rel := range byFile {
		t.Errorf("manifest entry %s has no corpus file (孤儿登记；删除条目)", rel)
	}

	// ---- 逐文件四层断言。----
	for _, path := range entriesOnDisk {
		rel := filepath.Base(path)
		e, ok := entryFor(manifest, rel)
		if !ok {
			continue // 漏登记已在第三层红过，此处不重复受理。
		}
		t.Run(rel, func(t *testing.T) {
			body, rerr := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有语料夹具
			require.NoError(t, rerr)
			_, _, composeBody := parseCorpusHeader(t, rel, body)

			// 双跑：同输入两次受理（第四层防随机的观测基础）。
			first := admitCorpus(composeBody, corpusVarLookup(e))
			second := admitCorpus(composeBody, corpusVarLookup(e))

			// 第一层：永不 panic——任何输入最坏是错误。
			require.False(t, first.panicked || second.panicked,
				"intake must never panic: worst case is an error (fail-closed admission law)")

			// 第四层：双跑防随机——两次受理的规范序列化逐字节相同。
			if first.err == nil && second.err == nil {
				require.Equal(t, first.specBody, second.specBody,
					"canonical serialization of the same input must be byte-identical across runs (normalization determinism)")
				require.Equal(t, first.digest, second.digest)
			} else {
				require.EqualError(t, first.err, errText(second), "rejection identity must be identical across runs")
			}

			// 第二层：逐文件 golden——结果与 manifest 相等。
			outcome, digest, errIdentity := corpusOutcome(first)
			require.Equal(t, e.Outcome, outcome,
				"admission outcome drifted from manifest.json (漂移须 -update 再生成 + commit 理由; got outcome %q)", outcome)
			switch outcome {
			case "accept":
				require.NotNil(t, first.spec)
				assert.Equal(t, e.Digest, digest, "normalized spec digest drifted (rerun with -update and commit the reason)")
				assert.Empty(t, e.Error, "accepted entries must not carry an error identity")
			case "reject":
				assert.Equal(t, e.Error, errIdentity, "rejection identity drifted (rerun with -update and commit the reason)")
				assert.Empty(t, e.Digest, "rejected entries must not carry a digest")
			default:
				t.Fatalf("manifest outcome %q is neither accept nor reject", outcome)
			}
		})
	}

	// ---- -update：落盘 manifest（受理与断言已先行——写盘的是刚验证过的观测）。----
	if *corpusUpdate {
		writeCorpusManifest(t, manifest)
	}
}

// errText 统一 nil → "<nil>"（EqualError 需要）。
func errText(r corpusResult) string {
	if r.err == nil {
		return "<nil>"
	}
	return r.err.Error()
}

// entryFor 取 manifest 行（无则 ok=false）。
func entryFor(m corpusManifestDoc, file string) (corpusEntry, bool) {
	for _, e := range m.Entries {
		if e.File == file {
			return e, true
		}
	}
	return corpusEntry{}, false
}

// loadCorpusManifest 装载 manifest（-update 首跑容缺——重建不需要旧档，
// 只有 vars 输入面从旧档携带，缺失即空集）。
func loadCorpusManifest(t *testing.T, required bool) corpusManifestDoc {
	t.Helper()
	raw, err := os.ReadFile(corpusManifest)
	if err != nil {
		if !required || *corpusUpdate {
			return corpusManifestDoc{Version: 1}
		}
		t.Fatalf("manifest.json unreadable: %v (首次落地或漂移修复跑 go test ./internal/application/composeintake/ -update)", err)
	}
	var m corpusManifestDoc
	require.NoError(t, json.Unmarshal(raw, &m), "manifest.json is not valid JSON")
	return m
}

// regenerateCorpusManifest 重建 manifest 行：outcome/digest/error 由受理观测
// 生成；vars 是手工输入面，从旧 manifest 原样携带（插值环境不是派生物）。
func regenerateCorpusManifest(t *testing.T, old corpusManifestDoc, entriesOnDisk []string) corpusManifestDoc {
	t.Helper()
	oldVars := map[string]map[string]string{}
	for _, e := range old.Entries {
		if len(e.Vars) > 0 {
			oldVars[e.File] = e.Vars
		}
	}
	out := corpusManifestDoc{
		Version: 1,
		Note: "generated by go test ./internal/application/composeintake/ -update; " +
			"vars are hand-maintained inputs (dokploy env panel replay), outcomes are derived; " +
			"drift commits must state the reason (B3a corpus gate)",
	}
	for _, path := range entriesOnDisk {
		rel := filepath.Base(path)
		body, rerr := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有语料夹具
		require.NoError(t, rerr)
		src, lic, composeBody := parseCorpusHeader(t, rel, body)
		e := corpusEntry{File: rel, Source: src, License: lic, Vars: oldVars[rel]}
		res := admitCorpus(composeBody, corpusVarLookup(e))
		require.False(t, res.panicked, "%s: intake panicked while regenerating the manifest", rel)
		e.Outcome, e.Digest, e.Error = corpusOutcome(res)
		out.Entries = append(out.Entries, e)
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].File < out.Entries[j].File })
	return out
}

// writeCorpusManifest 落盘 manifest（稳定 JSON：缩进两格 + 尾换行）。
func writeCorpusManifest(t *testing.T, m corpusManifestDoc) {
	t.Helper()
	raw, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(corpusManifest, append(raw, '\n'), 0o600))
	t.Logf("corpus manifest rewritten: %d entries", len(m.Entries))
}

// syncBuiltinExports 同步 builtin-*.yaml 与 apptemplate 内嵌目录：
// -update 时从内嵌目录重导出（预览值渲染——catalog.go dryRender 同口径）；
// 校验态逐字节比对，漂移即红并指明重导出动线。仓内模板目录由此全量自动
// 收录，静态文件不再需要手工同步。
func syncBuiltinExports(t *testing.T) {
	t.Helper()
	builtins, err := apptemplate.Builtin()
	require.NoError(t, err, "embedded template catalog must parse (startup-time law)")
	corpusNames := map[string]bool{}
	for _, path := range listCorpusYAML(t) {
		corpusNames[filepath.Base(path)] = true
	}
	for _, entry := range builtins {
		rel := "builtin-" + entry.Name + ".yaml"
		want := renderBuiltinCorpus(t, entry)
		path := filepath.Join(corpusDir, rel)
		if *corpusUpdate {
			require.NoError(t, os.WriteFile(path, want, 0o600), "export builtin catalog entry %s", entry.Name)
			continue
		}
		got, rerr := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有语料夹具
		if rerr != nil {
			t.Errorf("builtin catalog entry %s has no corpus export %s (run -update to export it)", entry.Name, rel)
			continue
		}
		assert.Equal(t, string(want), string(got),
			"%s drifted from the embedded apptemplate catalog (catalog changed? rerun go test ./internal/application/composeintake/ -update and commit both)", rel)
		corpusNames[rel] = false
	}
	// 反向：目录里残留的 builtin-*.yaml 若无对应内嵌条目（模板改名/删除）即红。
	for _, path := range listCorpusYAML(t) {
		rel := filepath.Base(path)
		if !strings.HasPrefix(rel, "builtin-") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(rel, "builtin-"), ".yaml")
		found := false
		for _, entry := range builtins {
			if entry.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("corpus export %s has no embedded catalog entry %q (stale export; delete it)", rel, name)
		}
	}
}

// renderBuiltinCorpus 渲染一条内嵌目录条目为语料文件（头两行标注 + 预览值
// 渲染产物）。预览值与 catalog.go dryRender 同口径：string → 缺省或
// "preview"，domain → 缺省或 "preview.invalid"（secret 平台铸造不参与）。
func renderBuiltinCorpus(t *testing.T, entry apptemplate.Entry) []byte {
	t.Helper()
	doc, err := apptemplate.Parse([]byte(entry.Body))
	require.NoError(t, err, "builtin entry %s must parse (catalog law)", entry.Name)
	values := map[string]string{}
	for _, v := range doc.Variables {
		switch v.Type {
		case apptemplate.VarString:
			if v.Default != "" {
				values[v.Name] = v.Default
			} else {
				values[v.Name] = "preview"
			}
		case apptemplate.VarDomain:
			if v.Default != "" {
				values[v.Name] = v.Default
			} else {
				values[v.Name] = "preview.invalid"
			}
		}
	}
	rendered, err := doc.Render(values, "catalog-preview")
	require.NoError(t, err, "builtin entry %s must dry-render (catalog law)", entry.Name)
	body, err := yaml.Marshal(rendered.Compose)
	require.NoError(t, err)
	header := fmt.Sprintf(
		"# source: in-repo apptemplate builtin catalog %q v%s (exported by the corpus gate -update; preview-value render, catalog.go dryRender 口径)\n"+
			"# license: in-repo test fixture (rides the repository tree; no separate license)\n",
		entry.Name, entry.Version)
	return []byte(header + string(body))
}

// listCorpusYAML 列语料 yaml（排序稳定）。
func listCorpusYAML(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(corpusDir, "*.yaml"))
	require.NoError(t, err)
	sort.Strings(paths)
	return paths
}

// TestCorpusRejectIdentityIsValidationError 受理面拒绝的稳定标识来源钉死：
// 全部 reject 错误必须是 spec.ValidationError（"field: reason" 形态）——
// manifest 的 error 字段以此为形，散装 error 类型会破坏逐文件 golden 的
// 稳定性契约。
func TestCorpusRejectIdentityIsValidationError(t *testing.T) {
	manifest := loadCorpusManifest(t, true)
	rejects := 0
	for _, e := range manifest.Entries {
		if e.Outcome != "reject" {
			continue
		}
		rejects++
		require.NotEmpty(t, e.Error, "%s: reject entry must carry an error identity", e.File)
		body, rerr := os.ReadFile(filepath.Join(corpusDir, e.File)) //nolint:gosec // 读取本包 testdata 自有语料夹具
		require.NoError(t, rerr)
		_, _, composeBody := parseCorpusHeader(t, e.File, body)
		res := admitCorpus(composeBody, corpusVarLookup(e))
		require.False(t, res.panicked)
		require.Error(t, res.err)
		var ve *spec.ValidationError
		require.True(t, errors.As(res.err, &ve),
			"%s: intake rejections must be spec.ValidationError (stable identity law), got %T", e.File, res.err)
	}
	require.Greater(t, rejects, 0, "corpus must carry negative samples (fail-closed face)")
}

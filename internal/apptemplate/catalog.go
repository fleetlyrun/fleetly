// 目录面（ADR-0050 决策 1/4）：内嵌目录（go:embed，冷启动/离线即全部——
// ADR-0045"随二进制"纪律同款）+ 远端清单拉取核对（RefreshTemplates 的纯
// 校验半边；持久化在 state/catalog，api 层协调）。刷新 fail-closed：digest
// 不符或任一条目预校验拒 = 整次拒绝（坏目录不换好目录）。
package apptemplate

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/fleetlyrun/fleetly/internal/spec"
)

//go:embed catalog/*.yaml
var builtinFS embed.FS

// Entry 是目录条目：name 是目录键、version 是模板自身版本、digest 是
// 模板体 sha256（刷新核对与回显锚）、body 是模板 YAML 全文。
type Entry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Body    string `json:"body"`
}

// DigestOf 铸模板体的内容寻址 digest（sha256，带算法前缀——P7 输出面口径）。
func DigestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Builtin 解析内嵌目录（启动即校验：坏内嵌条目是构建期缺陷，启动即红——
// 不带病服务）。返回条目按 name 排序稳定。
func Builtin() ([]Entry, error) {
	files, err := builtinFS.ReadDir("catalog")
	if err != nil {
		return nil, fmt.Errorf("apptemplate: builtin catalog unreadable: %w", err)
	}
	out := []Entry{}
	for _, f := range files {
		body, err := builtinFS.ReadFile("catalog/" + f.Name())
		if err != nil {
			return nil, fmt.Errorf("apptemplate: builtin entry %s unreadable: %w", f.Name(), err)
		}
		entry, err := validatedEntry(string(body))
		if err != nil {
			return nil, fmt.Errorf("apptemplate: builtin entry %s: %w", f.Name(), err)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// validatedEntry 解析 + 预校验一条目录条目并铸造 digest（内嵌与远端共用
// 单源——两条目录源的条目同一条执法链）。
func validatedEntry(body string) (Entry, error) {
	doc, err := Parse([]byte(body))
	if err != nil {
		return Entry{}, err
	}
	if err := dryRender(doc); err != nil {
		return Entry{}, err
	}
	return Entry{Name: doc.Name, Version: doc.Version, Digest: DigestOf(body), Body: body}, nil
}

// dryRender 用合成值整链预校验（渲染 → NormalizeCompose 白名单喉 →
// ValidateApp 叶子执法——占位 AppRef 只需非空）。刷新预校验与测试消费。
func dryRender(doc *Doc) error {
	values := map[string]string{}
	for _, v := range doc.Variables {
		switch v.Type {
		case VarString:
			if v.Default != "" {
				values[v.Name] = v.Default
			} else {
				values[v.Name] = "preview"
			}
		case VarDomain:
			if v.Default != "" {
				values[v.Name] = v.Default
			} else {
				values[v.Name] = "preview.invalid"
			}
		}
	}
	rendered, err := doc.Render(values, "catalog-preview")
	if err != nil {
		return err
	}
	if _, err := spec.NormalizeCompose(rendered.Compose, "catalog-preview-app", "catalog-preview-project"); err != nil {
		return err
	}
	return nil
}

// Manifest 是远端 catalog.json 的形态：version 必须是 1（清单 schema 的
// 版本面，与模板自身 version 分立）；templates 携带全量条目（body 内联）。
type Manifest struct {
	Version   int             `json:"version"`
	Templates []ManifestEntry `json:"templates"`
}

// ManifestEntry 与 Entry 同形（JSON 标签单源经此钉死）。
type ManifestEntry = Entry

// ErrCatalogEmpty 是清单零条目的哨兵（刷新拒绝——空目录不是合法目标态）。
var ErrCatalogEmpty = errors.New("apptemplate: catalog manifest carries no templates")

// FetchCatalog 拉取并核对远端目录（base 形如 https://host/path——拼
// /catalog.json）：每条 digest 核对 + 全量预校验 + 重名拒绝；任一条目坏 =
// 整次拒绝（fail-closed）。HTTP 细节（超时/状态码）是操作员可处置面，
// 错误文本直给。
func FetchCatalog(ctx context.Context, baseURL string) ([]Entry, error) {
	url := baseURL + "/catalog.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("apptemplate: catalog request could not be built: %w", err)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("apptemplate: catalog fetch %s failed: %w", url, err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读面
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apptemplate: catalog fetch %s returned %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 清单上界 8MiB——模板体是 KB 级文档
	if err != nil {
		return nil, fmt.Errorf("apptemplate: catalog body could not be read: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("apptemplate: catalog manifest is not valid JSON: %w", err)
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("apptemplate: catalog manifest version %d is not supported (want 1)", manifest.Version)
	}
	if len(manifest.Templates) == 0 {
		return nil, ErrCatalogEmpty
	}
	seen := map[string]bool{}
	out := make([]Entry, 0, len(manifest.Templates))
	for i, e := range manifest.Templates {
		if e.Body == "" {
			return nil, fmt.Errorf("apptemplate: manifest template %d (%s) carries no body", i, e.Name)
		}
		if want := DigestOf(e.Body); want != e.Digest {
			return nil, fmt.Errorf("apptemplate: manifest template %s digest mismatch (manifest %s, computed %s)", e.Name, e.Digest, want)
		}
		entry, err := validatedEntry(e.Body)
		if err != nil {
			return nil, fmt.Errorf("apptemplate: manifest template %s failed validation: %w", e.Name, err)
		}
		if entry.Name != e.Name || entry.Version != e.Version {
			return nil, fmt.Errorf("apptemplate: manifest template envelope says %s@%s but the body declares %s@%s",
				e.Name, e.Version, entry.Name, entry.Version)
		}
		if seen[entry.Name] {
			return nil, fmt.Errorf("apptemplate: manifest carries duplicate template name %s", entry.Name)
		}
		seen[entry.Name] = true
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

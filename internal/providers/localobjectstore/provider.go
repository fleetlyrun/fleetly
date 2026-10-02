// Package localobjectstore 实现 ObjectStore Capability 的本地 Provider
// （ADR-0020：默认安装自带本地备份目标，数据根内备份目录，开箱即用）。
// 对象键是正斜杠分层路径（backups/<project>/<database>/<timestamp>）；
// 本 Provider 只做目录承载，保留策略执行器（F2）经 Delete 消费。
package localobjectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// backupsDirName 是数据根下的备份目录名。
const backupsDirName = "backups"

// Provider 是本地文件系统 ObjectStore。
type Provider struct {
	root string
}

// 编译期契约断言。
var _ capability.ObjectStore = (*Provider)(nil)

// New 构造 Provider：dataRoot 是平台数据根（备份落 <dataRoot>/backups/；
// 目录惰性创建——健康探测在构造后首次写入前完成可写性检查）。
func New(dataRoot string) (*Provider, error) {
	if dataRoot == "" {
		return nil, fmt.Errorf("localobjectstore: data root is required")
	}
	return &Provider{root: filepath.Join(dataRoot, backupsDirName)}, nil
}

// Describe 实现 Provider 契约三件套之一。Notes 承载 ADR-0020 的诚实边界：
// 同机备份非灾备。
func (p *Provider) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{
		Name:       "local",
		Capability: capability.KindObjectStore,
		Version:    "1",
		Notes: []string{
			"local backup target inside the platform data root, available out of the box (ADR-0020)",
			"same-machine backup is NOT disaster recovery: losing the machine loses the backups; configure an external S3-compatible target when one exists",
		},
	}
}

// Health 实现 Provider 契约三件套之一（目录存在且可写）。
func (p *Provider) Health(ctx context.Context) capability.HealthReport {
	if err := p.probeWritable(ctx); err != nil {
		return capability.HealthReport{Healthy: false, Details: "local backup target unwritable: " + err.Error()}
	}
	return capability.HealthReport{Healthy: true, Details: "local backup target writable at " + p.root}
}

func (p *Provider) probeWritable(context.Context) error {
	if err := os.MkdirAll(p.root, 0o750); err != nil {
		return err
	}
	probe := filepath.Join(p.root, ".probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

// Put 写一个对象（原子落位：tmp+rename；reader 由调用方关闭）。
func (p *Provider) Put(_ context.Context, key string, r io.Reader) error {
	path, err := p.objectPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304：键已经 safeKey 钳制（无遍历/绝对路径）
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Get 读一个对象（调用方负责 Close）。
func (p *Provider) Get(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := p.objectPath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // G304：键已经 safeKey 钳制
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("localobjectstore: object %q not found", key)
		}
		return nil, err
	}
	return f, nil
}

// List 列举前缀下对象键（前缀空 = 全部；排序稳定。尾斜杠是目录前缀的
// 自然形态，剥后再钳）。
func (p *Provider) List(_ context.Context, prefix string) ([]string, error) {
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix != "" {
			cleaned, err := safeKey(prefix)
			if err != nil {
				return nil, err
			}
			prefix = cleaned
		}
	}
	var out []string
	base := p.root
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 根未建 = 空集
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".tmp") {
			return nil // 在途写入不进列举面
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return rerr
		}
		key := filepath.ToSlash(rel)
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}
		out = append(out, key)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(out)
	return out, nil
}

// Delete 删除一个对象（幂等：不存在不报错——保留策略执行器按清单滚动，
// 单键迟到缺失不阻断清扫）。
func (p *Provider) Delete(_ context.Context, key string) error {
	path, err := p.objectPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// objectPath 解析对象键为绝对路径（safeKey 钳制后拼接）。
func (p *Provider) objectPath(key string) (string, error) {
	clean, err := safeKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.root, filepath.FromSlash(clean)), nil
}

// safeKey 钳制对象键：正斜杠分层、每段非空、禁止遍历段、反斜杠与
// Windows 盘符形态（路径穿越是对象存储端口的第一攻击面——键来自备份
// 执行器，但端口不信任调用方；时间戳段里的 ":" 合法，只禁首段盘符
// 形态）。
func safeKey(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("localobjectstore: key must not be empty")
	}
	if strings.Contains(key, "\\") {
		return "", fmt.Errorf("localobjectstore: key %q must use forward slashes only", key)
	}
	if len(key) >= 2 && key[1] == ':' {
		return "", fmt.Errorf("localobjectstore: key %q looks like a windows drive path", key)
	}
	parts := strings.Split(key, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("localobjectstore: key %q has an empty or traversing segment", key)
		}
	}
	return key, nil
}

// init 自注册工厂（cmd/fleetlyd blank import 触发）。零配置默认在册：
// 数据根经 FLEETLY_DATA_ROOT 注入（zot 工厂同款 env 文化；消费方是 F2
// 备份执行链，装配期 Build 在消费面落地时接入）。
func init() {
	capability.RegisterFactory(capability.KindObjectStore, "local", func(context.Context) (capability.Provider, error) {
		dataRoot := os.Getenv("FLEETLY_DATA_ROOT")
		if dataRoot == "" {
			dataRoot = "./data" // 与 config.DefaultDataRoot 同缺省（install.sh 恒注入 env，此处兜底）
		}
		return New(dataRoot)
	})
}

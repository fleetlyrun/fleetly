// Package localobjectstore 实现 ObjectStore Capability 的本地 Provider
// （ADR-0020：默认安装自带本地备份目标，数据根内备份目录，开箱即用）。
// 对象键是正斜杠分层路径（backups/<project>/<database>/<timestamp>）；
// 本 Provider 只做目录承载，保留策略执行器（F2）经 Delete 消费。
package localobjectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
	// 探测文件用唯一名（CreateTemp）：探测面可能并发进入（健康检查节拍与
	// doctor 自证同拍、多进程共数据根的误配形态）——固定名会让并发方互删
	// 探测文件，Remove 撞 ErrNotExist 被误判为不可写（假 unhealthy）。
	probe, err := os.CreateTemp(p.root, ".probe-")
	if err != nil {
		return err
	}
	name := probe.Name()
	_ = probe.Close()
	return os.Remove(name)
}

// Put 写一个对象（原子落位：tmp+rename；reader 由调用方关闭），流式
// 铸 sha256 摘要并回执（restore verify 的校验锚，端口契约见 capability
// ObjectStore）。rename 前 fsync：跨进程崩溃安全——掉电/崩溃后 rename
// 已落但数据未落盘的形态会让备份对象空壳/半截（备份的可信度=可恢复性，
// sync 是落位承诺的一部分；Windows 上 File.Sync() 同样可用）。
func (p *Provider) Put(ctx context.Context, key string, r io.Reader) (capability.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return capability.ObjectInfo{}, err // 入界检查（端口契约；本地写受磁盘限速无长挂面）
	}
	path, err := p.objectPath(key)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return capability.ObjectInfo{}, err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304：键已经 safeKey 钳制（无遍历/绝对路径）
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return capability.ObjectInfo{}, err
	}
	if err := fileSync(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return capability.ObjectInfo{}, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return capability.ObjectInfo{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return capability.ObjectInfo{}, err
	}
	return capability.ObjectInfo{Key: key, Size: size, Digest: hex.EncodeToString(h.Sum(nil))}, nil
}

// fileSync 是 fsync 的注入缝（单测注入失败形态钉错误路径；生产 = File.Sync）。
var fileSync = func(f *os.File) error { return f.Sync() }

// Get 读一个对象（调用方负责 Close）。ctx 贯通读流（端口契约）：取消
// 即关底层文件——在途 Read 以错误返回，GB 级 restore 可中途放弃。
func (p *Provider) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // 入界取消：不开无用句柄
	}
	path, err := p.objectPath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // G304：键已经 safeKey 钳制
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("localobjectstore: object %q: %w", key, capability.ErrObjectNotFound)
		}
		return nil, err
	}
	return newCtxFile(ctx, f), nil
}

// ctxFile 是 ctx 贯通的文件读流：ctx 取消与显式 Close 双路径各只生效
// 一次（once 守 close 信号），看门狗 goroutine 随 Close 退出。
type ctxFile struct {
	f    *os.File
	done chan struct{}
	once sync.Once
}

func newCtxFile(ctx context.Context, f *os.File) *ctxFile {
	c := &ctxFile{f: f, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = c.f.Close() // 取消即断流（幂等：Close 后在途 Read 报错）
		case <-c.done:
		}
	}()
	return c
}

func (c *ctxFile) Read(p []byte) (int, error) { return c.f.Read(p) }

func (c *ctxFile) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.f.Close()
}

// Stat 回读单个对象的元数据（不读体、不重算 digest——端口契约）。
func (p *Provider) Stat(_ context.Context, key string) (capability.ObjectInfo, error) {
	path, err := p.objectPath(key)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return capability.ObjectInfo{}, fmt.Errorf("localobjectstore: object %q: %w", key, capability.ErrObjectNotFound)
		}
		return capability.ObjectInfo{}, err
	}
	return capability.ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime()}, nil
}

// List 列举前缀下对象（含元数据：size/modtime——保留窗滚动按存储属性
// 排序，不解析键名；前缀空 = 全部，排序稳定。尾斜杠是目录前缀的自然
// 形态，剥后再钳）。遍历按前缀剪枝：命中键必然住在前缀的父目录
// （dirPrefix）子树内，其余子树整枝跳过——备份根随年限增长后，按库列举
// 不再退化为全根遍历（剪枝语义 = WalkDir 的 SkipDir，结果集不变）。
func (p *Provider) List(_ context.Context, prefix string) ([]capability.ObjectInfo, error) {
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
	// dirPrefix 是前缀所在的目录（无斜杠 = 前缀即文件名形态，父目录为根）。
	dirPrefix := ""
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		dirPrefix = prefix[:i]
	}
	var out []capability.ObjectInfo
	base := p.root
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // 根未建 = 空集
			}
			return err
		}
		if d.IsDir() {
			rel, rerr := filepath.Rel(base, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if rel != "." && !dirMayContain(rel, dirPrefix) {
				return fs.SkipDir // 不可能命中前缀的子树整枝剪掉
			}
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
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		out = append(out, capability.ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// dirMayContain 报告目录 relDir 是否可能含 dirPrefix 前缀下的键：relDir
// 是 dirPrefix 的祖先（通往它的必经路径）或其子树成员，二者其一才可能
// 命中。组件级比较（带尾斜杠）——纯字符串前缀会把 "db1" 误判进 "db10"
// 子树。剪枝只动遍历量，结果集判定仍由调用方的 HasPrefix 把守。
func dirMayContain(relDir, dirPrefix string) bool {
	if dirPrefix == "" {
		return true // 无前缀 = 全根遍历
	}
	if relDir == dirPrefix {
		return true
	}
	return strings.HasPrefix(dirPrefix, relDir+"/") || strings.HasPrefix(relDir, dirPrefix+"/")
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

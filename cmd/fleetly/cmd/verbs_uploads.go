package cmd

// Uploads 动词组（F1.10，ADR-0019 附录 A）：put（目录 → 确定性 tar →
// client-streaming 上传）与 list。deploy --from-dir 复用同一上传 helper。

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// uploadChunkSize 是流式上传的分帧大小（256KiB——单条消息远小于 gRPC
// 收包上限 32MiB；总量上限由服务端字节计数执法）。
const uploadChunkSize = 256 << 10

// tarEpoch 是确定性 tar 的固定时间戳（同内容目录重 tar 得同字节流 → 同
// digest → 服务端内容寻址去重生效）。
var tarEpoch = time.Unix(0, 0).UTC()

// tarDir 把目录写成确定性 tar 流（路径字典序、固定 mtime/uid/gid、仅
// 权限位；.git 目录整棵不进上传——纯 VCS 元数据，构建永不引用）。
func tarDir(root string, w io.Writer) error {
	var rels []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if d.Name() == ".git" {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i] < rels[j] })

	tw := tar.NewWriter(w)
	defer tw.Close() //nolint:errcheck // 错误由 Flush 承载
	for _, rel := range rels {
		full := filepath.Join(root, rel)
		info, serr := os.Lstat(full)
		if serr != nil {
			return serr
		}
		name := filepath.ToSlash(rel)
		hdr := &tar.Header{Name: name, ModTime: tarEpoch, Uid: 0, Gid: 0}
		switch {
		case info.IsDir():
			hdr.Typeflag = tar.TypeDir
			hdr.Name = name + "/"
			hdr.Mode = 0o755
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			hdr.Typeflag = tar.TypeReg
			hdr.Mode = int64(info.Mode().Perm()) //nolint:gosec // 仅权限位
			hdr.Size = info.Size()
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			f, oerr := os.Open(full) //nolint:gosec // 用户显式指定的输入路径
			if oerr != nil {
				return oerr
			}
			_, cerr := io.Copy(tw, f) //nolint:gosec // 有界（文件大小）
			_ = f.Close()
			if cerr != nil {
				return cerr
			}
		default:
			return fmt.Errorf("tarDir: %s is neither a regular file nor a directory (symlinks are not uploaded)", rel)
		}
	}
	return tw.Flush()
}

// uploadDir 把目录流式上传到项目（tar 生成与分帧发送并行——目录不必先
// 全量驻内存）。流式动词：调用方须以 noDeadline 拨号。
func uploadDir(ctx context.Context, c *fleetly.Client, projectID, dir string) (*deliveryv1.UploadSourceResponse, error) {
	stream, err := c.Builds.UploadSource(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&deliveryv1.UploadSourceRequest{
		Part: &deliveryv1.UploadSourceRequest_Meta{Meta: &deliveryv1.UploadSourceMeta{ProjectId: projectID}},
	}); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		if err := tarDir(dir, pw); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	buf := make([]byte, uploadChunkSize)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			if serr := stream.Send(&deliveryv1.UploadSourceRequest{
				Part: &deliveryv1.UploadSourceRequest_Chunk{Chunk: buf[:n]},
			}); serr != nil {
				_ = pr.CloseWithError(serr)
				return nil, serr
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, rerr
		}
	}
	return stream.CloseAndRecv()
}

// newUploadsPutVerb：目录 → tar → 上传（确定性 tar 使同内容重传命中服务
// 端内容寻址去重——deduplicated=true 时返回同一 upload id）。
func newUploadsPutVerb() commands.Command {
	const name = "put"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "Upload a directory as build source (deterministic tar stream)",
		usage:    "uploads put --project PROJECT_ID DIR",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			if len(args) != 1 {
				return usageErr(name, "exactly one DIR argument is required")
			}
			dir := args[0]
			info, err := os.Stat(dir) //nolint:gosec // 用户显式指定的输入路径
			if err != nil {
				return fmt.Errorf("stat %s: %w", dir, err)
			}
			if !info.IsDir() {
				return usageErr(name, "DIR must be a directory")
			}
			// 流式动词：拨号豁免请求级 deadline（上传时长由目录大小与链路
			// 决定，服务端另有 15m 流硬上限）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := uploadDir(ctx, c, project, dir)
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				dedup := ""
				if resp.GetDeduplicated() {
					dedup = ", deduplicated"
				}
				_, _ = fmt.Fprintf(env.Stdout, "upload %s digest %s (%d bytes%s)\n",
					resp.GetId(), resp.GetDigest(), resp.GetSizeBytes(), dedup)
			})
		},
	}
}

// newUploadsListVerb：列项目上传产物（新→旧）。
func newUploadsListVerb() commands.Command {
	const name = "list"
	var project string
	return &flaggedVerb{
		name:     name,
		synopsis: "List uploaded sources for a project",
		usage:    "uploads list --project PROJECT_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Builds.ListUploads(ctx, &deliveryv1.ListUploadsRequest{ProjectId: project})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tDIGEST\tBYTES\tCREATED")
				for _, u := range resp.GetUploads() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%d\t%s\n", u.GetId(), u.GetDigest(), u.GetSizeBytes(), u.GetCreatedAt())
				}
			})
		},
	}
}

// appProjectID 回读 App 的项目（deploy --from-dir 的上传归属锚）。
func appProjectID(ctx context.Context, c *fleetly.Client, appID string) (string, error) {
	resp, err := c.Apps.GetApp(ctx, &structurev1.GetAppRequest{Id: appID})
	if err != nil {
		return "", err
	}
	return resp.GetApp().GetProjectId(), nil
}

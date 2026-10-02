package upload_test

import (
	"archive/tar"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/upload"
)

// buildTar 构造测试 tar 流（entries 顺序书写）。
func buildTar(t *testing.T, fn func(tw *tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fn(tw)
	require.NoError(t, tw.Close())
	return buf.Bytes()
}

func fileHeader(name string, size int64) *tar.Header {
	return &tar.Header{Name: name, Mode: 0o644, Size: size, ModTime: time.Unix(0, 0).UTC()}
}

func TestExtractTarHappy(t *testing.T) {
	dst := t.TempDir()
	body := buildTar(t, func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}))
		require.NoError(t, tw.WriteHeader(fileHeader("Dockerfile", 6)))
		_, err := tw.Write([]byte("FROM x"))
		require.NoError(t, err)
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: "web/", Typeflag: tar.TypeDir, Mode: 0o755}))
		require.NoError(t, tw.WriteHeader(fileHeader("web/index.html", 5)))
		_, err = tw.Write([]byte("hello"))
		require.NoError(t, err)
	})
	require.NoError(t, upload.ExtractTar(dst, bytes.NewReader(body), 0))
	got, err := os.ReadFile(filepath.Join(dst, "Dockerfile")) //nolint:gosec // 测试自有夹具目录
	require.NoError(t, err)
	assert.Equal(t, "FROM x", string(got))
	got, err = os.ReadFile(filepath.Join(dst, "web", "index.html")) //nolint:gosec // 测试自有夹具目录
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
}

func TestExtractTarRejectsUnsafe(t *testing.T) {
	cases := map[string][]byte{
		"parent escape": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(fileHeader("../evil", 1)))
			_, err := tw.Write([]byte("x"))
			require.NoError(t, err)
		}),
		"nested parent escape": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(fileHeader("ok/../../evil", 1)))
			_, err := tw.Write([]byte("x"))
			require.NoError(t, err)
		}),
		"absolute path": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(fileHeader("/etc/passwd", 1)))
			_, err := tw.Write([]byte("x"))
			require.NoError(t, err)
		}),
		"backslash": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(fileHeader("a\\b", 1)))
			_, err := tw.Write([]byte("x"))
			require.NoError(t, err)
		}),
		"symlink": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}))
		}),
		"hardlink": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: "link2", Typeflag: tar.TypeLink, Linkname: "Dockerfile"}))
		}),
		"duplicate entry": buildTar(t, func(tw *tar.Writer) {
			require.NoError(t, tw.WriteHeader(fileHeader("same", 1)))
			_, err := tw.Write([]byte("x"))
			require.NoError(t, err)
			require.NoError(t, tw.WriteHeader(fileHeader("./same", 1)))
			_, err = tw.Write([]byte("y"))
			require.NoError(t, err)
		}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dst := t.TempDir()
			err := upload.ExtractTar(dst, bytes.NewReader(body), 0)
			require.Error(t, err)
			t.Log(err)
		})
	}
}

func TestExtractTarSizeBudget(t *testing.T) {
	dst := t.TempDir()
	body := buildTar(t, func(tw *tar.Writer) {
		require.NoError(t, tw.WriteHeader(fileHeader("big", 16)))
		_, err := tw.Write(bytes.Repeat([]byte("x"), 16))
		require.NoError(t, err)
	})
	err := upload.ExtractTar(dst, bytes.NewReader(body), 8)
	var tooLarge *upload.ErrTooLarge
	require.ErrorAs(t, err, &tooLarge)
	assert.NoFileExists(t, filepath.Join(dst, "big"))
}

// TestRoundTripWithCLIForm 与 CLI 的确定性 tar 形态对齐（目录 → tar →
// ExtractTar 复原），钉住服务端接受 CLI 写出形态的契约。
func TestRoundTripDirectoryForm(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "web"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM alpine\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(src, "web", "index.html"), []byte("hi"), 0o600))

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	entries := []struct {
		name string
		body string
	}{
		{"Dockerfile", "FROM alpine\n"},
		{"web/index.html", "hi"},
	}
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(fileHeader(e.name, int64(len(e.body)))))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	dst := t.TempDir()
	require.NoError(t, upload.ExtractTar(dst, bytes.NewReader(buf.Bytes()), 0))
	for _, e := range entries {
		got, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(e.name))) //nolint:gosec // 测试自有夹具目录
		require.NoError(t, err, fmt.Sprintf("entry %s", e.name))
		assert.Equal(t, e.body, string(got))
	}
}

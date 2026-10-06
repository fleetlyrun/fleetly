package cmd

import (
	"flag"
	"fmt"
	"os"

	"github.com/lynx-go/commands"

	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// 连接参数：解析序 显式旗标 > FLEETLY_* env > 内建默认。CLI 不自动加载
// cwd 的 .env（常在不可信仓库目录运行，自动加载会被恶意 .env 重定向
// endpoint 窃取 Token；torchwood 同款守卫模式，TestMainNoDotEnvAutoLoad
// 随守卫批次落地）。
const (
	envAddr  = "FLEETLY_ADDR"
	envToken = "FLEETLY_TOKEN" //nolint:gosec // 环境变量名，非硬编码凭据

	defaultAddr = "127.0.0.1:9080"
)

// connFlags 是贯穿全部 RPC 动词的连接参数。
type connFlags struct {
	addr  string
	token string
}

// register 把连接旗标挂到动词的 FlagSet；token 默认空串防 -h 回显。
func (c *connFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.addr, "addr", "", fmt.Sprintf("fleetlyd gRPC address (default %q; env %s)", defaultAddr, envAddr))
	fs.StringVar(&c.token, "token", "", fmt.Sprintf("API token (env %s)", envToken))
}

// resolve 按显式旗标 > env > 内建默认归一。
func (c *connFlags) resolve() {
	if c.addr == "" {
		c.addr = envOr(envAddr, defaultAddr)
	}
	if c.token == "" {
		c.token = envOr(envToken, "")
	}
}

func envOr(key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// getenv 是 os.Getenv 的测试接缝（golden 夹具注入 FLEETLY_ADDR 等）。
var getenv = os.Getenv

// dialClient 是 fleetly.Dial 的测试接缝（bufconn 夹具注入；见 status 测试）。
var dialClient = fleetly.Dial

// dial 建立到 fleetlyd 的 SDK 连接。
func (c *connFlags) dial() (*fleetly.Client, error) {
	c.resolve()
	return dialClient(c.addr)
}

// noArgs 校验动词不接受位置参数。
func noArgs(verb string, args []string) error {
	if len(args) > 0 {
		return &commands.UsageError{
			Usage: verb,
			Err:   fmt.Errorf("unexpected argument(s): %v", args),
		}
	}
	return nil
}

// exactArgs 校验位置参数个数（templates show NAME 形态）。
func exactArgs(verb string, want int, args []string) error {
	if len(args) != want {
		return &commands.UsageError{
			Usage: verb,
			Err:   fmt.Errorf("expected %d positional argument(s), got %d: %v", want, len(args), args),
		}
	}
	return nil
}

package guards

import (
	"strings"
	"testing"
)

// 三红线①（无 I/O）的**共享禁列**（R12-L1）。domain 与 capability 两条守卫
// 描述同一条"纯领域/纯类型"纪律，禁列必须单源：旧状 domain 只拦
// `net/http`、capability 拦裸 `net` 却放 `net/url`，两表分叉——将来只改
// 其中一处会加剧分叉。此处收口为一张表：
//
//	os / os.*     进程、文件系统、宿主环境；
//	io/ioutil     弃用包且暗示文件系统面；
//	net / net.*   socket/HTTP 面（net/url 是纯字符串解析，显式白名单）；
//	syscall       裸系统调用面。
//
// 生成物（genproto）与 RPC 框架（grpc）不是"I/O 禁列"，由各守卫自己的
// 红线②/③表执法（capabilityDenyMatchers / frameworkImportMatchers）。
var puritySystemImportMatchers = []string{
	"os",
	"os/",
	"io/ioutil",
	"net",
	"net/",
	"syscall",
}

// puritySystemImportAllow 是共享禁列的显式白名单（前缀即整个子包）：
// net/url 只做 URL 拆解/拼装，零副作用。
var puritySystemImportAllow = []string{"net/url"}

// matchesPuritySystemImport 报告 import 是否命中三红线①（白名单优先）。
func matchesPuritySystemImport(imp string) bool {
	for _, allow := range puritySystemImportAllow {
		if imp == allow || strings.HasPrefix(imp, allow+"/") {
			return false
		}
	}
	return matchesImport(imp, puritySystemImportMatchers)
}

// TestPurityRedlineSharedMatchers 钉共享表的语义与单源事实：
//   - 裸 net/syscall/os.*/io/ioutil 命中，net/url 放行（纯解析），
//     "osmium"/"netx" 这类同前缀异包不误伤；
//   - 两条守卫必须引用同一 helper——任一守卫自建禁列表即红（R12-L1 的
//     分叉回归锚）。
func TestPurityRedlineSharedMatchers(t *testing.T) {
	for _, imp := range []string{"os", "os/exec", "io/ioutil", "net", "net/http", "syscall"} {
		if !matchesPuritySystemImport(imp) {
			t.Errorf("import %q must be denied by the shared red-line table", imp)
		}
	}
	for _, imp := range []string{"net/url", "net/url/query"} {
		if matchesPuritySystemImport(imp) {
			t.Errorf("import %q must be allowed (net/url is pure URL parsing)", imp)
		}
	}
	for _, imp := range []string{"osmium", "netx", "golang.org/x/net"} {
		if matchesPuritySystemImport(imp) {
			t.Errorf("import %q must not be denied by the shared red-line table (non-matching prefix)", imp)
		}
	}
	for _, rel := range []string{"internal/guards/domainpurity_test.go", "internal/guards/capabilitypurity_test.go"} {
		if !strings.Contains(readFileLF(t, rel), "matchesPuritySystemImport(") {
			t.Errorf("%s no longer consumes the shared red-line helper — the two purity guards must share one deny table (R12-L1)", rel)
		}
	}
}

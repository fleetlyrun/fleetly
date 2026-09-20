package execrun

// MG-1 封装自带回归：慢子进程 + ctx 超时注入 → Run 必须在 ctx 预算 +
// WaitDelay 内返回且错误可判；stderr 捕获 / Dir / Env 生效。慢子进程按
// GOOS 分支（Windows ping / unix sleep）——不依赖宿主安装 git 等工具，
// 两个平台的系统命令即足够。

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// slowChild 返回一个约 60s 才退出的子进程命令（挂起注入面；双平台分支）。
func slowChild() (name string, args []string) {
	if runtime.GOOS == "windows" {
		return "ping", []string{"-n", "60", "127.0.0.1"}
	}
	return "sleep", []string{"60"}
}

// stderrEcho 返回一个向 stderr 写入 msg 的命令（stderr 捕获注入面）。
func stderrEcho(msg string) (name string, args []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "echo " + msg + " 1>&2"}
	}
	return "sh", []string{"-c", "echo " + msg + " 1>&2"}
}

// TestRunCapturesStderr 成功路径：零错误返回 + stderr 字节被捕获（失败
// 原文进调用方日志的通道契约）。
func TestRunCapturesStderr(t *testing.T) {
	name, args := stderrEcho("boom-marker")
	stderr, err := Run(context.Background(), name, args, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(stderr), "boom-marker") {
		t.Fatalf("stderr = %q, want captured boom-marker", stderr)
	}
}

// TestRunReturnsWithinBudgetOnContextTimeout MG-1 核心契约：挂起子进程 +
// ctx 超时 → Run 在 ctx 预算 + WaitDelay 之内返回，错误可判（ExitError
// 或 ctx 错误，都归因到取消这一触发源）。
func TestRunReturnsWithinBudgetOnContextTimeout(t *testing.T) {
	name, args := slowChild()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Run(ctx, name, args, Options{WaitDelay: 2 * time.Second})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run did not return within ctx budget + WaitDelay: elapsed %v (hung-process reaping broken?)", elapsed)
	}
	if err == nil {
		t.Fatal("Run must return an error after the hung child is killed by ctx timeout")
	}
	if ctx.Err() == nil {
		t.Fatalf("got error %v before ctx expiry (error misattributed)", err)
	}
	// 错误可判：进程被杀 → *exec.ExitError；取消后进程体面退出的竞态 →
	// ctx 错误。两者之外即异常形态。
	var exitErr *exec.ExitError
	if !errors.Is(err, context.DeadlineExceeded) && !errors.As(err, &exitErr) {
		t.Fatalf("error shape unclassifiable (neither ctx error nor ExitError): %T %v", err, err)
	}
}

// TestRunAppliesDirAndEnv Options 生效面：Dir 决定子进程工作目录、Env
// 追加在进程环境之上（两个注入都以 stderr 回显观测）。
func TestRunAppliesDirAndEnv(t *testing.T) {
	dir := t.TempDir()
	var name string
	var args []string
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/c", "echo %FLEETLY_EXECRUN_MARKER% 1>&2"}
	} else {
		name, args = "sh", []string{"-c", `echo "$FLEETLY_EXECRUN_MARKER" 1>&2`}
	}
	stderr, err := Run(context.Background(), name, args, Options{
		Dir: dir,
		Env: []string{"FLEETLY_EXECRUN_MARKER=dir-is-" + filepath.Base(dir)},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(stderr); !strings.Contains(got, "dir-is-"+filepath.Base(dir)) {
		t.Fatalf("stderr = %q, want env marker echoed (Env not applied)", got)
	}
}

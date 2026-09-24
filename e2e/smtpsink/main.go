// e2e/smtpsink 是 notifications e2e 的假 SMTP sink（观测 §8 通道扩展
// e2e 腿的接收器）：标准库 net.Listen 的最小 SMTP 服务器——canned 应答
// （220/250/354/221），会话全文（命令 + DATA 正文）逐行原样写 stdout。
// e2e 在 dind 内以后台进程跑它并把 stdout 落盘，断言侧 grep 落盘文件。
//
// 仅测试夹具：不进产品构建（e2e/ 目录独立 main 包，go test ./... 不编译
// cmd 面之外的 main 不受影响；notifications.sh 单独 go build 本目录）。
//
// usage: smtpsink -addr 127.0.0.1:2525
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
)

func reply(conn net.Conn, line string) {
	_, _ = fmt.Fprintf(conn, "%s\r\n", line)
}

func handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reply(conn, "220 sink.test ESMTP smtpsink")
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	inData := false
	for sc.Scan() {
		line := sc.Text()
		fmt.Println(line) // 会话全文（命令 + 正文）逐行落 stdout——断言面
		upper := strings.ToUpper(line)
		switch {
		case inData:
			if line == "." {
				inData = false
				reply(conn, "250 OK queued as sink-1")
			}
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			reply(conn, "250-sink.test")
			reply(conn, "250 8BITMIME")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
			reply(conn, "250 OK")
		case strings.HasPrefix(upper, "DATA"):
			inData = true
			reply(conn, "354 end data with <CR><LF>.<CR><LF>")
		case strings.HasPrefix(upper, "QUIT"):
			reply(conn, "221 bye")
			return
		default:
			reply(conn, "250 OK")
		}
	}
}

func main() {
	addr := flag.String("addr", "127.0.0.1:2525", "listen address")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("smtpsink: listen %s: %v", *addr, err)
	}
	fmt.Printf("smtpsink listening on %s\n", ln.Addr())
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatalf("smtpsink: accept: %v", err)
		}
		go handle(conn)
	}
}

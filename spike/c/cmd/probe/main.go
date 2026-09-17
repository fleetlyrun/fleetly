// Command probe is the Spike C evidence helper (single static binary,
// cross-compiled to linux/amd64 by scripts\c0-up.bat and staged into each
// dind at /opt/probe).
//
// Modes:
//
//	ts                       print current unix milliseconds (one integer,
//	                         kernel clock shared by all containers on the host)
//	parse -ts <rfc3339nano>  parse a timestamp (e.g. docker inspect
//	                         .State.FinishedAt), print unix milliseconds
//	stamp write -dir D -token T
//	                         write D/stamp.json {token,host,iso,ms,pid};
//	                         used as the volume-data witness for V6a/V6b
//	stamp read  -dir D       print the stamp file content; exit 0 = ok,
//	                         3 = file missing (empty-volume evidence),
//	                         4 = present but not valid JSON
//
// Design note: services under test intentionally do NOT write stamps
// automatically; every write/read is issued explicitly by the experiment
// scripts so a rescheduled task can never overwrite the evidence of what
// the previous task left in the volume.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "probe: "+format+"\n", args...)
	os.Exit(code)
}

func main() {
	if len(os.Args) < 2 {
		fail(2, "usage: probe <ts|parse|stamp> [flags]")
	}
	switch os.Args[1] {
	case "ts":
		fmt.Println(time.Now().UnixMilli())
	case "parse":
		fs := flag.NewFlagSet("parse", flag.ExitOnError)
		v := fs.String("ts", "", "RFC3339 / RFC3339Nano timestamp to parse")
		_ = fs.Parse(os.Args[2:])
		if *v == "" {
			fail(2, "parse: -ts required")
		}
		t, err := time.Parse(time.RFC3339Nano, *v)
		if err != nil {
			fail(1, "parse: %v", err)
		}
		fmt.Println(t.UnixMilli())
	case "stamp":
		fs := flag.NewFlagSet("stamp", flag.ExitOnError)
		dir := fs.String("dir", "/data", "volume-backed directory holding stamp.json")
		token := fs.String("token", "", "token to record (write mode)")
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() < 1 {
			fail(2, "stamp: subcommand write|read required")
		}
		path := filepath.Join(*dir, "stamp.json")
		switch fs.Arg(0) {
		case "write":
			if *token == "" {
				fail(2, "stamp write: -token required")
			}
			now := time.Now()
			b, _ := json.MarshalIndent(map[string]any{
				"token": *token,
				"host":  shortHost(),
				"iso":   now.UTC().Format(time.RFC3339Nano),
				"ms":    now.UnixMilli(),
				"pid":   os.Getpid(),
			}, "", "  ")
			if err := os.WriteFile(path, b, 0o644); err != nil {
				fail(1, "stamp write: %v", err)
			}
			fmt.Printf("STAMP-WRITTEN %s\n", string(b))
		case "read":
			b, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				fmt.Println("STAMP-MISS (no stamp.json in volume = empty-volume evidence)")
				os.Exit(3)
			}
			if err != nil {
				fail(4, "stamp read: %v", err)
			}
			var chk map[string]any
			if json.Unmarshal(b, &chk) != nil {
				fmt.Printf("STAMP-CORRUPT %s\n", string(b))
				os.Exit(4)
			}
			fmt.Printf("STAMP-OK %s\n", string(b))
		default:
			fail(2, "stamp: unknown subcommand %q", fs.Arg(0))
		}
	default:
		fail(2, "unknown mode %q", os.Args[1])
	}
}

func shortHost() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

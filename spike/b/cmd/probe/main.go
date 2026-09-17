// Command probe is the single multi-mode binary for Spike B experiments.
//
// Modes (first CLI argument):
//
//	server  - fixture app server (env-driven: APP_VERSION / HEALTH_MODE /
//	          LISTEN_DELAY_S / CRASH_ON_START); endpoints /, /health,
//	          /__ctl/healthfail, /__ctl/exit
//	hc      - one-shot health probe: GET -url, exit 0 on 2xx (used as
//	          CMD-SHELL healthcheck inside alpine-based fixtures)
//	lbwatch - LB endpoint watcher: continuously samples the service VIP over
//	          HTTP and the tasks.<svc> DNSRR set; JSONL evidence on stdout
//	client  - HTTP prober (fresh-conn or keep-alive) for Traefik-path
//	          experiments V3/V4; JSONL evidence on stdout
//	cfgsvc  - Traefik HTTP-provider config server: serves <dir>/config.json
//	          re-read from disk on every request (control plane stand-in)
//	resolve - DNS lookup helper for infra sanity checks
//
// All JSONL lines carry both "iso" (RFC3339Nano UTC) and "ms" (unix milli,
// exact in float64) so shell/awk post-analysis can compute deltas against
// docker events timestamps (same kernel clock inside the dind).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probe <server|hc|lbwatch|client|cfgsvc|resolve> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "hc":
		runHC(os.Args[2:])
	case "lbwatch":
		runLBWatch(os.Args[2:])
	case "client":
		runClient(os.Args[2:])
	case "cfgsvc":
		runCfgSvc(os.Args[2:])
	case "resolve":
		runResolve(os.Args[2:])
	case "ts":
		if len(os.Args) < 3 {
			log.Fatal("ts: RFC3339Nano timestamp argument required")
		}
		t, err := time.Parse(time.RFC3339Nano, os.Args[2])
		if err != nil {
			log.Fatalf("ts: %v", err)
		}
		fmt.Println(t.UnixMilli())
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", os.Args[1])
		os.Exit(2)
	}
}

// ---- shared helpers ----

func nowFields() (string, int64) {
	t := time.Now()
	return t.UTC().Format(time.RFC3339Nano), t.UnixMilli()
}

func emit(kv map[string]any) {
	iso, ms := nowFields()
	kv["iso"] = iso
	kv["ms"] = ms
	b, err := json.Marshal(kv)
	if err != nil {
		return
	}
	fmt.Println(string(b))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// ---- server ----

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	port := fs.Int("port", 8080, "listen port")
	fs.Parse(args)

	if os.Getenv("CRASH_ON_START") == "1" {
		fmt.Println("server: CRASH_ON_START=1 -> exiting 1")
		os.Exit(1)
	}
	if d, err := strconv.Atoi(getenv("LISTEN_DELAY_S", "0")); err == nil && d > 0 {
		fmt.Printf("server: warmup delay %ds before binding :%d\n", d, *port)
		time.Sleep(time.Duration(d) * time.Second)
	}
	ver := getenv("APP_VERSION", "unset")
	healthFailsAtStart := getenv("HEALTH_MODE", "ok") == "fail"
	var healthFail atomic.Bool
	healthFail.Store(healthFailsAtStart)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// read per-request so service-level env changes apply to new tasks
		if d, err := strconv.Atoi(getenv("SLOW_MS", "0")); err == nil && d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		host, _ := os.Hostname()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "edgefleet-spike-b version=%s host=%s\n", ver, host)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if healthFail.Load() {
			http.Error(w, "unhealthy (injected)", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/__ctl/healthfail", func(w http.ResponseWriter, r *http.Request) {
		on := r.URL.Query().Get("on") == "1"
		healthFail.Store(on)
		fmt.Fprintf(w, "healthfail=%v\n", on)
	})
	mux.HandleFunc("/__ctl/exit", func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		if code == 0 {
			code = 3
		}
		fmt.Fprintf(w, "exiting %d\n", code)
		os.Exit(code)
	})

	srv := &http.Server{Addr: fmt.Sprintf(":%d", *port), Handler: mux}
	if os.Getenv("GRACEFUL") == "1" {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		go func() {
			s := <-sig
			emit(map[string]any{"ev": "graceful-shutdown-begin", "signal": s.String()})
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx) // stop accepting; finish in-flight requests
			emit(map[string]any{"ev": "graceful-shutdown-done"})
			os.Exit(0)
		}()
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("server: listen: %v", err)
	}
	emit(map[string]any{"ev": "server-listening", "port": *port, "version": ver, "health_fail": healthFailsAtStart})
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: serve: %v", err)
	}
	// Serve returns ErrServerClosed the moment Shutdown closes the listener.
	// In graceful mode the signal goroutine owns process exit (it must keep
	// running to wait for in-flight requests); leaving early here - e.g. a
	// bare os.Exit(0) - silently cancels the drain. That exact bug is what
	// this binary shipped first, and it is why graceful drain failed in the
	// first V4-POST round.
	if os.Getenv("GRACEFUL") != "1" {
		os.Exit(0)
	}
	select {} // block; signal goroutine exits after Shutdown finishes
}

// ---- hc (in-container healthcheck) ----

func runHC(args []string) {
	fs := flag.NewFlagSet("hc", flag.ExitOnError)
	url := fs.String("url", "http://127.0.0.1:8080/health", "URL to probe")
	timeout := fs.Duration("timeout", 2*time.Second, "request timeout")
	fs.Parse(args)
	cl := &http.Client{Timeout: *timeout}
	resp, err := cl.Get(*url)
	if err != nil {
		fmt.Printf("HC ERR %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	fmt.Printf("HC %d %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		os.Exit(0)
	}
	os.Exit(1)
}

// ---- resolve ----

func runResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	host := fs.String("host", "", "hostname to resolve")
	fs.Parse(args)
	if *host == "" {
		log.Fatal("resolve: -host required")
	}
	ips, err := net.LookupHost(*host)
	if err != nil {
		fmt.Printf("RESOLVE ERR %v\n", err)
		os.Exit(1)
	}
	sort.Strings(ips)
	fmt.Printf("RESOLVE %s -> [%s]\n", *host, strings.Join(ips, " "))
}

// ---- version body parsing ----

func parseVersion(body string) string {
	for _, f := range strings.Fields(body) {
		if strings.HasPrefix(f, "version=") {
			return strings.TrimPrefix(f, "version=")
		}
	}
	return "?"
}

// ---- lbwatch (B3 evidence collector) ----

func runLBWatch(args []string) {
	fs := flag.NewFlagSet("lbwatch", flag.ExitOnError)
	svc := fs.String("svc", "", "service DNS name (also drives tasks.<svc>)")
	port := fs.Int("port", 8080, "backend port")
	rate := fs.Int("rate-ms", 100, "HTTP sample period")
	dnsMs := fs.Int("dns-ms", 100, "DNS sample period")
	dur := fs.Duration("dur", 45*time.Second, "total run duration")
	timeout := fs.Duration("timeout", 900*time.Millisecond, "per-request timeout")
	fs.Parse(args)
	if *svc == "" {
		log.Fatal("lbwatch: -svc required")
	}
	emit(map[string]any{"ev": "lbwatch-start", "svc": *svc, "port": *port,
		"rate_ms": *rate, "dns_ms": *dnsMs, "dur": dur.String()})

	stop := make(chan struct{})
	var dnsSeen atomic.Pointer[string]
	var vipSeen atomic.Pointer[string]

	go func() { // DNSRR watcher: tasks.<svc>
		t := time.NewTicker(time.Duration(*dnsMs) * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ips, err := net.LookupHost("tasks." + *svc)
				cur := "ERR:" + errstr(err)
				if err == nil {
					sort.Strings(ips)
					cur = strings.Join(ips, ",")
				}
				if prev := dnsSeen.Load(); prev == nil || *prev != cur {
					dnsSeen.Store(&cur)
					emit(map[string]any{"ev": "dnsrr", "set": cur})
				}
			}
		}
	}()
	go func() { // service VIP watcher
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				ips, err := net.LookupHost(*svc)
				cur := "ERR:" + errstr(err)
				if err == nil {
					sort.Strings(ips)
					cur = strings.Join(ips, ",")
				}
				if prev := vipSeen.Load(); prev == nil || *prev != cur {
					vipSeen.Store(&cur)
					emit(map[string]any{"ev": "vip", "addr": cur})
				}
			}
		}
	}()

	cl := &http.Client{
		Timeout: *timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true, // every sample is an independent new connection
		},
	}
	url := fmt.Sprintf("http://%s:%d/", *svc, *port)
	var total, fails int
	perVer := map[string]int{}
	firstVer := map[string]int64{} // version -> unix ms of first sighting
	deadline := time.Now().Add(*dur)
	for time.Now().Before(deadline) {
		sample := map[string]any{"ev": "req", "url": url}
		resp, err := cl.Get(url)
		if err != nil {
			fails++
			sample["fail"] = errstr(err)
			emit(sample)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			ver := parseVersion(string(body))
			total++
			perVer[ver]++
			sample["status"] = resp.StatusCode
			sample["ver"] = ver
			if resp.StatusCode >= 500 {
				// Traefik gateway errors (502 page) count as failures
				fails++
				sample["bad"] = true
			}
			if _, seen := firstVer[ver]; !seen {
				_, ms := nowFields()
				firstVer[ver] = ms
				sample["first_seen"] = true
			}
			emit(sample)
		}
		d := time.Duration(*rate) * time.Millisecond
		if rest := time.Until(deadline); rest < d {
			d = rest
		}
		time.Sleep(d)
	}
	close(stop)
	emit(map[string]any{"ev": "lbwatch-summary", "total_ok": total, "total_fail": fails,
		"per_version": perVer, "first_seen_ms": firstVer})
}

func errstr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- client (V3/V4 prober) ----

func runClient(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	url := fs.String("url", "http://edge/", "target URL")
	host := fs.String("host", "", "override Host header (Traefik router rule)")
	every := fs.Int("every-ms", 500, "request period")
	dur := fs.Duration("dur", 30*time.Second, "total run duration (0 = use -count)")
	count := fs.Int("count", 0, "number of requests (0 = run for -dur)")
	keepalive := fs.Bool("keepalive", false, "use a pooled keep-alive transport")
	method := fs.String("method", http.MethodGet, "HTTP method (POST is not replayable by transports, so mid-flight backend kills surface as 502)")
	idleTo := fs.Duration("idle-timeout", 10*time.Minute, "keep-alive transport idle timeout")
	timeout := fs.Duration("timeout", 3*time.Second, "per-request timeout")
	fs.Parse(args)

	tr := &http.Transport{}
	if *keepalive {
		tr.MaxIdleConns = 8
		tr.MaxIdleConnsPerHost = 4
		tr.IdleConnTimeout = *idleTo // long on purpose: pool outlives task death
	} else {
		tr.DisableKeepAlives = true
	}
	cl := &http.Client{Timeout: *timeout, Transport: tr}
	emit(map[string]any{"ev": "client-start", "url": *url, "host": *host,
		"keepalive": *keepalive, "every_ms": *every, "dur": dur.String(), "count": *count})

	var total, fails int
	perVer := map[string]int{}
	failSamples := 0
	runUntil := time.Now().Add(*dur)
	for i := 0; *count == 0 || i < *count; i++ {
		if *count == 0 && time.Now().After(runUntil) {
			break
		}
		req, err := http.NewRequest(*method, *url, nil)
		if err != nil {
			log.Fatalf("client: %v", err)
		}
		if *host != "" {
			req.Host = *host
		}
		sample := map[string]any{"ev": "req"}
		resp, err := cl.Do(req)
		if err != nil {
			fails++
			failSamples++
			sample["fail"] = errstr(err)
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			ver := parseVersion(string(body))
			total++
			perVer[ver]++
			sample["status"] = resp.StatusCode
			sample["ver"] = ver
			if ver == "?" {
				sample["body"] = strings.TrimSpace(string(body))
			}
			if resp.StatusCode >= 500 {
				fails++ // Traefik gateway error page (502 etc.)
				failSamples++
				sample["bad"] = true
			}
		}
		// print every sample when keep-alive (sparse) or when interesting
		if *keepalive || err != nil || sample["ver"] != nil {
			emit(sample)
		}
		time.Sleep(time.Duration(*every) * time.Millisecond)
	}
	emit(map[string]any{"ev": "client-summary", "total_ok": total, "total_fail": fails,
		"per_version": perVer, "keepalive": *keepalive, "url": *url, "host": *host})
}

// ---- cfgsvc (Traefik HTTP provider stand-in) ----

func runCfgSvc(args []string) {
	fs := flag.NewFlagSet("cfgsvc", flag.ExitOnError)
	dir := fs.String("dir", "/data", "directory holding config.json")
	file := fs.String("file", "config.json", "config file name")
	addr := fs.String("addr", ":9000", "listen address")
	fs.Parse(args)
	path := *dir + "/" + *file

	mux := http.NewServeMux()
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		_, ms := nowFields()
		if err != nil {
			fmt.Printf("CFG ms=%d GET err=%v\n", ms, err)
			http.Error(w, "config unreadable: "+errstr(err), http.StatusInternalServerError)
			return
		}
		fmt.Printf("CFG ms=%d GET bytes=%d\n", ms, len(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	fmt.Printf("cfgsvc serving %s on %s\n", path, *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

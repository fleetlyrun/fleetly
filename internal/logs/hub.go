package logs

import (
	"context"
	"sync"
)

// hub 是 per app-service 流的注册表：ring（回放）+ 订阅者扇出（Follow
// 实时）。键 = app + "\x00" + service（service 维度有界：应用 × compose
// 服务，单机规模常数级）。
type hub struct {
	mu      sync.Mutex
	streams map[string]*ring
	subs    map[int64]*subscriber
	subSeq  int64
	size    int
}

type subscriber struct {
	app     string
	service string // 空 = 该 app 全部服务
	ch      chan Entry
}

func newHub(ringSize int) *hub {
	return &hub{
		streams: make(map[string]*ring),
		subs:    make(map[int64]*subscriber),
		size:    ringSize,
	}
}

func streamKey(app, service string) string { return app + "\x00" + service }

// ingest 追加一条到对应 ring 并扇出给匹配订阅者（慢订阅者丢帧——日志扇
// 出尽力而为，断线由 Follow 客户端重连回放补齐；ring 不阻塞采集）。
func (h *hub) ingest(e Entry) {
	h.mu.Lock()
	key := streamKey(e.App, e.Service)
	r, ok := h.streams[key]
	if !ok {
		r = newRing(h.size)
		h.streams[key] = r
	}
	r.append(e)
	for _, s := range h.subs {
		if s.app != e.App {
			continue
		}
		if s.service != "" && s.service != e.Service {
			continue
		}
		select {
		case s.ch <- e:
		default: // 慢订阅者丢帧（不阻塞采集）
		}
	}
	h.mu.Unlock()
}

// subscribe 注册订阅者并回放既有 ring（该 app，或指定 service）；返回的
// channel 由 unsubscribe/cancel 关闭。回放与注册同锁完成，回放与实时之间
// 不重不漏（回放快照后追加的条目才会走扇出）。
func (h *hub) subscribe(app, service string) (<-chan Entry, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subSeq++
	id := h.subSeq
	ch := make(chan Entry, 256)
	h.subs[id] = &subscriber{app: app, service: service, ch: ch}
	// 回放：单服务订阅回放该服务 ring；全服务订阅按 key 前缀聚合回放。
	if service != "" {
		if r, ok := h.streams[streamKey(app, service)]; ok {
			for _, e := range r.snapshot() {
				ch <- e
			}
		}
	} else {
		prefix := app + "\x00"
		for key, r := range h.streams {
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				for _, e := range r.snapshot() {
					ch <- e
				}
			}
		}
	}
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if s, ok := h.subs[id]; ok {
			close(s.ch)
			delete(h.subs, id)
		}
	}
	return ch, cancel
}

// follow 订阅并在 ctx 结束时自动注销（Follow gRPC 取消语义的管线侧承载）。
func (h *hub) follow(ctx context.Context, app, service string) (<-chan Entry, func()) {
	ch, cancel := h.subscribe(app, service)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, cancel
}

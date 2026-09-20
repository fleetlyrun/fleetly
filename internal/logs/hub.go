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

// evictApps 删除指定 app 集的全部 per-service ring（M7-6 生命周期回收：
// 淘汰计时在 manager 侧统一判定，hub 不引入时钟/active 集依赖——本方法
// 只做纯删除）。订阅者不受影响：subs 独立保留，ring 没了只影响新订阅者
// 的回放（为空），实时扇出继续；app 回归后首条日志即重建 ring（首启语义）。
func (h *hub) evictApps(apps []string) {
	if len(apps) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, app := range apps {
		prefix := app + "\x00"
		for key := range h.streams {
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				delete(h.streams, key)
			}
		}
	}
}

// subscribe 注册订阅者并回放既有 ring（该 app，或指定 service）；返回的
// channel 由 unsubscribe/cancel 关闭。回放与注册同锁完成，回放与实时之间
// 不重不漏（回放快照后追加的条目才会走扇出）。
func (h *hub) subscribe(app, service string) (<-chan Entry, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subSeq++
	id := h.subSeq
	// 回放：单服务订阅回放该服务 ring；全服务订阅按 key 前缀聚合回放。
	// 先在锁内收集全部待回放切片，按回放总量决定 channel 容量，使锁内
	// 发送永不阻塞——否则积压超过缓冲（256）时消费方在 subscribe 返回
	// 后才读 channel，持锁阻塞发送即死锁，ingest 同锁连带停摆（H1）。
	// 切片收集、注册与发送全程同锁，回放与实时之间不重不漏的语义不变。
	var replay [][]Entry
	if service != "" {
		if r, ok := h.streams[streamKey(app, service)]; ok {
			replay = append(replay, r.snapshot())
		}
	} else {
		prefix := app + "\x00"
		for key, r := range h.streams {
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				replay = append(replay, r.snapshot())
			}
		}
	}
	total := 0
	for _, snap := range replay {
		total += len(snap)
	}
	capacity := 256
	if total > capacity {
		capacity = total
	}
	ch := make(chan Entry, capacity)
	h.subs[id] = &subscriber{app: app, service: service, ch: ch}
	for _, snap := range replay {
		for _, e := range snap {
			ch <- e // 容量 >= 回放总量，锁内发送不阻塞
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

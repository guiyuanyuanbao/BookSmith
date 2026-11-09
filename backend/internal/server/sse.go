package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// newTicker 定时器（keep-alive 心跳）。
func newTicker(ctx context.Context) *time.Ticker {
	_ = ctx
	return time.NewTicker(15 * time.Second)
}

// handleEvents SSE 事件流（任务进度/日志）。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "不支持流式响应")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	subID, ch := s.app.Bus.Subscribe()
	defer s.app.Bus.Unsubscribe(subID)

	// 初始注释行（建立连接）
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepAlive := newTicker(r.Context())
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}

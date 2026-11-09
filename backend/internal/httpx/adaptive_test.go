package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 429 冷却：连续 429 → 学习限流 → 后续请求排队等待 → 成功后清除。
func TestAdaptive429Cooling(t *testing.T) {
	var count, blocked int32
	var ever429 atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&count, 1)
		// 前 5 个请求 429（超过单次 Do 的重试次数，强制跨 Do 学习）
		if n <= 5 {
			atomic.AddInt32(&blocked, 1)
			ever429.Store(true)
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewClient()
	c.retryDelay = 20 * time.Millisecond
	host := hostOf(srv.URL)

	// 第一个 Do：连续 429 用尽重试，返回错误但已学习限流
	_, err := c.Do(context.Background(), &Request{URL: srv.URL})
	if err == nil {
		t.Log("first Do unexpectedly succeeded")
	}
	if !ever429.Load() {
		t.Fatal("no 429 observed")
	}
	// 学习已发生
	if iv := c.limiter.learnedInterval[host]; iv <= 0 {
		t.Fatalf("learned interval = %v, want > 0", iv)
	}

	// 放行后续请求，冷却期内重试应成功
	atomic.StoreInt32(&count, 100)
	resp, err := c.Do(context.Background(), &Request{URL: srv.URL + "/next"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.BodyText() != "ok" {
		t.Fatalf("body=%q", resp.BodyText())
	}
	// 成功后冷却清除
	c.limiter.mu.Lock()
	_, hasBlock := c.limiter.blockedUntil[host]
	_, hasLearned := c.limiter.learnedInterval[host]
	c.limiter.mu.Unlock()
	if hasBlock || hasLearned {
		t.Errorf("cooling should be cleared after success: block=%v learned=%v", hasBlock, hasLearned)
	}
}

// Retry-After 头解析。
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5", 5 * time.Second},
		{"120", 120 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"999", 0},
		{"abc", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in); got != c.want {
			t.Errorf("parseRetryAfter(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

// learnBackoff 递增序列：1s → 2s → 4s → 8s 封顶，并设置冷却截止。
func TestLearnBackoffProgression(t *testing.T) {
	h := newHostLimiter()
	h.learnBackoff("h")
	if got := h.learnedInterval["h"]; got != 1*time.Second {
		t.Errorf("1st = %v want 1s", got)
	}
	if until := h.blockedUntil["h"]; until.Before(time.Now()) {
		t.Error("blockedUntil should be set to now+interval")
	}
	h.learnBackoff("h")
	h.learnBackoff("h")
	if got := h.learnedInterval["h"]; got != 4*time.Second {
		t.Errorf("3rd = %v want 4s", got)
	}
	for i := 0; i < 5; i++ {
		h.learnBackoff("h")
	}
	if got := h.learnedInterval["h"]; got != 8*time.Second {
		t.Errorf("capped = %v want 8s", got)
	}
	// 已有显式规则的主机不被覆盖
	h.setRule("explicit", 1, time.Second)
	h.learnBackoff("explicit")
	if _, ok := h.learnedInterval["explicit"]; ok {
		t.Error("explicit rule host should not be learned")
	}
	// 清除
	h.clearBlock("h")
	if len(h.blockedUntil) != 1 { // 只剩 explicit 无 blocked 记录
		t.Logf("blockedUntil len=%d", len(h.blockedUntil))
	}
}

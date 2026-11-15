package server

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCoverFetchDeduped 并发同 URL miss 只抓一次（发现页一屏 40+ <img> 同封面场景）。
func TestCoverFetchDeduped(t *testing.T) {
	var fetches atomic.Int32
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		time.Sleep(50 * time.Millisecond) // 制造并发窗口
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0fakejpeg"))
	}))
	defer imgSrv.Close()

	app, _ := newCoverTestApp(t)
	app.AllowPrivateCoverFetch = true
	app.StartCoverProxy()

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _, ok := app.fetchCoverDeduped(imgSrv.URL + "/cover.jpg")
			if !ok || len(data) == 0 {
				t.Error("应成功获取封面")
			}
		}()
	}
	wg.Wait()

	if got := fetches.Load(); got != 1 {
		t.Fatalf("并发 %d 请求应只抓取 1 次，实际 %d 次", n, got)
	}
}

// TestCoverFetchSemaphore 全局并发信号量：限制同时抓取数。
func TestCoverFetchSemaphore(t *testing.T) {
	var concurrent, peak atomic.Int32
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := concurrent.Add(1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		concurrent.Add(-1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0j"))
	}))
	defer imgSrv.Close()

	app, _ := newCoverTestApp(t)
	app.AllowPrivateCoverFetch = true
	app.StartCoverProxy()

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = app.fetchCoverDeduped(imgSrv.URL + "/c" + time.Unix(0, int64(i)).String() + ".jpg")
		}(i)
	}
	wg.Wait()

	if p := peak.Load(); p > coverFetchConcurrency {
		t.Fatalf("峰值并发 %d 超过信号量上限 %d", p, coverFetchConcurrency)
	}
}

package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEncodingByName(t *testing.T) {
	for name := range map[string]bool{"gbk": true, "GBK": true, "gb2312": true, "gb18030": true, "big5": true, "utf-8": true} {
		if EncodingByName(name) == nil {
			t.Errorf("encoding %q should exist", name)
		}
	}
	if EncodingByName("no-such-charset-xyz") != nil {
		t.Error("unknown charset should return nil")
	}
}

func TestDecodeBodyGBK(t *testing.T) {
	// "中文" 的 GBK 编码字节
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	got, err := DecodeBody(gbk, "text/html; charset=gbk")
	if err != nil {
		t.Fatal(err)
	}
	if got != "中文" {
		t.Fatalf("got %q want 中文", got)
	}
	// 无 Content-Type，HTML meta 声明
	html := append([]byte(`<html><head><meta charset="gbk">`), gbk...)
	got, err = DecodeBody(html, "text/html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "中文") {
		t.Fatalf("meta charset detection failed, got %q", got)
	}
}

func TestDecodeBodyBOM(t *testing.T) {
	// UTF-16LE: BOM(FF FE) + 中(2D 4E) + 文(87 65)
	utf16 := []byte{0xFF, 0xFE, 0x2D, 0x4E, 0x87, 0x65}
	got, _ := DecodeBody(utf16, "")
	if got != "中文" {
		t.Fatalf("got %q", got)
	}
	// UTF-16BE: BOM(FE FF) + 中(4E 2D)
	utf16be := []byte{0xFE, 0xFF, 0x4E, 0x2D}
	got, _ = DecodeBody(utf16be, "")
	if got != "中" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeBodyUTF8NoBOM(t *testing.T) {
	got, _ := DecodeBody([]byte("你好，世界"), "text/html; charset=utf-8")
	if got != "你好，世界" {
		t.Fatalf("got %q", got)
	}
}

func TestClientGetDecode(t *testing.T) {
	enc := func(s string) []byte {
		e := EncodingByName("gbk")
		b, _ := e.NewEncoder().String(s)
		return []byte(b)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("UA should be set by default")
		}
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		_, _ = w.Write(enc("<html>正文内容</html>"))
	}))
	defer srv.Close()

	c := NewClient()
	resp, err := c.Do(context.Background(), &Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(resp.BodyText(), "正文内容") {
		t.Fatalf("decoded body wrong: %q", resp.BodyText())
	}
}

func TestClientRetryOn500(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count < 3 {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewClient(WithTimeout(2 * time.Second))
	c.retryDelay = time.Millisecond
	resp, err := c.Do(context.Background(), &Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if resp.BodyText() != "ok" || count != 3 {
		t.Fatalf("retry failed: count=%d body=%q", count, resp.BodyText())
	}
}

func TestClientRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewClient()
	c.SetRateLimit(hostOf(srv.URL), 1, 100*time.Millisecond) // 每 100ms 一次
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.Do(context.Background(), &Request{URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
	}
	cost := time.Since(start)
	if cost < 200*time.Millisecond {
		t.Fatalf("rate limit not effective: %v", cost)
	}
}

func TestParseConcurrentRate(t *testing.T) {
	cases := []struct {
		in           string
		count        int
		windowMillis int64
	}{
		{"10/60000", 10, 60000},
		{"0", 0, 0},
		{"", 0, 0},
		{"bad", 0, 0},
		{"1/1000", 1, 1000},
	}
	for _, c := range cases {
		n, w := ParseConcurrentRate(c.in)
		if n != c.count || int64(w/time.Millisecond) != c.windowMillis {
			t.Errorf("ParseConcurrentRate(%q) = (%d,%v) want (%d,%d)", c.in, n, w, c.count, c.windowMillis)
		}
	}
}

func TestParseHeaders(t *testing.T) {
	h := ParseHeaders(`{"User-Agent":"test/1.0","Referer":"https://a.com"}`)
	if h["User-Agent"] != "test/1.0" || h["Referer"] != "https://a.com" {
		t.Fatalf("json parse failed: %v", h)
	}
	h = ParseHeaders("User-Agent: line/1.0\nCookie: a=b; c=d")
	if h["User-Agent"] != "line/1.0" || h["Cookie"] != "a=b; c=d" {
		t.Fatalf("line parse failed: %v", h)
	}
	if len(ParseHeaders("")) != 0 {
		t.Fatal("empty should be empty map")
	}
}

func TestAbsoluteURL(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"https://a.com/b/c.html", "/d/e.html", "https://a.com/d/e.html"},
		{"https://a.com/b/", "e.html", "https://a.com/b/e.html"},
		{"https://a.com", "https://b.com/x", "https://b.com/x"},
		{"https://a.com/b/", "//c.com/x", "https://c.com/x"},
		{"https://a.com/b/c.html", "?q=1", "https://a.com/b/c.html?q=1"},
	}
	for _, c := range cases {
		if got := AbsoluteURL(c.base, c.ref); got != c.want {
			t.Errorf("AbsoluteURL(%q,%q)=%q want %q", c.base, c.ref, got, c.want)
		}
	}
}

func TestRandomUA(t *testing.T) {
	for i := 0; i < 20; i++ {
		ua := RandomUA()
		if !strings.HasPrefix(ua, "Mozilla/5.0") {
			t.Fatalf("bad UA %q", ua)
		}
	}
}

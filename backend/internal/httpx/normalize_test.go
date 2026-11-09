package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NormalizeURL：双协议头折叠（真实书源 JS 拼出 https:https:// 触发 206 章正文为空）。
func TestNormalizeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://a.com/x", "https://a.com/x"},
		{"http://a.com", "http://a.com"},
		{"https:https://a.com/x", "https://a.com/x"},
		{"http:http://a.com", "http://a.com"},
		{"https:http://a.com", "http://a.com"}, // 内层 URL 优先
		{"HTTP:HTTPS://a.com", "https://a.com"},
		{"https:HTTPS://a.com/x", "https://a.com/x"},
		{"https:https:https://a.com", "https://a.com"},
		{"  https://a.com  ", "https://a.com"},
		{"", ""},
		{"/relative/path", "/relative/path"},
	}
	for _, c := range cases {
		if got := NormalizeURL(c.in); got != c.want {
			t.Errorf("NormalizeURL(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

// java.ajax 场景：双协议头 URL 经规范化后正常抓取。
func TestClientDoNormalizesDoubleScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("_txt_call({content:'正文内容'})"))
	}))
	defer srv.Close()

	broken := "http:http://" + srv.URL[len("http://"):] + "/data.dat"
	c := NewClient()
	resp, err := c.Do(context.Background(), &Request{URL: broken})
	if err != nil {
		t.Fatalf("double-scheme URL should work after normalize: %v", err)
	}
	if resp.StatusCode != 200 || len(resp.Body) == 0 {
		t.Fatalf("status=%d body=%q", resp.StatusCode, resp.Body)
	}
}

package jsenv

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"booksmith/internal/httpx"
	"booksmith/internal/model"
	"booksmith/internal/varstore"
)

func TestJsBasicCrypto(t *testing.T) {
	env := New(httpx.NewClient(), t.TempDir())
	ext := env.NewExtensions(nil, nil, "https://a.com")
	if got := ext.Base64Encode("hello"); got != "aGVsbG8=" {
		t.Errorf("b64enc = %q", got)
	}
	if got := ext.Base64Decode("aGVsbG8="); got != "hello" {
		t.Errorf("b64dec = %q", got)
	}
	// URL-safe 无 padding
	if got := ext.Base64Decode("aGVsbG8"); got != "hello" {
		t.Errorf("b64dec lenient = %q", got)
	}
	if got := ext.Md5Encode("abc"); got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("md5 = %q", got)
	}
	if got := ext.Md5Encode16("abc"); len(got) != 16 {
		t.Errorf("md5_16 = %q", got)
	}
	if got := ext.DigestHex("abc", "SHA-256"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %q", got)
	}
}

func TestJsTimeFormat(t *testing.T) {
	env := New(httpx.NewClient(), "")
	ext := env.NewExtensions(nil, nil, "")
	// 本地时区 2024-01-02 03:04:05
	want := time.Date(2024, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli()
	got := ext.TimeFormat(want)
	if got != "2024-01-02 03:04:05" {
		t.Errorf("timeFormat = %q", got)
	}
}

func TestJsAjax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("响应内容"))
	}))
	defer srv.Close()

	env := New(httpx.NewClient(), "")
	ext := env.NewExtensions(&model.BookSource{BookSourceURL: srv.URL}, nil, srv.URL)
	got := ext.Ajax(srv.URL + "/x")
	if got != "响应内容" {
		t.Errorf("ajax = %q", got)
	}
	// connect
	resp := ext.Connect(srv.URL + "/y")
	if resp.Body != "响应内容" {
		t.Errorf("connect body = %q", resp.Body)
	}
	if resp.Headers.Get("Content-Type") == "" {
		t.Errorf("headers missing")
	}
}

func TestJsEngineEval(t *testing.T) {
	env := New(httpx.NewClient(), t.TempDir())
	engine := env.Engine()

	// 变量与算术
	v, err := engine.Eval("1+2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%v", v) != "3" {
		t.Errorf("eval = %v", v)
	}
	// java 方法经 proxy 调用
	vars := varstore.New(nil)
	ext := env.NewExtensions(nil, vars, "")
	v, err = engine.Eval(`java.put('k', 'val') + java.get('k')`, map[string]any{"java": ext})
	if err != nil {
		t.Fatal(err)
	}
	if v != "valval" {
		t.Errorf("java put/get = %v", v)
	}
	if vars.Get("k") != "val" {
		t.Errorf("stored = %q", vars.Get("k"))
	}
	// cookie proxy
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	v, err = engine.Eval(`cookie.setCookie(baseUrl, 'token=abc'); cookie.getCookie(baseUrl)`,
		map[string]any{"java": ext, "baseUrl": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprintf("%v", v), "token=abc") {
		t.Errorf("cookie = %v", v)
	}
	// cache proxy
	v, err = engine.Eval(`cache.put('c', 'cached', 60); cache.get('c')`, map[string]any{"java": ext})
	if err != nil {
		t.Fatal(err)
	}
	if v != "cached" {
		t.Errorf("cache = %v", v)
	}
}

func TestJsEngineScriptCache(t *testing.T) {
	env := New(httpx.NewClient(), t.TempDir())
	engine := env.Engine()
	for i := 0; i < 5; i++ {
		v, err := engine.Eval("40+2", nil)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%v", v) != "42" {
			t.Fatalf("iter %d = %v", i, v)
		}
	}
}

func TestJsSourceObject(t *testing.T) {
	env := New(httpx.NewClient(), "")
	bs := &model.BookSource{BookSourceURL: "https://s.com", BookSourceName: "测试源"}
	ext := env.NewExtensions(bs, varstore.New(nil), bs.BookSourceURL)
	engine := env.Engine()
	src := NewJSSource(bs, ext)
	v, err := engine.Eval(`source.bookSourceUrl + "|" + (source.put('x','1'), source.get('x'))`,
		map[string]any{"java": ext, "source": src})
	if err != nil {
		t.Fatal(err)
	}
	if v != "https://s.com|1" {
		t.Errorf("source obj = %v", v)
	}
}

func TestJsBookChapterObjects(t *testing.T) {
	env := New(httpx.NewClient(), "")
	ext := env.NewExtensions(nil, nil, "")
	engine := env.Engine()
	book := &JSBook{Name: "斗破苍穹", Author: "天蚕土豆", BookUrl: "https://a.com/b/1"}
	chapter := &JSChapter{Title: "第一章", Url: "https://a.com/c/1", Index: 0}
	v, err := engine.Eval(`book.name + "/" + book.author + "/" + chapter.title`,
		map[string]any{"java": ext, "book": book, "chapter": chapter})
	if err != nil {
		t.Fatal(err)
	}
	if v != "斗破苍穹/天蚕土豆/第一章" {
		t.Errorf("objects = %v", v)
	}
}

func TestChineseConvert(t *testing.T) {
	env := New(httpx.NewClient(), "")
	ext := env.NewExtensions(nil, nil, "")
	s2t := ext.S2T("简体中文")
	if s2t == "" {
		t.Fatal("s2t empty")
	}
	t2s := ext.T2S(s2t)
	if t2s != "简体中文" {
		t.Errorf("t2s roundtrip = %q", t2s)
	}
}

func TestToNumChapter(t *testing.T) {
	env := New(httpx.NewClient(), "")
	ext := env.NewExtensions(nil, nil, "")
	cases := []struct{ in, want string }{
		{"第一百二十三章 测试", "第123章 测试"},
		{"第十二章", "第12章"},
		{"第三章 两仪", "第3章 两仪"},
	}
	for _, c := range cases {
		if got := ext.ToNumChapter(c.in); got != c.want {
			t.Errorf("ToNumChapter(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestCookieStore(t *testing.T) {
	cs := NewCookieStore("")
	cs.SetCookie("https://www.a.com/x", "a=1; b=2")
	if got := cs.GetCookie("https://sub.a.com/y"); got != "a=1; b=2" {
		t.Errorf("cross-subdomain cookie = %q", got)
	}
	if got := cs.GetKey("https://a.com", "b"); got != "2" {
		t.Errorf("key = %q", got)
	}
	cs.ReplaceCookie("https://a.com", "a", "9")
	if got := cs.GetKey("https://a.com", "a"); got != "9" {
		t.Errorf("replaced = %q", got)
	}
	cs.RemoveCookie("https://a.com", "b")
	if got := cs.GetKey("https://a.com", "b"); got != "" {
		t.Errorf("removed = %q", got)
	}
}

func TestCacheManagerPersist(t *testing.T) {
	dir := t.TempDir()
	cm := NewCacheManager(dir)
	cm.Put("k1", "v1", 60)
	cm2 := NewCacheManager(dir)
	if got := cm2.Get("k1"); got != "v1" {
		t.Errorf("persisted = %q", got)
	}
	// 过期
	cm.Put("k2", "v2", -1) // 已过期
	if got := cm.Get("k2"); got != "" {
		t.Errorf("expired = %q", got)
	}
	// 内存
	cm.PutMemory("m", "mv")
	if got := cm.GetFromMemory("m"); got != "mv" {
		t.Errorf("memory = %q", got)
	}
	cm.DeleteMemory("m")
	if got := cm.GetFromMemory("m"); got != "" {
		t.Errorf("deleted memory = %q", got)
	}
}

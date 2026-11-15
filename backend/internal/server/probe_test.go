package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/store"
	"booksmith/internal/webbook"
)

// probeTestEnv 探针测试环境：DB + mock 书源站（详情页与目录页分离）。
type probeTestEnv struct {
	st    *store.Store
	srv   *httptest.Server
	mux   *http.ServeMux
	app   *App
	srvr  *Server
}

func newProbeTestEnv(t *testing.T) *probeTestEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	client := httpx.NewClient()
	app := &App{
		Store:    st,
		DataDir:  dir,
		WebBook:  webbook.New(client, jsenv.New(client, dir)),
		CoverClient: &http.Client{},
	}
	srvr := New(app)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 目录页：1 卷 + 3 章（卷条目在章节列表内，isVolume 规则文本为 true）
	tocHTML := `<html><body>
	<li class="ch"><a>第一卷</a><div class="isvol">true</div></li>
	<li class="ch"><a href="/c/1.html">第一章</a></li>
	<li class="ch"><a href="/c/2.html">第二章</a></li>
	<li class="ch"><a href="/c/3.html">第三章</a></li>
	</body></html>`

	// 详情页：含书名（analyzeBookInfo 要求非空）+ ruleBookInfo.tocUrl 指向目录页
	// （探针必须先解析它——sfacg 类源的标准写法）
	detailHTML := `<html><body>
	<h1 class="title">测试书名</h1>
	<span class="author">测试作者</span>
	<div id="BasicOperation"><a href="/toc/1.html">目录</a></div>
	</body></html>`

	mux.HandleFunc("GET /detail/1.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(detailHTML))
	})
	mux.HandleFunc("GET /toc/1.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(tocHTML))
	})

	env := &probeTestEnv{st: st, srv: srv, mux: mux, app: app, srvr: srvr}

	// 注册书源：ruleBookInfo.tocUrl 配置目录在另一页（sfacg 类源的标准写法）
	bs := &model.BookSource{
		BookSourceURL:  srv.URL,
		BookSourceName: "tocUrl测试源",
		Enabled:        true,
		SearchURL:      "/search?q={{key}}",
		RuleSearch:     &model.RuleSearch{BookList: "class.item", Name: "class.title@text", BookURL: "tag.a.0@href"},
		RuleBookInfo: &model.RuleBookInfo{
			Name:   "class.title@text",
			Author: "class.author@text",
			TocURL: "#BasicOperation a.0@href",
		},
		RuleToc: &model.RuleToc{
			ChapterList: "class.ch",
			ChapterName: "tag.a.0@text",
			ChapterURL:  "tag.a.0@href",
			IsVolume:    "class.isvol@text",
		},
	}
	if err := st.UpsertSource(bs, false); err != nil {
		t.Fatal(err)
	}
	return env
}

func probeReq(t *testing.T, srvr *Server, sourceURL, bookURL string) TocProbeResult {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/probe-toc", srvr.handleProbeToc)
	body := fmt.Sprintf(`{"sourceUrl": %q, "bookUrl": %q}`, sourceURL, bookURL)
	req := httptest.NewRequest("POST", "/api/probe-toc", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("probe 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data TocProbeResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data
}

// TestProbeToc_ResolvesTocURL 目录在另一页的源（ruleBookInfo.tocUrl 配置）：
// 探针必须先经详情页解析 tocUrl 再取目录——否则误报「目录解析为空」
// （历史 bug：探针直接把 bookUrl 当 tocUrl，与真实下载路径不一致）。
func TestProbeToc_ResolvesTocURL(t *testing.T) {
	env := newProbeTestEnv(t)
	r := probeReq(t, env.srvr, env.srv.URL, env.srv.URL+"/detail/1.html")

	if !r.OK {
		t.Fatalf("探测应成功（tocUrl 已解析），实际错误: %s", r.Err)
	}
	if r.Total != 3 {
		t.Fatalf("应解析出 3 章，实际 %d", r.Total)
	}
	if r.VolumeCount != 1 {
		t.Fatalf("应识别 1 个卷，实际 %d", r.VolumeCount)
	}
	if len(r.FirstChapters) != 3 || r.FirstChapters[0] != "第一章" {
		t.Fatalf("首章列表错误: %v", r.FirstChapters)
	}
}

// TestProbeToc_LastChaptersNeverNull 章节数 ≤3 时 lastChapters 必须是 [] 而非 null：
// nil 切片序列化为 null，前端 probeBadge/目录预览对 null 取 .length 会抛
// TypeError → Vue 渲染崩溃 → 下载弹窗内容消失而遮罩残留（历史 bug）。
func TestProbeToc_LastChaptersNeverNull(t *testing.T) {
	env := newProbeTestEnv(t)
	r := probeReq(t, env.srvr, env.srv.URL, env.srv.URL+"/detail/1.html")
	if !r.OK {
		t.Fatal(r.Err)
	}
	// Go 侧切片必须非 nil
	if r.LastChapters == nil || r.FirstChapters == nil {
		t.Fatal("FirstChapters/LastChapters 应初始化为非 nil 切片")
	}
	// JSON 序列化必须是 [] 而非 null
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), `"lastChapters":null`) || strings.Contains(string(b), `"firstChapters":null`) {
		t.Fatalf("序列化出现 null 数组: %s", string(b))
	}
}

// TestProbeToc_CachedResult 频繁重复探测命中缓存（不重复抓取源站）。
func TestProbeToc_CachedResult(t *testing.T) {
	env := newProbeTestEnv(t)
	r1 := probeReq(t, env.srvr, env.srv.URL, env.srv.URL+"/detail/1.html")
	if !r1.OK {
		t.Fatal(r1.Err)
	}
	r2 := probeReq(t, env.srvr, env.srv.URL, env.srv.URL+"/detail/1.html")
	if !r2.Cached {
		t.Fatal("第二次探测应命中缓存")
	}
}

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"booksmith/internal/model"
	"booksmith/internal/store"
)

// newCoverTestApp 建立带真实 Store 的测试 App。
func newCoverTestApp(t *testing.T) (*App, *Server) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bscover")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	app := &App{Store: st, DataDir: dir}
	return app, New(app)
}

// mustBook 落库一本测试书。
func mustBook(t *testing.T, app *App, name, author, origin, coverURL, localCover string) *model.Book {
	t.Helper()
	b := &model.Book{
		BookURL: "http://example.com/book/" + name + "/" + origin, Origin: origin, OriginName: "源" + origin,
		Name: name, Author: author, CoverURL: coverURL, LocalCover: localCover,
	}
	id, err := app.Store.UpsertBook(b)
	if err != nil {
		t.Fatal(err)
	}
	b.ID = id
	return b
}

func TestPreserveGroupCovers_RemainingSourceHasCover(t *testing.T) {
	app, _ := newCoverTestApp(t)
	// 两个源都有 CoverURL：删 A 后 B 的封面仍可达，不应产生本地封面
	a := mustBook(t, app, "测试书A", "作者", "http://a.com", "http://a.com/cover.jpg", "")
	b := mustBook(t, app, "测试书A", "作者", "http://b.com", "http://b.com/cover.jpg", "")

	s := &Server{app: app}
	s.preserveGroupCovers([]int64{a.ID})

	if _, err := app.Store.GetBook(b.ID); err != nil {
		t.Fatalf("剩余源不应被删除: %v", err)
	}
	rest, err := app.Store.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rest.LocalCover != "" {
		t.Errorf("剩余源仍有远程封面，不应转存本地封面，实际 %q", rest.LocalCover)
	}
}

func TestPreserveGroupCovers_LastSourceNoCover(t *testing.T) {
	app, _ := newCoverTestApp(t)
	// 唯一来源且无封面：整本书删除，封面无需保留
	a := mustBook(t, app, "测试书B", "作者", "http://a.com", "", "")
	s := &Server{app: app}
	s.preserveGroupCovers([]int64{a.ID})
	// 不 panic 即可（无剩余源时直接跳过）
	_ = a
}

func TestPreserveGroupCovers_TransfersLocalCoverToRemaining(t *testing.T) {
	app, srv := newCoverTestApp(t)
	// A 有本地上传封面，B 无任何封面；删除 A 后封面文件应转移给 B
	if err := srv.ensureUploadsDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srv.localCoverPath("1.jpg"), []byte("fakeimg"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := mustBook(t, app, "测试书C", "作者", "http://a.com", "", "1.jpg")
	b := mustBook(t, app, "测试书C", "作者", "http://b.com", "", "")

	s := &Server{app: srv.app}
	s.preserveGroupCovers([]int64{a.ID})

	rest, err := app.Store.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rest.LocalCover == "" {
		t.Fatal("本地上传封面应转移到剩余源记录")
	}
	data, err := os.ReadFile(srv.localCoverPath(rest.LocalCover))
	if err != nil || string(data) != "fakeimg" {
		t.Fatalf("转移后的封面文件应可读且内容一致: %v", err)
	}
}

func TestPreserveGroupCovers_DownloadsCoverWhenNoRemaining(t *testing.T) {
	app, srv := newCoverTestApp(t)
	// 远程封面服务
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0fakejpeg"))
	}))
	defer imgSrv.Close()

	a := mustBook(t, app, "测试书D", "作者", "http://a.com", imgSrv.URL+"/cover.jpg", "")
	b := mustBook(t, app, "测试书D", "作者", "http://b.com", "", "")

	app.StartCoverProxy()
	s := &Server{app: srv.app}
	s.preserveGroupCovers([]int64{a.ID})

	rest, err := app.Store.GetBook(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rest.LocalCover == "" {
		t.Fatal("远程封面应下载转存为剩余源的本地封面")
	}
	if _, err := os.Stat(srv.localCoverPath(rest.LocalCover)); err != nil {
		t.Fatalf("转存文件应存在: %v", err)
	}
}

func TestPickCoverCandidate(t *testing.T) {
	mk := func(name, author, cover string, rt int64) *model.SearchOrigin {
		return &model.SearchOrigin{Name: name, Author: author, CoverURL: cover, RespondTime: rt}
	}
	items := []*model.SearchOrigin{
		mk("测试书X其他版", "甲", "http://c1", 100),
		mk("测试书X", "乙", "http://c2", 500),   // 同名，作者不同
		mk("测试书X", "作者", "http://c3", 900),   // 同名同作者（最匹配）
		mk("测试书X", "作者", "", 50),             // 最匹配但无封面
	}
	got := pickCoverCandidate(items, "测试书X", "作者")
	if got == nil || got.CoverURL != "http://c3" {
		t.Fatalf("应选同名同作者且有封面的条目，实际 %+v", got)
	}
	// 全部无封面 → nil
	if got := pickCoverCandidate([]*model.SearchOrigin{mk("x", "y", "", 1)}, "x", "y"); got != nil {
		t.Fatal("无封面候选应返回 nil")
	}
	// 无同名时回退到任意有封面的
	got2 := pickCoverCandidate([]*model.SearchOrigin{mk("别的书", "甲", "http://c9", 10)}, "测试书X", "作者")
	if got2 == nil || got2.CoverURL != "http://c9" {
		t.Fatal("无精确匹配时应回退到首个有封面的条目")
	}
}

func TestHandleCoverFetch_NoSources(t *testing.T) {
	app, srv := newCoverTestApp(t)
	b := mustBook(t, app, "测试书E", "作者", "http://a.com", "", "")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/books/{id}/cover/fetch", srv.handleCoverFetch)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/books/%d/cover/fetch", b.ID), strings.NewReader("{}"))
	req.SetPathValue("id", fmt.Sprintf("%d", b.ID))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// 无启用书源 → 400
	if rec.Code != 400 {
		t.Fatalf("无书源时应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "书源") {
		t.Fatalf("错误信息应提示书源缺失: %s", rec.Body.String())
	}
}

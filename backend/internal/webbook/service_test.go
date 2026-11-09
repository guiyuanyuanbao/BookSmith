package webbook

import (
	"context"
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/replace"
	"booksmith/internal/testutil"
)

func newTestService(t *testing.T) (*Service, *testutil.FixtureSite) {
	t.Helper()
	env := jsenv.New(httpx.NewClient(), t.TempDir())
	svc := New(httpx.NewClient(), env)
	svc.ReplaceProvider = func() []*model.ReplaceRule { return nil }
	fs := testutil.NewFixtureSite()
	t.Cleanup(fs.Server.Close)
	return svc, fs
}

func TestSearchOneHTMLSource(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	items, err := svc.SearchOne(context.Background(), bs, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	// 精确搜索：非匹配书名被过滤
	items2, err := svc.SearchOne(context.Background(), bs, "斗破", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items2) != 1 || items2[0].Name != "斗破苍穹" {
		t.Errorf("precision items = %+v", items2)
	}
	first := items[0]
	if first.Name != "斗破苍穹" {
		t.Errorf("name = %q", first.Name)
	}
	if first.Author != "天蚕土豆" {
		t.Errorf("author (清洗后) = %q", first.Author)
	}
	if first.BookURL != fs.Base+"/book/1.html" {
		t.Errorf("bookUrl = %q", first.BookURL)
	}
	if first.Kind != "玄幻" {
		t.Errorf("kind = %q", first.Kind)
	}
}

func TestSearchOneGBK(t *testing.T) {
	// GBK 编码站点自动解码
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	items, err := svc.SearchOne(context.Background(), bs, "斗破", 1)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Name != "斗破苍穹" {
		t.Errorf("gbk decode failed: %q", items[0].Name)
	}
}

func TestSearchOnePostSource(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixturePostSource(fs.Base)
	items, err := svc.SearchOne(context.Background(), bs, "测试", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Name != "测试传奇" {
		t.Errorf("name = %q", items[0].Name)
	}
}

func TestSearchOneJSONSource(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureJSONSource(fs.Base)
	items, err := svc.SearchOne(context.Background(), bs, "搜索", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Name != "搜索之JSON书" || items[0].Author != "json作者" {
		t.Errorf("json item = %+v", items[0])
	}
	if items[0].BookURL != fs.Base+"/api/book/5" {
		t.Errorf("bookUrl = %q", items[0].BookURL)
	}
}

func TestAggregateSearchDedup(t *testing.T) {
	svc, fs := newTestService(t)
	// 两个源返回同书（同名同作者）→ 聚合去重
	bs1 := testutil.FixtureHTMLSource(fs.Base)
	bs2 := testutil.FixturePostSource(fs.Base)
	agg := svc.Search(context.Background(), []*model.BookSource{bs1, bs2}, "", nil)
	if agg.Searched != 2 {
		t.Errorf("searched = %d", agg.Searched)
	}
	// 每本书聚合为一项（不同书各自成项）
	merged := 0
	for _, it := range agg.Items {
		if len(it.Origins) > 1 {
			merged++
		}
	}
	_ = merged
	// 斗破（源1）与 测试传奇（源2）是不同书 → 至少 2 项
	if len(agg.Items) < 3 {
		t.Fatalf("items = %d", len(agg.Items))
	}
	// 同名同作者合并：再搜一次同书名
	bs3 := testutil.FixtureHTMLSource(fs.Base)
	bs3.BookSourceName = "fixture2"
	bs3.BookSourceURL = fs.Base + "/v2/"
	agg2 := svc.Search(context.Background(), []*model.BookSource{bs1, bs3}, "斗破", nil)
	found := false
	for _, it := range agg2.Items {
		if it.Name == "斗破苍穹" {
			found = true
			if len(it.Origins) != 2 {
				t.Errorf("origins = %d want 2", len(it.Origins))
			}
		}
	}
	if !found {
		t.Error("斗破苍穹 not found in aggregate")
	}
}

func TestGetBookInfo(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book, err := svc.GetBookInfo(context.Background(), bs, fs.Base+"/book/1.html")
	if err != nil {
		t.Fatal(err)
	}
	if book.Name != "斗破苍穹" || book.Author != "天蚕土豆" {
		t.Errorf("book = %+v", book)
	}
	if !strings.Contains(book.Intro, "萧炎的奋斗史") || strings.Contains(book.Intro, "&nbsp;") {
		t.Errorf("intro = %q", book.Intro)
	}
	if book.TocURL != fs.Base+"/book/1.html" {
		t.Errorf("tocUrl = %q", book.TocURL)
	}
}

func TestGetBookInfoJSON(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureJSONSource(fs.Base)
	book, err := svc.GetBookInfo(context.Background(), bs, fs.Base+"/api/book/5")
	if err != nil {
		t.Fatal(err)
	}
	if book.Name != "测试之JSON书" {
		t.Errorf("name = %q", book.Name)
	}
	if book.TocURL != fs.Base+"/api/toc/5" {
		t.Errorf("tocUrl = %q", book.TocURL)
	}
}

func TestGetToc(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{BookURL: fs.Base + "/book/1.html", TocURL: fs.Base + "/book/1.html"}
	chapters, err := svc.GetToc(context.Background(), bs, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 {
		t.Fatalf("chapters = %d", len(chapters))
	}
	if chapters[0].Title != "第一章 陨落的天才" {
		t.Errorf("first = %q", chapters[0].Title)
	}
	if chapters[0].URL != fs.Base+"/book/1/1.html" {
		t.Errorf("url = %q", chapters[0].URL)
	}
}

func TestGetTocPaginationDedup(t *testing.T) {
	svc, fs := newTestService(t)
	// 目录翻页源：第 4 本，目录页 1（2 章）+ 页 2（2 章，1 章重复）→ 去重后 3 章
	book := &model.Book{
		BookURL: fs.Base + "/book/4.html",
		TocURL:  fs.Base + "/toc/4-1.html",
	}
	// 需要 nextTocUrl 规则的源
	bs2 := testutil.FixtureHTMLSource(fs.Base)
	bs2.BookSourceURL = fs.Base + "/p/"
	bs2.RuleToc.NextTocURL = "class.next@href"
	chapters, err := svc.GetToc(context.Background(), bs2, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 3 {
		t.Fatalf("chapters = %d want 3 (含翻页与去重)", len(chapters))
	}
	titles := []string{}
	for _, ch := range chapters {
		titles = append(titles, ch.Title)
	}
	// 去重：第十章 只保留一次
	count10 := 0
	for _, ti := range titles {
		if strings.Contains(ti, "第十章") {
			count10++
		}
	}
	if count10 != 1 {
		t.Errorf("第十章出现 %d 次", count10)
	}
}

func TestGetTocJSON(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureJSONSource(fs.Base)
	book := &model.Book{BookURL: fs.Base + "/api/book/5", TocURL: fs.Base + "/api/book/5"}
	chapters, err := svc.GetToc(context.Background(), bs, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 2 || chapters[0].Title != "第1章" {
		t.Fatalf("chapters = %+v", chapters)
	}
	if chapters[0].URL != fs.Base+"/api/ch/1" {
		t.Errorf("url = %q", chapters[0].URL)
	}
}

func TestGetContentWithPagination(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{BookURL: fs.Base + "/book/1.html", TocURL: fs.Base + "/book/1.html", Name: "斗破苍穹"}
	chapter := &model.Chapter{Index: 0, URL: fs.Base + "/book/1/1.html", Title: "第一章"}
	text, err := svc.GetContent(context.Background(), bs, book, chapter)
	if err != nil {
		t.Fatal(err)
	}
	// 正文分页内容合并
	if !strings.Contains(text, "萧炎盯着头顶的修炼台") || !strings.Contains(text, "这是第二页内容") {
		t.Errorf("text = %q", text)
	}
	// nextContentUrl 与下一章 URL 相同时终止（此处 1_2 是分页不是下一章，应包含）
}

func TestGetContentSimple(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "凡人修仙传", BookURL: fs.Base + "/book/2.html", TocURL: fs.Base + "/book/2.html"}
	chapter := &model.Chapter{Index: 0, URL: fs.Base + "/book/2/9.html"}
	text, err := svc.GetContent(context.Background(), bs, book, chapter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "结丹成功") {
		t.Errorf("text = %q", text)
	}
}

func TestGetContentJSON(t *testing.T) {
	svc, fs := newTestService(t)
	bs := testutil.FixtureJSONSource(fs.Base)
	book := &model.Book{Origin: bs.BookSourceURL, BookURL: fs.Base + "/api/book/5", TocURL: fs.Base + "/api/book/5"}
	chapter := &model.Chapter{Index: 0, URL: fs.Base + "/api/ch/1"}
	text, err := svc.GetContent(context.Background(), bs, book, chapter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "这是JSON源的第一段") || !strings.Contains(text, "这是第二段") {
		t.Errorf("json content = %q", text)
	}
}

func TestContentReplaceRules(t *testing.T) {
	svc, fs := newTestService(t)
	svc.ReplaceProvider = func() []*model.ReplaceRule {
		return []*model.ReplaceRule{{
			Name: "净化", Pattern: `这是第二页内容。`, Replacement: "[已净化]", IsRegex: true, Enabled: true, ScopeContent: true,
		}}
	}
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "斗破苍穹", Origin: bs.BookSourceURL, BookURL: fs.Base + "/book/1.html", TocURL: fs.Base + "/book/1.html"}
	chapter := &model.Chapter{Index: 0, URL: fs.Base + "/book/1/1.html"}
	text, err := svc.GetContent(context.Background(), bs, book, chapter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[已净化]") || strings.Contains(text, "这是第二页内容") {
		t.Errorf("replace not applied: %q", text)
	}
}

func TestProcessorExport(t *testing.T) {
	// 确保 replace 包导出可用
	p := replace.NewProcessor(nil, nil, nil)
	if p == nil {
		t.Fatal("processor nil")
	}
}

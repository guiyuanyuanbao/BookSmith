package webbook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"booksmith/internal/httpx"
	"booksmith/internal/model"
)

// fixtureSite 构造一个模拟书站（GBK 编码、目录翻页、正文分页、广告、JSON API 源）。
type fixtureSite struct {
	Server *httptest.Server
	Base   string
}

// newFixtureSite 启动模拟书站。
func newFixtureSite() *fixtureSite {
	fs := &fixtureSite{}
	mux := http.NewServeMux()

	// ---------- HTML 源 ----------
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		_, _ = w.Write(gbkEncode(fmt.Sprintf(`<html><head><meta charset="gbk"><title>搜索:%s</title></head><body>
<div class="result-list">
  <div class="book-item">
    <h3 class="bk-name"><a href="/book/1.html">斗破苍穹</a></h3>
    <span class="bk-author">作者：天蚕土豆 著</span>
    <span class="bk-kind">玄幻</span>
    <span class="bk-latest"><a href="/book/1/3.html">第三章 突破</a></span>
    <div class="bk-intro">斗气大陆的传奇故事</div>
  </div>
  <div class="book-item">
    <h3 class="bk-name"><a href="/book/2.html">凡人修仙传</a></h3>
    <span class="bk-author">忘语</span>
    <span class="bk-kind">仙侠</span>
    <span class="bk-latest"><a href="/book/2/9.html">第九章 结丹</a></span>
    <div class="bk-intro">一个凡人的修仙之路</div>
  </div>
</div>
</body></html>`, q)))
	})

	mux.HandleFunc("/search-post", func(w http.ResponseWriter, r *http.Request) {
		var form map[string]string
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &form)
		q := form["searchkey"]
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fmt.Sprintf(`<html><body><div class="post-list">
<div class="row"><a class="t" href="/book/3.html">%s传奇</a><span class="a">post作者</span><span class="l">第1章 开篇</span></div>
</div></body></html>`, q)))
	})

	mux.HandleFunc("/book/1.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>斗破苍穹</title></head><body>
<div id="info">
  <h1>斗破苍穹</h1>
  <p>作者：天蚕土豆</p>
  <div id="intro"><p>讲述了天才少年萧炎的奋斗史&nbsp;&nbsp;额外实体信息。</p></div>
  <p class="latest"><a href="/book/1/3.html">第三章 突破</a></p>
</div>
<div id="list">
  <dl>
    <dt>正文卷</dt>
    <dd><a href="/book/1/1.html">第一章 陨落的天才</a></dd>
    <dd><a href="/book/1/2.html">第二章 斗气大陆</a></dd>
    <dd><a href="/book/1/3.html">第三章 突破</a></dd>
  </dl>
</div>
</body></html>`))
	})

	mux.HandleFunc("/toc/4-1.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<div class="mulu" id="list">
  <dl>
    <dd><a href="/book/4/10.html">第十章 上</a></dd>
    <dd><a href="/book/4/11.html">第十一章 上</a></dd>
  </dl>
  <a class="next" href="/toc/4-2.html">下一页</a>
</div>
</body></html>`))
	})
	mux.HandleFunc("/toc/4-2.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<div class="mulu" id="list">
  <dl>
    <dd><a href="/book/4/12.html">第十二章 下</a></dd>
    <dd><a href="/book/4/10.html">第十章 上</a></dd>
  </dl>
</div>
</body></html>`))
	})
	mux.HandleFunc("/book/4.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div class="mulu"><a href="/toc/4-1.html">目录</a></div></body></html>`))
	})

	chapterBody := func(page, totalPages int) string {
		var sb strings.Builder
		sb.WriteString(`<html><body><div id="content">`)
		if page == 1 {
			sb.WriteString(`第一章 陨落的天才<br/><br/>萧炎盯着头顶的修炼台。<br/><br/>家族中人的议论声。`)
		} else {
			sb.WriteString(`这是第二页内容。`)
		}
		sb.WriteString(`</div>`)
		if page < totalPages {
			sb.WriteString(`<a id="pb_next" href="/book/1/1_` + strconv.Itoa(page+1) + `.html">下一页</a>`)
		}
		sb.WriteString(`</body></html>`)
		return sb.String()
	}
	mux.HandleFunc("/book/1/1.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(chapterBody(1, 2)))
	})
	mux.HandleFunc("/book/1/1_2.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(chapterBody(2, 2)))
	})
	mux.HandleFunc("/book/1/2.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="content">第二章 斗气大陆<br/><br/>斗之气三段。</div></body></html>`))
	})
	mux.HandleFunc("/book/1/3.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="content">第三章 突破<br/><br/>突破斗者！</div></body></html>`))
	})
	mux.HandleFunc("/book/2/9.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="content">第九章 结丹<br/><br/>结丹成功。</div></body></html>`))
	})
	mux.HandleFunc("/book/2.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="info"><h1>凡人修仙传</h1><p>作者：忘语</p><div id="intro">凡人流开山之作</div></div>
<div id="list"><dl><dd><a href="/book/2/9.html">第九章 结丹</a></dd></dl></div></body></html>`))
	})

	// ---------- JSON API 源 ----------
	mux.HandleFunc("/api/search", func(w http.ResponseWriter, r *http.Request) {
		kw := r.URL.Query().Get("kw")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"data":{"books":[{"name":"%s之JSON书","author":"json作者","intro":"json简介","bookUrl":"/api/book/5","cover":"https://cdn.example.com/c.jpg"}]}}`, kw)))
	})
	mux.HandleFunc("/api/book/5", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"name":"测试之JSON书","author":"json作者","intro":"详细简介","tocUrl":"/api/toc/5","latest":"第2章","chapters":[{"title":"第1章","url":"/api/ch/1"},{"title":"第2章","url":"/api/ch/2"}]}}`))
	})
	mux.HandleFunc("/api/ch/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"title":"第1章","content":"这是JSON源的第一段。\n这是第二段。"}}`))
	})
	mux.HandleFunc("/api/ch/2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"title":"第2章","content":"JSON源第二章内容。"}}`))
	})

	fs.Server = httptest.NewServer(mux)
	fs.Base = fs.Server.URL
	return fs
}

func gbkEncode(s string) []byte {
	enc := httpx.EncodingByName("gbk")
	b, _ := enc.NewEncoder().String(s)
	return []byte(b)
}

// fixtureHTMLSource HTML 规则书源（指向 fixture 站点）。
func fixtureHTMLSource(base string) *model.BookSource {
	return &model.BookSource{
		BookSourceURL:  base + "/",
		BookSourceName: "fixture",
		Enabled:        true,
		SearchURL:      base + "/search?q={{key}}",
		RuleSearch: &model.RuleSearch{
			BookList:    "class.book-item",
			Name:        "class.bk-name@tag.a@text",
			Author:      "class.bk-author@text",
			Kind:        "class.bk-kind@text",
			LastChapter: "class.bk-latest@tag.a@text",
			BookURL:     "class.bk-name@tag.a@href",
			Intro:       "class.bk-intro@text",
		},
		RuleBookInfo: &model.RuleBookInfo{
			Name:        "id.info@tag.h1@text",
			Author:      "id.info@tag.p.0@text",
			Intro:       "id.intro@text",
			LastChapter: "class.latest@tag.a@text",
		},
		RuleToc: &model.RuleToc{
			ChapterList: "id.list@tag.dd",
			ChapterName: "tag.a@text",
			ChapterURL:  "tag.a@href",
		},
		RuleContent: &model.RuleContent{
			Content:        "id.content@textNodes",
			NextContentURL: "id.pb_next@href",
		},
	}
}

// fixturePostSource POST 搜索书源。
func fixturePostSource(base string) *model.BookSource {
	return &model.BookSource{
		BookSourceURL:  base + "/post",
		BookSourceName: "post源",
		Enabled:        true,
		SearchURL:      base + "/search-post,{\"method\":\"POST\",\"body\":{\"searchkey\":\"{{key}}\"}}",
		RuleSearch: &model.RuleSearch{
			BookList:    "class.row",
			Name:        "class.t@text",
			Author:      "class.a@text",
			LastChapter: "class.l@text",
			BookURL:     "class.t@href",
		},
		RuleBookInfo: &model.RuleBookInfo{Name: "id.info@tag.h1@text"},
		RuleToc: &model.RuleToc{
			ChapterList: "id.list@tag.dd",
			ChapterName: "tag.a@text",
			ChapterURL:  "tag.a@href",
		},
		RuleContent: &model.RuleContent{Content: "id.content@textNodes"},
	}
}

// fixtureJSONSource JSON API 书源（js 规则）。
func fixtureJSONSource(base string) *model.BookSource {
	return &model.BookSource{
		BookSourceURL:  base + "/api",
		BookSourceName: "json源",
		Enabled:        true,
		SearchURL:      base + "/api/search?kw={{key}}",
		RuleSearch: &model.RuleSearch{
			BookList: "$.data.books[*]",
			Name:     "$.name",
			Author:   "$.author",
			Intro:    "$.intro",
			BookURL:  "$.bookUrl",
			CoverURL: "$.cover",
		},
		RuleBookInfo: &model.RuleBookInfo{
			Init:        "$.data",
			Name:        "$.name",
			Author:      "$.author",
			Intro:       "$.intro",
			LastChapter: "$.latest",
			TocURL:      "$.tocUrl",
		},
		RuleToc: &model.RuleToc{
			ChapterList: "$.data.chapters[*]",
			ChapterName: "$.title",
			ChapterURL:  "$.url",
		},
		RuleContent: &model.RuleContent{
			Content: "$.data.content",
			Title:   "$.data.title",
		},
	}
}

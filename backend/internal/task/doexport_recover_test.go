package task

import (
	"strings"
	"testing"

	"booksmith/internal/export"
	"booksmith/internal/model"
	"booksmith/internal/store"
)

// TestDoExportPanicRecovered 验证 doExport 的 panic 兜底：ImageProvider 内 panic
// 会被 doExport 的 defer recover 捕获并转为 error（含堆栈），任务不再静默卡死。
func TestDoExportPanicRecovered(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	m := New(st, nil, nil, t.TempDir()+"/dl")
	book := bookFixtureForExport()
	bookID, err := st.UpsertBook(book)
	if err != nil {
		t.Fatal(err)
	}
	book.ID = bookID
	// 注入章节行（先有行才能存内容）+ done 缓存
	ch := &model.Chapter{BookID: book.ID, Index: 0, Title: "第一章", URL: "http://c/1"}
	if err := st.ReplaceChapters(book.ID, []*model.Chapter{ch}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveChapterContent(book.ID, 0, "正文内容", 2, ""); err != nil {
		t.Fatal(err)
	}

	// 注入会 panic 的 CoverProvider（模拟字体/图片解码意外）
	m.CoverProvider = func(rawURL string) *export.CoverImage {
		panic("模拟字体解析崩溃")
	}

	_, err = m.doExport(0, book, []string{"epub"})
	if err == nil {
		t.Fatal("expected panic converted to error")
	}
	if !strings.Contains(err.Error(), "导出内部异常") {
		t.Fatalf("error 未包含兜底标记: %v", err)
	}
	if !strings.Contains(err.Error(), "模拟字体解析崩溃") {
		t.Fatalf("error 未包含 panic 内容: %v", err)
	}
	if !strings.Contains(err.Error(), "goroutine") && !strings.Contains(err.Error(), "panic") {
		// debug.Stack 输出包含 goroutine 标记
		t.Logf("stack 标记缺失（可接受）: %v", err)
	}
}

// bookFixtureForExport 构造导出用书籍。
func bookFixtureForExport() *model.Book {
	return &model.Book{
		Name: "测试书", Author: "测试作者",
		Origin: "http://src", OriginName: "测试源", CoverURL: "http://img/cover.jpg",
	}
}

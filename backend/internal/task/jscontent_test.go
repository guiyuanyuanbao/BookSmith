package task

import (
	"path/filepath"
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/store"
	"booksmith/internal/testutil"
	"booksmith/internal/webbook"
)

// JS 正文管线端到端：<js> + java.ajax + eval 解码（模拟艾途小说模式），含 502 重试。
func TestJSAjaxContentPipeline(t *testing.T) {
	testutil.ResetFlaky()
	st, err := store.Open(filepath.Join(t.TempDir(), "js.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	env := jsenv.New(httpx.NewClient(), t.TempDir())
	svc := webbook.New(httpx.NewClient(), env)
	m := New(st, svc, NewEventBus(), t.TempDir())
	m.ChapterRetries = 3
	fs := testutil.NewFixtureSite()
	defer fs.Server.Close()

	bs := testutil.FixtureJSAjaxSource(fs.Base)
	book := &model.Book{Name: "JS源之书", BookURL: fs.Base + "/jsajax/book/1.html"}
	id, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitDone(t, m, id)
	if tk.Status != model.TaskDone {
		t.Fatalf("js task failed: %+v", tk)
	}
	ch, err := st.GetChapter(tk.BookID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Content, "JS解码后的第一段") {
		t.Errorf("js content = %q", ch.Content)
	}
}

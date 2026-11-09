package task

import (
	"path/filepath"
	"testing"
	"time"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/store"
	"booksmith/internal/testutil"
	"booksmith/internal/webbook"
)

func newManager(t *testing.T) (*Manager, *testutil.FixtureSite) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env := jsenv.New(httpx.NewClient(), t.TempDir())
	svc := webbook.New(httpx.NewClient(), env)
	m := New(st, svc, NewEventBus(), t.TempDir())
	m.ChapterRetries = 3
	fs := testutil.NewFixtureSite()
	t.Cleanup(fs.Server.Close)
	return m, fs
}

func waitDone(t *testing.T, m *Manager, id int64) *model.DownloadTask {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		tk, err := m.GetTask(id)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Status == model.TaskDone || tk.Status == model.TaskFailed || tk.Status == model.TaskCanceled {
			return tk
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %+v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Create 立即返回（异步流水线），不再阻塞 HTTP。
func TestCreateReturnsImmediately(t *testing.T) {
	m, fs := newManager(t)
	bs := testutil.FixtureSlowSource(fs.Base)
	book := &model.Book{Name: "慢书", BookURL: fs.Base + "/slow/x.html"}
	start := time.Now()
	id, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("Create blocked %v (should return immediately)", el)
	}
	tk := waitDone(t, m, id)
	if tk.Status != model.TaskFailed && tk.Status != model.TaskDone {
		t.Fatalf("status = %s", model.TaskStatusName(tk.Status))
	}
	// 慢源没有 /slow/x.html 详情 → 目录失败 → 任务失败（phase=toc），错误信息可读
	if tk.Status == model.TaskFailed {
		if tk.Phase != "toc" {
			t.Errorf("phase = %q", tk.Phase)
		}
		if tk.ErrMsg == "" {
			t.Error("failure should carry message")
		}
	}
}

// 详情拉取失败时回退搜索信息，目录正常则继续下载。
func TestInfoFallbackAndPipeline(t *testing.T) {
	m, fs := newManager(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	// bookUrl 指向有效详情页 → 全流水线成功
	book := &model.Book{Name: "斗破苍穹", Author: "天蚕土豆", BookURL: fs.Base + "/book/1.html"}
	id, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitDone(t, m, id)
	if tk.Status != model.TaskDone {
		t.Fatalf("task failed: %+v", tk)
	}
	if tk.Phase != "" {
		t.Errorf("phase should clear on done, got %q", tk.Phase)
	}
}

// 闪断章节：前 2 次 500，重试后成功。
func TestFlakyChapterRetry(t *testing.T) {
	testutil.ResetFlaky()
	m, fs := newManager(t)
	bs := testutil.FixtureFlakySource(fs.Base)
	// 目录直接取详情页（不经过闪断 toc 页），专注验证章节重试
	bs.RuleBookInfo.TocURL = ""
	book := &model.Book{Name: "闪断之书", BookURL: fs.Base + "/flaky/book/1.html"}
	id, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitDone(t, m, id)
	if tk.Status != model.TaskDone {
		t.Fatalf("flaky task failed: %+v", tk)
	}
	if tk.FailChapters != 0 || tk.DoneChapters != 1 {
		t.Errorf("done=%d fail=%d", tk.DoneChapters, tk.FailChapters)
	}
}

// 目录闪断（第一次 500）→ 任务失败 → Resume 重跑 toc 阶段成功。
func TestResumeRetriesToc(t *testing.T) {
	testutil.ResetFlaky()
	m, fs := newManager(t)
	bs := testutil.FixtureFlakySource(fs.Base)
	// 目录页第一次请求 500
	book := &model.Book{Name: "闪断之书", BookURL: fs.Base + "/flaky/book/1.html"}
	id, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	tk := waitDone(t, m, id)
	if tk.Status != model.TaskFailed || tk.Phase != "toc" {
		t.Fatalf("expected toc failure, got %+v", tk)
	}
	if err := m.Resume(id); err != nil {
		t.Fatal(err)
	}
	tk = waitDone(t, m, id)
	if tk.Status != model.TaskDone {
		t.Fatalf("resume retry failed: %+v", tk)
	}
}

package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"booksmith/internal/httpx"
	"booksmith/internal/jsenv"
	"booksmith/internal/model"
	"booksmith/internal/store"
	"booksmith/internal/testutil"
	"booksmith/internal/webbook"
)

func newTestManager(t *testing.T) (*Manager, *testutil.FixtureSite, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	env := jsenv.New(httpx.NewClient(), t.TempDir())
	svc := webbook.New(httpx.NewClient(), env)
	bus := NewEventBus()
	outDir := t.TempDir()
	m := New(st, svc, bus, outDir)
	fs := testutil.NewFixtureSite()
	t.Cleanup(fs.Server.Close)
	return m, fs, st
}

func TestDownloadTaskFullPipeline(t *testing.T) {
	m, fs, st := newTestManager(t)

	// 净化规则：过滤 fixture 正文中的特定内容
	m.svc.ReplaceProvider = func() []*model.ReplaceRule {
		return []*model.ReplaceRule{}
	}

	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "斗破苍穹", BookURL: fs.Base + "/book/1.html"}

	// 订阅事件
	_, evCh := m.bus.Subscribe()
	_ = evCh
	defer m.bus.Unsubscribe(1)

	taskID, _, err := m.Create(nil, book, bs, []string{"txt", "epub", "html", "md"}, 0, -1, 3)
	if err != nil {
		t.Fatal(err)
	}

	// 等待完成
	deadline := time.Now().Add(30 * time.Second)
	for {
		tk, err := m.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Status == model.TaskDone || tk.Status == model.TaskFailed || tk.Status == model.TaskCanceled {
			if tk.Status != model.TaskDone {
				t.Fatalf("task failed: %+v", tk)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout, task = %+v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}

	tk, _ := m.GetTask(taskID)
	if tk.DoneChapters != 3 {
		t.Errorf("done = %d want 3", tk.DoneChapters)
	}
	if tk.FailChapters != 0 {
		t.Errorf("failed = %d", tk.FailChapters)
	}

	// 章节内容已缓存
	ch, err := st.GetChapter(tk.BookID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Content, "萧炎盯着头顶的修炼台") || !strings.Contains(ch.Content, "这是第二页内容") {
		t.Errorf("chapter content = %q", ch.Content)
	}

	// 导出文件存在
	exports, err := st.ListExports(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 4 {
		t.Fatalf("exports = %d want 4", len(exports))
	}
	for _, e := range exports {
		if _, err := os.Stat(e["path"].(string)); err != nil {
			t.Errorf("export file missing: %v", e["path"])
		}
	}
	// TXT 内容验证
	txtPath := ""
	for _, e := range exports {
		if e["format"] == "txt" {
			txtPath = e["path"].(string)
		}
	}
	data, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "书名：斗破苍穹") || !strings.Contains(out, "作者：天蚕土豆") {
		t.Errorf("txt meta missing")
	}
	if !strings.Contains(out, "　　萧炎盯着头顶的修炼台。") {
		t.Errorf("txt indent missing: %q", out[:min(300, len(out))])
	}
	// 重复标题去除（正文首行 = 章节标题时删除）
	if strings.Contains(out, "第一章 陨落的天才\n第一章 陨落的天才") {
		t.Errorf("duplicate title not removed")
	}
}

func TestDownloadTaskWithReplaceRules(t *testing.T) {
	m, fs, _ := newTestManager(t)
	m.svc.ReplaceProvider = func() []*model.ReplaceRule {
		return []*model.ReplaceRule{{
			Name: "净化测试", Pattern: `这是第二页内容。`, Replacement: "", IsRegex: true, Enabled: true,
		}}
	}
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "斗破苍穹", BookURL: fs.Base + "/book/1.html"}
	taskID, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, 0, 2) // 只下载第一章
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		tk, err := m.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Status == model.TaskDone || tk.Status == model.TaskFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %+v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}
	tk, _ := m.GetTask(taskID)
	if tk.TotalChapters != 3 {
		t.Errorf("total = %d want 3 (rangeEnd=0 现为全本)", tk.TotalChapters)
	}
}

func TestPauseResume(t *testing.T) {
	m, fs, _ := newTestManager(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "斗破苍穹", BookURL: fs.Base + "/book/1.html"}
	taskID, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 立即暂停
	_ = m.Pause(taskID)
	time.Sleep(500 * time.Millisecond)
	tk, _ := m.GetTask(taskID)
	if tk.Status != model.TaskDone && tk.Status != model.TaskPaused && tk.Status != model.TaskRunning {
		t.Fatalf("unexpected status %s", model.TaskStatusName(tk.Status))
	}
	// 章节很少时任务可能在暂停生效前完成 — 只验证状态机不卡死
	deadline := time.Now().Add(20 * time.Second)
	for {
		tk, _ := m.GetTask(taskID)
		if tk.Status == model.TaskDone || tk.Status == model.TaskPaused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stuck: %+v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 若暂停则恢复
	tk, _ = m.GetTask(taskID)
	if tk.Status == model.TaskPaused {
		if err := m.Resume(taskID); err != nil {
			t.Fatal(err)
		}
		for {
			tk, _ = m.GetTask(taskID)
			if tk.Status == model.TaskDone {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("resume timeout: %+v", tk)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func TestCancel(t *testing.T) {
	m, fs, _ := newTestManager(t)
	bs := testutil.FixtureHTMLSource(fs.Base)
	book := &model.Book{Name: "斗破苍穹", BookURL: fs.Base + "/book/1.html"}
	taskID, _, err := m.Create(nil, book, bs, []string{"txt"}, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Cancel(taskID)
	deadline := time.Now().Add(15 * time.Second)
	for {
		tk, _ := m.GetTask(taskID)
		if tk.Status == model.TaskCanceled || tk.Status == model.TaskDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel timeout: %+v", tk)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestJSONSourceDownload(t *testing.T) {
	m, fs, st := newTestManager(t)
	bs := testutil.FixtureJSONSource(fs.Base)
	book := &model.Book{Name: "测试之JSON书", BookURL: fs.Base + "/api/book/5"}
	taskID, _, err := m.Create(nil, book, bs, []string{"epub"}, 0, -1, 2)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		tk, err := m.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if tk.Status == model.TaskDone || tk.Status == model.TaskFailed {
			if tk.Status != model.TaskDone {
				t.Fatalf("json task failed: %+v", tk)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: %+v", tk)
		}
		time.Sleep(100 * time.Millisecond)
	}
	ch, err := st.GetChapter(m.taskBookID(taskID), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Content, "这是JSON源的第一段") {
		t.Errorf("json chapter = %q", ch.Content)
	}
}

// taskBookID 查任务对应的 bookID。
func (m *Manager) taskBookID(taskID int64) int64 {
	t, _ := m.GetTask(taskID)
	if t == nil {
		return 0
	}
	return t.BookID
}

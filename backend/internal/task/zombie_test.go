package task

import (
	"path/filepath"
	"testing"

	"booksmith/internal/model"
	"booksmith/internal/store"
)

// 服务重启后，running/pending/exporting 状态的僵尸任务应标记为 paused（可恢复）。
func TestStaleTasksRecoveredOnBoot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "z.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertBook(&model.Book{ID: 0, Name: "书", BookURL: "https://a.com/1", Origin: "https://a.com"}); err != nil {
		t.Fatal(err)
	}
	// 造三个僵尸任务 + 一个已完成任务
	for _, status := range []model.TaskStatus{model.TaskRunning, model.TaskPending, model.TaskExporting, model.TaskDone} {
		tk := &model.DownloadTask{BookID: 1, BookName: "x", Status: status}
		if _, err := st.CreateTask(tk); err != nil {
			t.Fatal(err)
		}
		if status == model.TaskDone {
			tk.Status = model.TaskDone
			_ = st.UpdateTask(tk)
		}
	}
	if _, err := st.MarkStaleTasksPaused(); err != nil {
		t.Fatal(err)
	}
	tasks, err := st.ListTasks(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		if tk.Status == model.TaskRunning || tk.Status == model.TaskPending || tk.Status == model.TaskExporting {
			t.Errorf("task %d still stale status %s", tk.ID, model.TaskStatusName(tk.Status))
		}
	}
	// 已完成的任务不受影响
	done := 0
	for _, tk := range tasks {
		if tk.Status == model.TaskDone {
			done++
		}
	}
	if done != 1 {
		t.Errorf("done tasks = %d want 1", done)
	}
}

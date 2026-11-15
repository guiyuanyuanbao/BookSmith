package server

import (
	"fmt"
	"net/http"

	"booksmith/internal/export"
	"booksmith/internal/model"
	"booksmith/internal/task"
)

// export_now.go 纯导出：从已缓存章节直接生成文件，不触发目录/正文抓取。

// handleExportNow 立即导出（仅使用缓存）。
// body: {formats: []}
// 缓存不完整时返回 400 + {pending, done, failed}，提示先续传。
func (s *Server) handleExportNow(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "id 非法")
		return
	}
	var req struct {
		Formats []string `json:"formats"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{"txt", "epub"}
	}
	// 与 handleCreateTask 一致：无效格式提前 400
	for _, f := range req.Formats {
		if !export.ValidFormat(f) {
			writeErr(w, 400, "不支持的导出格式: "+f)
			return
		}
	}
	bookAny, err := s.app.Store.GetBook(id)
	if err != nil {
		writeErr(w, 404, "书籍不存在")
		return
	}
	book := bookAny

	// 有活动任务（running/pending）时禁止并发导出。paused 放行：任务不写章节，
	// 且 doExport 已按 bookID 串行化 + 暂存原子替换，无并发风险（回归修复：
	// paused 纳入 ActiveTaskByBook 后曾误拦缓存完整书籍的合法导出）
	if at, aerr := s.app.Store.ActiveTaskByBook(id); aerr == nil && at.Status != model.TaskPaused {
		writeErr(w, 409, "任务 #"+fmt.Sprint(at.ID)+" 进行中（"+model.TaskStatusName(at.Status)+"），完成后再导出")
		return
	}

	// 缓存完整性检查：有非完成正文章节 → 要求先续传
	total, done, failed, err := s.app.Store.CountChapterStatus(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pending := total - done - failed
	if pending > 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		writeRawJSON(w, map[string]any{
			"isSuccess": false,
			"errorMsg":  "章节缓存不完整，请先续传下载",
			"data":      map[string]any{"pending": pending, "done": done, "failed": failed},
		})
		return
	}

	// 导出只依赖已缓存正文，不依赖书源存在（书源可能已被清理删除）

	// 创建瞬时导出任务（exporting → done，同步秒完成）
	t := &model.DownloadTask{
		BookID:        id,
		BookName:      book.Name,
		Author:        book.Author,
		Origin:        book.Origin,
		OriginName:    book.OriginName,
		Status:        model.TaskExporting,
		Phase:         "export",
		Formats:       req.Formats,
		TotalChapters: done,
		DoneChapters:  done,
	}
	taskID, err := s.app.Store.CreateTask(t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	exportPath, err := s.app.TaskMgr.ExportNow(taskID, book, req.Formats)
	if err != nil {
		t.Status = model.TaskFailed
		t.Phase = "export"
		t.ErrMsg = "导出失败: " + err.Error()
		_ = s.app.Store.UpdateTask(t)
		writeErr(w, 500, t.ErrMsg)
		return
	}
	t.Status = model.TaskDone
	t.Phase = ""
	t.ExportPath = exportPath
	_ = s.app.Store.UpdateTask(t)
	s.app.Bus.Publish(task.Event{Type: "task.done", TaskID: taskID, Data: map[string]any{
		"bookName": book.Name, "done": done, "total": done, "status": "done",
	}})
	writeOK(w, map[string]any{"taskId": taskID, "exportPath": exportPath})
}

// writeRawJSON 写原始 JSON 响应（含 HTTP 状态码，由调用方先 WriteHeader）。
func writeRawJSON(w http.ResponseWriter, v any) {
	writeJSONBody(w, v)
}

package server

import (
	"context"
	"net/http"
	"os"
	"time"

	"booksmith/internal/model"
)

// book_handlers.go 书架资产操作：统计视图 + 书架发起下载（含任务去重）。

// handleBookStats 书籍统计：章节缓存 + 活动任务（书架进度条数据源）。
func (s *Server) handleBookStats(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "id 非法")
		return
	}
	stats, err := s.computeBookStats(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, stats)
}

// handleBookDownload 从书架发起下载：复用缓存断点；已有活动任务直接返回。
func (s *Server) handleBookDownload(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "id 非法")
		return
	}
	var req struct {
		Formats     []string `json:"formats"`
		RangeStart  int      `json:"rangeStart"`
		RangeEnd    int      `json:"rangeEnd"` // -1 = 到末尾
		Concurrency int      `json:"concurrency"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{"txt", "epub"}
	}
	bookAny, err := s.app.Store.GetBook(id)
	if err != nil {
		writeErr(w, 404, "书籍不存在")
		return
	}
	book := bookAny
	bsAny, err := s.app.Store.GetSource(book.Origin)
	if err != nil {
		writeErr(w, 400, "该书源已不存在，无法下载（可移除此书后重新搜索）")
		return
	}
	bs := bsAny

	// 同书同源去重
	if at, err := s.app.Store.ActiveTaskByBook(id); err == nil {
		writeOK(w, map[string]any{"taskId": at.ID, "existing": true})
		return
	}

	// 缓存完整且无失败 → 纯导出（不重抓目录/正文，秒完成）
	// 有失败章节时不走此捷径：用户要的是补齐失败章节（重跑），直接导出会漏章
	total, done, failed, serr := s.app.Store.CountChapterStatus(id)
	if serr == nil && total > 0 && done > 0 && failed == 0 {
		pending := total - done
		if pending <= 0 {
			// 先清旧导出
			if paths, e := s.app.Store.DeleteExportsByBook(id); e == nil {
				for _, p := range paths {
					_ = osRemove(p)
				}
			}
			t := &model.DownloadTask{
				BookID: id, BookName: book.Name, Author: book.Author,
				Origin: book.Origin, OriginName: book.OriginName,
				Status: model.TaskExporting, Phase: "export",
				Formats: req.Formats, TotalChapters: done, DoneChapters: done,
			}
			taskID, cerr := s.app.Store.CreateTask(t)
			if cerr != nil {
				writeErr(w, 500, cerr.Error())
				return
			}
			exportPath, expErr := s.app.TaskMgr.ExportNow(taskID, book, req.Formats)
			if expErr != nil {
				t.Status = model.TaskFailed
				t.Phase = "export"
				t.ErrMsg = "导出失败: " + expErr.Error()
				_ = s.app.Store.UpdateTask(t)
				writeErr(w, 500, t.ErrMsg)
				return
			}
			t.Status = model.TaskDone
			t.Phase = ""
			t.ExportPath = exportPath
			_ = s.app.Store.UpdateTask(t)
			writeOK(w, map[string]any{"taskId": taskID, "existing": false})
			return
		}
	}

	ctx, cancel := contextWithTimeout()
	defer cancel()
	taskID, existed, err := s.app.TaskMgr.Create(ctx, book, bs, req.Formats, req.RangeStart, req.RangeEnd, req.Concurrency)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"taskId": taskID, "existing": existed})
}

// handleListBooksWithStats 书架列表（含统计），前端一次请求渲染全部进度条。
func (s *Server) handleListBooksWithStats(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("keyword")
	items, total, err := s.app.Store.ListBooks(keyword, queryInt(r, "limit", 50), queryInt(r, "offset", 0))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type bookWithStats struct {
		*model.Book
		Stats *BookStats `json:"stats"`
	}
	out := make([]bookWithStats, 0, len(items))
	for _, b := range items {
		bws := bookWithStats{Book: b}
		if stats, err := s.computeBookStats(b.ID); err == nil {
			bws.Stats = stats
		}
		out = append(out, bws)
	}
	writeOK(w, map[string]any{"items": out, "total": total})
}

// contextWithTimeout 后台任务默认超时。
func contextWithTimeout() (context.Context, context.CancelFunc) {
	return newTimeoutContext(120 * time.Second)
}

// handleDeleteExport 删除单个导出文件（磁盘 + 记录）。
func (s *Server) handleDeleteExport(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "id 非法")
		return
	}
	path, err := s.app.Store.DeleteExport(id)
	if err != nil {
		writeErr(w, 404, "导出记录不存在")
		return
	}
	_ = os.Remove(path) // 磁盘文件删除失败不影响记录清除
	writeOK(w, nil)
}

func osRemove(path string) error {
	return os.Remove(path)
}

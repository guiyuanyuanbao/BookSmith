package server

import (
	"net/http"

	"booksmith/internal/model"
)

// batch.go 批量操作处理器。

// handleSourceBatch 书源批量操作。
// body: {action: "enable"|"disable"|"delete", urls: []}
func (s *Server) handleSourceBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string   `json:"action"`
		URLs   []string `json:"urls"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Action != "enable" && req.Action != "disable" && req.Action != "delete" {
		writeErr(w, 400, "action 必须为 enable/disable/delete")
		return
	}
	if len(req.URLs) == 0 {
		writeErr(w, 400, "urls 不能为空")
		return
	}
	if len(req.URLs) > 3000 {
		req.URLs = req.URLs[:3000]
	}
	n, err := s.app.Store.BatchSourceAction(req.URLs, req.Action)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"affected": n})
}

// handleBookBatch 书架批量操作。
// body: {action: "remove", ids: []}
func (s *Server) handleBookBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string  `json:"action"`
		IDs    []int64 `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Action != "remove" {
		writeErr(w, 400, "action 必须为 remove")
		return
	}
	if len(req.IDs) == 0 {
		writeErr(w, 400, "ids 不能为空")
		return
	}
	n, err := s.app.Store.BatchRemoveBooks(req.IDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"affected": n})
}

// handleOverview 概览统计聚合。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}

	// 书源统计
	srcAll, _ := s.app.Store.AllSources(false)
	enabled, disabled, verified, abnormal := 0, 0, 0, 0
	for _, bs := range srcAll {
		if bs.Enabled {
			enabled++
		} else {
			disabled++
		}
		if bs.Verified {
			verified++
		} else if bs.VerifyErr != "" {
			abnormal++
		}
	}
	out["sources"] = map[string]any{
		"total": len(srcAll), "enabled": enabled, "disabled": disabled,
		"verified": verified, "abnormal": abnormal,
	}

	// 书架统计
	books, _, err := s.app.Store.ListBooks("", 10000, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	totalChapters, cachedChapters := 0, 0
	for _, b := range books {
		total, done, failed, _ := s.app.Store.CountChapterStatus(b.ID)
		totalChapters += total
		cachedChapters += done + failed
	}
	out["books"] = map[string]any{"count": len(books), "totalChapters": totalChapters, "cachedChapters": cachedChapters}

	// 任务统计
	tasks, _ := s.app.Store.ListTasks(1000)
	statusCount := map[string]int{}
	for _, t := range tasks {
		statusCount[model.TaskStatusName(t.Status)]++
	}
	out["tasks"] = map[string]any{"recent": len(tasks), "byStatus": statusCount}

	// 导出统计
	exports, _ := s.app.Store.ListExports(10000)
	var totalSize int64
	byFormat := map[string]int{}
	for _, e := range exports {
		if size, ok := e["size"].(int64); ok {
			totalSize += size
		}
		if f, ok := e["format"].(string); ok {
			byFormat[f]++
		}
	}
	out["exports"] = map[string]any{"count": len(exports), "totalSize": totalSize, "byFormat": byFormat}

	writeOK(w, out)
}

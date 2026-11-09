package server

import (
	"net/http"
)

// handleCleanupStart 启动清理任务（异步）。
// body: {scope?: "all"|"enabled", mode?: "check"|"disable"|"delete"}
func (s *Server) handleCleanupStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope string `json:"scope"`
		Mode  string `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "disable"
	}
	if req.Mode != "check" && req.Mode != "disable" && req.Mode != "delete" {
		writeErr(w, 400, "mode 必须为 check/disable/delete")
		return
	}
	if req.Scope == "" {
		req.Scope = "enabled"
	}
	job, err := s.startCleanup(req.Scope, req.Mode)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeOK(w, map[string]any{"cleanupId": job.ID, "running": true, "total": job.Snapshot().Total})
}

// handleCleanupGet 清理进度/结果快照。
func (s *Server) handleCleanupGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.CleanupMu.Lock()
	job := s.app.CleanupJobs[id]
	s.app.CleanupMu.Unlock()
	if job == nil {
		writeErr(w, 404, "清理任务不存在或已过期")
		return
	}
	writeOK(w, job.Snapshot())
}

// handleCleanupCancel 取消清理。
func (s *Server) handleCleanupCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.app.CleanupMu.Lock()
	job := s.app.CleanupJobs[id]
	s.app.CleanupMu.Unlock()
	if job == nil {
		writeErr(w, 404, "清理任务不存在")
		return
	}
	job.cancel()
	writeOK(w, nil)
}

// handleClearFinishedTasks 清空已完成/已取消的任务记录。
func (s *Server) handleClearFinishedTasks(w http.ResponseWriter, r *http.Request) {
	n, err := s.app.Store.ClearFinishedTasks()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": n})
}

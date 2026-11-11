package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
// 移除前做封面兜底：若同书剩余来源都不足以提供封面（或这是最后一个来源），
// 先把分组当前封面抓取转存为本地封面，避免源移除后封面随 CoverURL 失效丢失。
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
	s.preserveGroupCovers(req.IDs)
	n, err := s.app.Store.BatchRemoveBooks(req.IDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{"affected": n})
}

// preserveGroupCovers 移除前封面兜底（尽力而为，失败不阻塞移除）：
// 对每个待移除的书，找同书剩余来源（不在删除列表中）：
//   - 剩余源里有有效 CoverURL（或该书已有本地上传封面）→ 封面仍可达，不处理；
//   - 剩余源都没有封面 → 把待删源（或分组任一）的封面抓下来转存为本地封面。
func (s *Server) preserveGroupCovers(ids []int64) {
	removeSet := map[int64]bool{}
	for _, id := range ids {
		removeSet[id] = true
	}
	// 待删书籍先取出（删除后无法再读）
	pending := make([]*model.Book, 0, len(ids))
	for _, id := range ids {
		if b, err := s.app.Store.GetBook(id); err == nil {
			pending = append(pending, b)
		}
	}
	if len(pending) == 0 {
		return
	}
	// 全库书籍按去重键分组，找同书剩余来源
	all, _, err := s.app.Store.ListBooks("", 10000, 0)
	if err != nil {
		return
	}
	remainingByKey := map[string][]*model.Book{}
	for _, b := range all {
		if removeSet[b.ID] {
			continue
		}
		key := normalizeKey(b.Name, b.Author)
		remainingByKey[key] = append(remainingByKey[key], b)
	}
	for _, b := range pending {
		key := normalizeKey(b.Name, b.Author)
		rest := remainingByKey[key]
		// 同书任一剩余记录已有本地上传封面 → 安全
		localOK := false
		remoteCandidate := ""
		for _, rb := range rest {
			if rb.LocalCover != "" {
				localOK = true
				break
			}
			if remoteCandidate == "" && rb.CoverURL != "" {
				remoteCandidate = rb.CoverURL
			}
		}
		if localOK {
			continue
		}
		// 本记录自己有本地上传封面 → 剩余源可共享同一文件，转移文件名到第一个剩余源
		if b.LocalCover != "" {
			s.transferLocalCover(b, rest)
			continue
		}
		// 剩余源有远程 CoverURL → 分组封面仍可达（书架展示会用剩余源的 URL），不处理
		if remoteCandidate != "" {
			continue
		}
		// 无任何剩余封面来源：把本源封面转存为本地封面，挂到一个剩余源上（若有）
		// 若没有剩余源（整本书被移除），封面随之作废，无需保留
		if b.CoverURL == "" || len(rest) == 0 {
			continue
		}
		s.persistCoverToGroup(b, rest)
	}
}

// transferLocalCover 把待删源记录的本地上传封面文件名转移到同书剩余的第一个源（更新 LocalCover 字段指向同一文件）。
func (s *Server) transferLocalCover(b *model.Book, rest []*model.Book) {
	if len(rest) == 0 {
		return
	}
	target := rest[0]
	if target.LocalCover == b.LocalCover {
		return
	}
	// 同名文件共享：把文件复制一份挂到目标（避免删书时误删共享文件）
	if err := s.ensureUploadsDir(); err != nil {
		return
	}
	if data, err := os.ReadFile(s.localCoverPath(b.LocalCover)); err == nil && len(data) > 0 {
		ext := strings.TrimPrefix(filepath.Ext(b.LocalCover), ".")
		name := fmt.Sprintf("%d.%s", target.ID, ext)
		if err := os.WriteFile(s.localCoverPath(name), data, 0o644); err == nil {
			target.LocalCover = name
			_, _ = s.app.Store.UpsertBook(target)
		}
	}
}

// persistCoverToGroup 抓取 b.CoverURL 转存为本地封面，挂到同书剩余的第一个源上。
func (s *Server) persistCoverToGroup(b *model.Book, rest []*model.Book) {
	if len(rest) == 0 {
		return
	}
	data, mime, ok := s.fetchAndCacheCover(b.CoverURL)
	if !ok {
		return // 尽力而为：抓不到就放弃，不阻塞移除
	}
	if err := s.ensureUploadsDir(); err != nil {
		return
	}
	target := rest[0]
	ext := ".jpg"
	switch {
	case strings.Contains(mime, "png"):
		ext = ".png"
	case strings.Contains(mime, "gif"):
		ext = ".gif"
	case strings.Contains(mime, "webp"):
		ext = ".webp"
	}
	name := fmt.Sprintf("%d%s", target.ID, ext)
	if err := os.WriteFile(s.localCoverPath(name), data, 0o644); err != nil {
		return
	}
	target.LocalCover = name
	_, _ = s.app.Store.UpsertBook(target)
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

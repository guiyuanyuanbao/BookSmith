package server

import (
	"strconv"

	"booksmith/internal/model"
)

// book_stats.go 书架资产视图：章节缓存统计 + 活动任务关联。

// BookStats 单本书的统计与活动任务。
type BookStats struct {
	// 章节缓存统计
	TotalChapters   int `json:"totalChapters"`
	DoneChapters    int `json:"doneChapters"`
	FailedChapters  int `json:"failedChapters"`
	PendingChapters int `json:"pendingChapters"`
	// 活动任务（running/pending/exporting 中最新的一个），无则为 null
	ActiveTask *ActiveTaskInfo `json:"activeTask,omitempty"`
	// 最近一次任务（无论状态）
	LastTask *ActiveTaskInfo `json:"lastTask,omitempty"`
	// 是否已有该书的任务（含历史）
	HasTask bool `json:"hasTask"`
}

// ActiveTaskInfo 任务摘要。
type ActiveTaskInfo struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Phase      string `json:"phase,omitempty"`
	Done       int    `json:"done"`
	Failed     int    `json:"failed"`
	Total      int    `json:"total"`
	ErrMsg     string `json:"errMsg,omitempty"`
	ExportPath string `json:"exportPath,omitempty"`
}

// computeBookStats 计算书的统计与任务关联。
func (s *Server) computeBookStats(bookID int64) (*BookStats, error) {
	stats := &BookStats{}
	// 章节统计
	total, done, failed, err := s.app.Store.CountChapterStatus(bookID)
	if err != nil {
		return nil, err
	}
	stats.TotalChapters = total
	stats.DoneChapters = done
	stats.FailedChapters = failed
	stats.PendingChapters = total - done - failed
	// 任务关联（该书的全部任务，取最新活动/最新一条）
	tasks, err := s.app.Store.TasksByBook(bookID)
	if err != nil {
		return nil, err
	}
	stats.HasTask = len(tasks) > 0
	for _, t := range tasks {
		info := &ActiveTaskInfo{
			ID: t.ID, Status: model.TaskStatusName(t.Status), Phase: t.Phase,
			Done: t.DoneChapters, Failed: t.FailChapters, Total: t.TotalChapters,
			ErrMsg: t.ErrMsg, ExportPath: t.ExportPath,
		}
		switch t.Status {
		case model.TaskRunning, model.TaskPending, model.TaskExporting:
			if stats.ActiveTask == nil {
				stats.ActiveTask = info
			}
		}
		if stats.LastTask == nil {
			stats.LastTask = info
		}
	}
	return stats, nil
}

// parseID 从 PathValue 解析 int64 ID。
func parseID(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

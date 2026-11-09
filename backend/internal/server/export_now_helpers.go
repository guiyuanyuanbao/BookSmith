package server

import (
	"net/http"
)

// countNotDoneChapters 统计非完成状态的正文章节数。
func (s *Server) countNotDoneChapters(bookID int64) int {
	chs, err := s.app.Store.ListChapters(bookID, false)
	if err != nil {
		return 0
	}
	n := 0
	for _, ch := range chs {
		if ch.IsVolume {
			continue
		}
		if ch.Status != 2 { // 2 = done
			n++
		}
	}
	return n
}

var _ = http.StatusOK

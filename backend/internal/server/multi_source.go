package server

import (
	"net/http"

	"booksmith/internal/model"
)

// multi_source.go 多书源聚合视图：同一本书的所有来源合并展示，每源独立进度。

// SourceBook 单源书籍记录（含该源独立缓存统计）。
type SourceBook struct {
	*model.Book
	Stats *BookStats `json:"stats"`
	// SourceMissing 该书的书源已被删除（清理/手动），无法续传，仅可导出已缓存内容或移除
	SourceMissing bool `json:"sourceMissing,omitempty"`
}

// BookGroup 同一本书（name+author 去重）的聚合视图。
type BookGroup struct {
	// 主记录（缓存最完整/最近更新的源）
	Primary *model.Book `json:"primary"`
	// 该书命中的全部来源（含各自缓存、进度、任务）
	Sources []*SourceBook `json:"sources"`
	// 聚合统计
	SourceCount   int  `json:"sourceCount"`
	TotalChapters int  `json:"totalChapters"` // 主源的章节总数
	DoneChapters  int  `json:"doneChapters"`  // 主源已完成
	HasCover      bool `json:"hasCover"`
	// 跨源元数据补全：主源缺失的封面/简介从其他源补齐
	CoverURL string `json:"coverUrl,omitempty"`
	Intro    string `json:"intro,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// handleListBookGroups 书架聚合视图（同书多源合并）。
func (s *Server) handleListBookGroups(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("keyword")
	books, _, err := s.app.Store.ListBooks(keyword, 500, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	// 一次性取现有源 URL 集合，标记"源已删除"的书（清理源后书架不再显示可续传的假象）
	existingSources := map[string]bool{}
	if all, err := s.app.Store.AllSources(false); err == nil {
		for _, bs := range all {
			existingSources[bs.BookSourceURL] = true
		}
	}

	// 按 (规整书名, 规整作者) 分组
	groups := map[string]*BookGroup{}
	order := make([]string, 0, len(books))
	for _, b := range books {
		key := normalizeKey(b.Name, b.Author)
		g, ok := groups[key]
		if !ok {
			g = &BookGroup{}
			groups[key] = g
			order = append(order, key)
		}
		sb := &SourceBook{Book: b, SourceMissing: b.Origin != "" && !existingSources[b.Origin]}
		if st, err := s.computeBookStats(b.ID); err == nil {
			sb.Stats = st
		}
		// 跨源元数据补全（优先取有值的）
		if g.CoverURL == "" && b.CoverURL != "" {
			g.CoverURL = b.CoverURL
		}
		if g.Intro == "" && b.Intro != "" {
			g.Intro = b.Intro
		}
		if g.Kind == "" && b.Kind != "" {
			g.Kind = b.Kind
		}
		g.Sources = append(g.Sources, sb)
	}

	// 选主源（已完成章节数最多，其次最近更新）+ 聚合统计
	out := make([]*BookGroup, 0, len(order))
	for _, key := range order {
		g := groups[key]
		var primary *SourceBook
		bestDone := -1
		for _, sb := range g.Sources {
			done := 0
			if sb.Stats != nil {
				done = sb.Stats.DoneChapters
			}
			if done > bestDone {
				bestDone = done
				primary = sb
			}
		}
		if primary == nil {
			continue
		}
		g.Primary = primary.Book
		g.SourceCount = len(g.Sources)
		g.HasCover = g.CoverURL != ""
		if primary.Stats != nil {
			g.TotalChapters = primary.Stats.TotalChapters
			g.DoneChapters = primary.Stats.DoneChapters
		}
		// 主源无封面/简介时用跨源补全值
		if primary.CoverURL == "" {
			primary.CoverURL = g.CoverURL
		}
		if primary.Intro == "" {
			primary.Intro = g.Intro
		}
		out = append(out, g)
	}
	writeOK(w, map[string]any{"items": out, "total": len(out)})
}

// normalizeKey 书籍去重键（书名+作者，忽略空白与大小写）。
func normalizeKey(name, author string) string {
	norm := func(s string) string {
		out := make([]rune, 0, len(s))
		for _, r := range s {
			if r == ' ' || r == '\t' || r == '\u3000' {
				continue
			}
			if r >= 'A' && r <= 'Z' {
				r = r - 'A' + 'a'
			}
			out = append(out, r)
		}
		return string(out)
	}
	return norm(name) + "|" + norm(author)
}

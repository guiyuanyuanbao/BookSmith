// cover_fetch.go 封面兜底获取：从全部启用书源中搜索书名并抓取首个可用封面。
// 与聚合搜索同一封面逻辑（SearchOne → coverUrl → 封面代理抓取），
// 并发探测，首个有效图片即返回并转存为该书的本地封面（持久，不随源删除而丢失）。
package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"booksmith/internal/model"
)

// coverFetchTimeout 单源搜索超时；整体受 r.Context() 控制。
const coverFetchTimeout = 10 * time.Second

// handleCoverFetch POST /api/books/{id}/cover/fetch
// 从所有启用书源中搜索该书（书名+作者），并发探测，取第一个可下载的有效封面，
// 下载后转存为本地封面（与手动上传同一存储路径，导出时同样生效）。
func (s *Server) handleCoverFetch(w http.ResponseWriter, r *http.Request) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "id 非法")
		return
	}
	bookAny, err := s.app.Store.GetBook(id)
	if err != nil {
		writeErr(w, 404, "书籍不存在")
		return
	}
	book := bookAny

	sources, err := s.app.FindEnabledSources()
	if err != nil {
		writeErr(w, 500, "读取书源失败: "+err.Error())
		return
	}
	if len(sources) == 0 {
		writeErr(w, 400, "没有启用的书源")
		return
	}

	coverURL, sourceName := s.probeCoverFromSources(r.Context(), sources, book.Name, book.Author)
	if coverURL == "" {
		writeErr(w, 404, "所有书源都未找到可用封面（可稍后重试或手动上传）")
		return
	}
	log.Printf("[cover-fetch] 书《%s》从「%s」获得封面", book.Name, sourceName)

	// 抓取图片字节（复用封面代理的防盗链客户端 + 磁盘缓存）
	data, mime, ok := s.fetchAndCacheCover(coverURL)
	if !ok {
		writeErr(w, 502, "封面图片下载失败（源站不可用）")
		return
	}

	// 转存为本地封面：与手动上传同路径 data/uploads/{bookID}.{ext}，Book.LocalCover 记录文件名
	if err := s.ensureUploadsDir(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if book.LocalCover != "" {
		_ = os.Remove(s.localCoverPath(book.LocalCover)) // 同书旧封面（不同扩展名）一并清理
	}
	ext := ".jpg"
	switch {
	case strings.Contains(mime, "png"):
		ext = ".png"
	case strings.Contains(mime, "gif"):
		ext = ".gif"
	case strings.Contains(mime, "webp"):
		ext = ".webp"
	}
	name := fmt.Sprintf("%d%s", id, ext)
	if err := os.WriteFile(s.localCoverPath(name), data, 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	book.LocalCover = name
	if _, err := s.app.Store.UpsertBook(book); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeOK(w, map[string]any{
		"localCover": name,
		"coverUrl":   fmt.Sprintf("/api/books/%d/cover?t=%d", id, book.UpdatedAt),
		"fromSource": sourceName,
	})
}

// coverHit 首个命中的封面（源内传递用）。
type coverHit struct{ coverURL, sourceName string }

func readHit(found chan coverHit) (string, string) {
	select {
	case h := <-found:
		return h.coverURL, h.sourceName
	default:
		return "", ""
	}
}

// probeCoverFromSources 并发搜索书源，返回首个"图片可下载"的封面 (coverURL, sourceName)。
// 命中搜索结果仅是候选：候选封面下载失败（404/防盗链/非图片）时继续尝试其他源，
// 直到拿到真实图片或全部源尝试完毕。
func (s *Server) probeCoverFromSources(ctx context.Context, sources []*model.BookSource, name, author string) (string, string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	found := make(chan coverHit, 1)

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // 并发 8：与搜索页探测节奏接近，避免触发限流
	for _, bs := range sources {
		// 已有结果即停止派发（值直接取用——receive 本身会消费 channel）
		select {
		case h := <-found:
			wg.Wait()
			return h.coverURL, h.sourceName
		case <-ctx.Done():
			wg.Wait()
			return readHit(found)
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(bs *model.BookSource) {
			defer wg.Done()
			defer func() { <-sem }()
			select {
			case <-found: // 已找到，不再浪费请求
				return
			case <-ctx.Done():
				return
			default:
			}
			cctx, ccancel := context.WithTimeout(ctx, coverFetchTimeout)
			defer ccancel()
			items, err := s.app.WebBook.SearchOne(cctx, bs, name, 1)
			if err != nil {
				return // 单源失败不记录日志（聚合搜索常态），仅调试时开启
			}
			if len(items) == 0 {
				return
			}
			// 候选按书名作者相关度粗排，逐个验证封面可下载（数量有限，避免连环抓取）
			for _, item := range coverCandidates(items, name, author) {
				if item.CoverURL == "" {
					continue
				}
				if data, _, ok := s.fetchAndCacheCover(item.CoverURL); ok && len(data) > 0 {
					select {
					case found <- coverHit{coverURL: item.CoverURL, sourceName: bs.BookSourceName}:
						cancel() // 通知其余协程收手
					default:
					}
					return
				}
			}
		}(bs)
	}
	wg.Wait()
	return readHit(found)
}

// coverCandidates 返回按匹配度排序的候选条目（同名同作者 > 同名 > 同作者 > 其他，并列取响应快者）。
func coverCandidates(items []*model.SearchOrigin, name, author string) []*model.SearchOrigin {
	type scored struct {
		item  *model.SearchOrigin
		score int
	}
	sc := make([]scored, 0, len(items))
	for _, it := range items {
		s := 0
		if strings.EqualFold(strings.TrimSpace(it.Name), strings.TrimSpace(name)) {
			s += 2
		}
		if author != "" && strings.EqualFold(strings.TrimSpace(it.Author), strings.TrimSpace(author)) {
			s += 1
		}
		sc = append(sc, scored{item: it, score: s})
	}
	sort.SliceStable(sc, func(i, j int) bool {
		if sc[i].score != sc[j].score {
			return sc[i].score > sc[j].score
		}
		return sc[i].item.RespondTime < sc[j].item.RespondTime
	})
	out := make([]*model.SearchOrigin, 0, len(sc))
	for _, x := range sc {
		out = append(out, x.item)
	}
	return out
}

// pickCoverCandidate 从搜索结果中挑最适合做封面的条目（coverCandidates 首个带封面者）。
// 评分：书名精确匹配 +2，作者匹配 +1；取分高者，并列取 respondTime 快者。
func pickCoverCandidate(items []*model.SearchOrigin, name, author string) *model.SearchOrigin {
	for _, it := range coverCandidates(items, name, author) {
		if it.CoverURL != "" {
			return it
		}
	}
	return nil
}

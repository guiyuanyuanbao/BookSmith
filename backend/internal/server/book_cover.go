// book_cover.go 手动上传封面：上传 / 读取 / 移除。
// 文件存于 data/uploads/{bookID}.{ext}；Book.LocalCover 记录文件名，优先于 CoverURL。
package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"booksmith/internal/export"
)

// uploadsDir 上传封面目录。
func (s *Server) uploadsDir() string {
	return filepath.Join(s.app.DataDir, "uploads")
}

// ensureUploadsDir 创建上传目录。
func (s *Server) ensureUploadsDir() error {
	return os.MkdirAll(s.uploadsDir(), 0o755)
}

// localCoverPath 按 Book.LocalCover 解析绝对路径。
func (s *Server) localCoverPath(name string) string {
	return filepath.Join(s.uploadsDir(), filepath.Base(name)) // Base 防目录穿越
}

// handleCoverUpload POST /api/books/{id}/cover（multipart，字段名 file）。
func (s *Server) handleCoverUpload(w http.ResponseWriter, r *http.Request) {
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

	// 10MB 上限
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, 400, "表单解析失败（文件过大或格式错误）")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "读取文件失败")
		return
	}
	ct := export.SniffImageMime(data)
	ext := ".jpg"
	switch {
	case strings.Contains(ct, "png"):
		ext = ".png"
	case strings.Contains(ct, "gif"):
		ext = ".gif"
	case strings.Contains(ct, "webp"):
		ext = ".webp"
	}
	if ct == "text/html" || !strings.HasPrefix(ct, "image/") {
		writeErr(w, 400, "仅支持图片文件（jpg/png/gif/webp）")
		return
	}

	if err := s.ensureUploadsDir(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 同书旧封面（不同扩展名）一并清理
	if book.LocalCover != "" {
		_ = os.Remove(s.localCoverPath(book.LocalCover))
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
	writeOK(w, map[string]any{"localCover": name, "url": fmt.Sprintf("/api/books/%d/cover?t=%d", id, book.UpdatedAt)})
}

// handleCoverGet GET /api/books/{id}/cover 返回本地上传封面；无则 404。
func (s *Server) handleCoverGet(w http.ResponseWriter, r *http.Request) {
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
	if bookAny.LocalCover == "" {
		writeErr(w, 404, "无本地上传封面")
		return
	}
	data, err := os.ReadFile(s.localCoverPath(bookAny.LocalCover))
	if err != nil {
		writeErr(w, 404, "封面文件丢失")
		return
	}
	w.Header().Set("Content-Type", export.SniffImageMime(data))
	w.Header().Set("Cache-Control", "no-cache") // 更换封面后立即生效
	_, _ = w.Write(data)
}

// handleCoverDelete DELETE /api/books/{id}/cover 移除本地上传封面（恢复使用远程 CoverURL）。
func (s *Server) handleCoverDelete(w http.ResponseWriter, r *http.Request) {
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
	if book.LocalCover != "" {
		_ = os.Remove(s.localCoverPath(book.LocalCover))
		book.LocalCover = ""
		if _, err := s.app.Store.UpsertBook(book); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeOK(w, nil)
}

// localCoverBytes 读取本地封面供导出使用；无则返回 nil。
func (s *Server) localCoverBytes(bookID int64) *export.CoverImage {
	bookAny, err := s.app.Store.GetBook(bookID)
	if err != nil || bookAny.LocalCover == "" {
		return nil
	}
	data, err := os.ReadFile(s.localCoverPath(bookAny.LocalCover))
	if err != nil || len(data) == 0 {
		return nil
	}
	ct := export.SniffImageMime(data)
	ext := ".jpg"
	switch {
	case strings.Contains(ct, "png"):
		ext = ".png"
	case strings.Contains(ct, "gif"):
		ext = ".gif"
	case strings.Contains(ct, "webp"):
		ext = ".webp"
	}
	return &export.CoverImage{Data: data, MimeType: ct, Ext: ext}
}

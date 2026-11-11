package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"booksmith/internal/model"
	"booksmith/internal/util"
)

// ErrNotFound 未找到。
var ErrNotFound = errors.New("not found")

// ---------------- BookSource ----------------

// UpsertSource 插入或更新书源（主键 bookSourceUrl，天然去重）。
func (s *Store) UpsertSource(bs *model.BookSource, custom bool) error {
	data, err := marshal(bs)
	if err != nil {
		return err
	}
	group := ""
	if bs.BookSourceGroup != "" {
		group = bs.BookSourceGroup
	}
	_, err = s.db.Exec(`INSERT INTO book_sources
		(url,name,group_name,enabled,custom_order,custom,origin,verified,last_update_time,respond_time,weight,data,imported_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(url) DO UPDATE SET
			name=excluded.name, group_name=excluded.group_name,
			-- enabled 保留已有值：用户手动禁用/一键清理禁用的状态不因重新导入或仓库同步被覆盖
			enabled=book_sources.enabled,
			custom_order=excluded.custom_order, custom=excluded.custom OR book_sources.custom,
			origin=excluded.origin,
			-- verified 同理保留：校验结果不因导入覆盖
			verified=book_sources.verified,
			last_update_time=excluded.last_update_time, respond_time=excluded.respond_time,
			weight=excluded.weight, data=excluded.data, imported_at=excluded.imported_at`,
		bs.BookSourceURL, bs.BookSourceName, group, b2i(bs.Enabled), bs.CustomOrder,
		b2i(custom), bs.Origin, b2i(bs.Verified), bs.LastUpdateTime, bs.RespondTime,
		bs.Weight, data, bs.ImportedAt)
	return err
}

// GetSource 按 URL 取书源。
func (s *Store) GetSource(url string) (*model.BookSource, error) {
	row := s.db.QueryRow(`SELECT data, enabled, custom, origin, verified FROM book_sources WHERE url=?`, url)
	return scanSource(row)
}

func scanSource(row *sql.Row) (*model.BookSource, error) {
	var data string
	var enabled, custom, verified int
	var origin string
	if err := row.Scan(&data, &enabled, &custom, &origin, &verified); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var bs model.BookSource
	if err := jsonUnmarshal(data, &bs); err != nil {
		return nil, fmt.Errorf("decode source: %w", err)
	}
	bs.Enabled = enabled == 1
	bs.Custom = custom == 1
	bs.Origin = origin
	bs.Verified = verified == 1
	return &bs, nil
}

// ListSources 列出书源。keyword 走 FTS（空为全部），enabledFilter: -1 全部 0 禁用 1 启用。
func (s *Store) ListSources(keyword string, enabledFilter int, limit, offset int) ([]*model.BookSource, int, error) {
	where := "1=1"
	args := []any{}
	if keyword != "" {
		where += ` AND rowid IN (SELECT rowid FROM book_sources_fts WHERE book_sources_fts MATCH ?)`
		args = append(args, ftsQuery(keyword))
	}
	if enabledFilter >= 0 {
		where += ` AND enabled=?`
		args = append(args, enabledFilter)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM book_sources WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT data, enabled, custom, origin, verified FROM book_sources WHERE ` + where +
		` ORDER BY custom_order ASC, weight DESC, name ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*model.BookSource
	for rows.Next() {
		var data string
		var enabled, custom, verified int
		var origin string
		if err := rows.Scan(&data, &enabled, &custom, &origin, &verified); err != nil {
			return nil, 0, err
		}
		var bs model.BookSource
		if err := jsonUnmarshal(data, &bs); err != nil {
			continue
		}
		bs.Enabled = enabled == 1
		bs.Custom = custom == 1
		bs.Origin = origin
		bs.Verified = verified == 1
		out = append(out, &bs)
	}
	return out, total, rows.Err()
}

// AllSources 全部书源（导出用）。
func (s *Store) AllSources(enabledOnly bool) ([]*model.BookSource, error) {
	q := `SELECT data, enabled, custom, origin, verified FROM book_sources`
	if enabledOnly {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY custom_order ASC, name ASC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.BookSource
	for rows.Next() {
		var data string
		var enabled, custom, verified int
		var origin string
		if err := rows.Scan(&data, &enabled, &custom, &origin, &verified); err != nil {
			return nil, err
		}
		var bs model.BookSource
		if err := jsonUnmarshal(data, &bs); err != nil {
			continue
		}
		bs.Enabled = enabled == 1
		bs.Custom = custom == 1
		bs.Origin = origin
		bs.Verified = verified == 1
		out = append(out, &bs)
	}
	return out, rows.Err()
}

// SetSourceEnabled 启用/禁用书源。
func (s *Store) SetSourceEnabled(url string, enabled bool) error {
	r, err := s.db.Exec(`UPDATE book_sources SET enabled=? WHERE url=?`, b2i(enabled), url)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// 同步 data JSON 内的 enabled（GetSource 从 data 解析回填）
	if bs, gerr := s.GetSource(url); gerr == nil {
		bs.Enabled = enabled
		if data, merr := marshal(bs); merr == nil {
			_, _ = s.db.Exec(`UPDATE book_sources SET data=? WHERE url=?`, data, url)
		}
	}
	return nil
}

// SetSourceVerified 更新书源校验结果。
func (s *Store) SetSourceVerified(url string, ok bool, errMsg string, respondMS int64) error {
	r, err := s.db.Exec(`UPDATE book_sources SET verified=?, respond_time=? WHERE url=?`,
		b2i(ok), respondMS, url)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// 错误信息写入 data JSON
	if errMsg != "" || ok {
		bs, err := s.GetSource(url)
		if err == nil {
			bs.VerifyErr = errMsg
			if data, mErr := marshal(bs); mErr == nil {
				_, _ = s.db.Exec(`UPDATE book_sources SET data=? WHERE url=?`, data, url)
			}
		}
	}
	return nil
}

// DeleteSource 删除书源。
func (s *Store) DeleteSource(url string) error {
	r, err := s.db.Exec(`DELETE FROM book_sources WHERE url=?`, url)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSourcesByOrigin 删除某导入来源的全部书源（仓库同步前清理），返回删除数。
func (s *Store) DeleteSourcesByOrigin(origin string) (int64, error) {
	r, err := s.db.Exec(`DELETE FROM book_sources WHERE origin=? AND custom=0`, origin)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// SourceCount 书源总数。
func (s *Store) SourceCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM book_sources`).Scan(&n)
	return n, err
}

// ftsQuery 将普通关键词转为 FTS5 安全查询。
func ftsQuery(kw string) string {
	kw = strings.TrimSpace(kw)
	if kw == "" {
		return `""`
	}
	// 双引号包裹每个 token，避免 FTS 语法注入
	tokens := strings.Fields(kw)
	for i, t := range tokens {
		t = strings.ReplaceAll(t, `"`, `""`)
		tokens[i] = `"` + t + `"`
	}
	return strings.Join(tokens, " ")
}

// ---------------- ReplaceRule ----------------

// UpsertReplaceRule 插入或更新净化规则。
func (s *Store) UpsertReplaceRule(r *model.ReplaceRule) error {
	if r.UpdatedAt == 0 {
		r.UpdatedAt = NowMS()
	}
	data, err := marshal(r)
	if err != nil {
		return err
	}
	if r.ID > 0 {
		res2, err2 := s.db.Exec(`UPDATE replace_rules SET name=?,group_name=?,pattern=?,replacement=?,scope=?,
			is_regex=?,enabled=?,order_num=?,scope_title=?,scope_content=?,exclude_scope=?,apply_to=?,data=?,updated_at=?
			WHERE id=?`,
			r.Name, r.Group, r.Pattern, r.Replacement, r.Scope, b2i(r.IsRegex), b2i(r.Enabled),
			r.Order, b2i(r.ScopeTitle), b2i(r.ScopeContent), r.ExcludeScope, string(r.ApplyTo), data, r.UpdatedAt, r.ID)
		if err2 == nil {
			if n, _ := res2.RowsAffected(); n > 0 {
				return nil
			}
		}
		// ID 不存在或更新失败：回退为插入
	}
	res, err := s.db.Exec(`INSERT INTO replace_rules
		(id,name,group_name,pattern,replacement,scope,is_regex,enabled,order_num,scope_title,scope_content,exclude_scope,apply_to,data,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Name, r.Group, r.Pattern, r.Replacement, r.Scope, b2i(r.IsRegex), b2i(r.Enabled),
		r.Order, b2i(r.ScopeTitle), b2i(r.ScopeContent), r.ExcludeScope, string(r.ApplyTo), data, r.UpdatedAt)
	if err != nil {
		return err
	}
	if id, e := res.LastInsertId(); e == nil {
		r.ID = id
	}
	return nil
}

// ListReplaceRules 启用的净化规则按 order 升序。
func (s *Store) ListReplaceRules(enabledOnly bool) ([]*model.ReplaceRule, error) {
	q := `SELECT data FROM replace_rules`
	if enabledOnly {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY order_num ASC, id ASC`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.ReplaceRule
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r model.ReplaceRule
		if err := jsonUnmarshal(data, &r); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// DeleteReplaceRule 删除净化规则。
func (s *Store) DeleteReplaceRule(id int64) error {
	r, err := s.db.Exec(`DELETE FROM replace_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceRuleCount 净化规则总数。
func (s *Store) ReplaceRuleCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM replace_rules`).Scan(&n)
	return n, err
}

// ---------------- Book ----------------

// UpsertBook 插入或更新书（按 bookUrl+origin 去重），返回 ID。
func (s *Store) UpsertBook(b *model.Book) (int64, error) {
	data, err := marshal(b)
	if err != nil {
		return 0, err
	}
	now := NowMS()
	if b.CreatedAt == 0 {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	_, err = s.db.Exec(`INSERT INTO books(book_url,origin,name,author,dedup_key,data,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(book_url, origin) DO UPDATE SET
			name=excluded.name, author=excluded.author, dedup_key=excluded.dedup_key,
			data=excluded.data, updated_at=excluded.updated_at`,
		b.BookURL, b.Origin, b.Name, b.Author, util.NormalizeBookKey(b.Name, b.Author), data, b.CreatedAt, b.UpdatedAt)
	if err != nil {
		return 0, err
	}
	// ON CONFLICT UPDATE 路径的 LastInsertId 不可靠，显式查询
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM books WHERE book_url=? AND origin=?`, b.BookURL, b.Origin).Scan(&id); err != nil {
		return 0, err
	}
	// ID 回写 JSON
	if b.ID != id {
		b.ID = id
		if data2, err := marshal(b); err == nil {
			_, _ = s.db.Exec(`UPDATE books SET data=? WHERE id=?`, data2, id)
		}
	}
	return id, nil
}

// GetBook 按 ID 取书。
func (s *Store) GetBook(id int64) (*model.Book, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM books WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var b model.Book
	if err := jsonUnmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// FindBookByDedup 按去重键查书（跨源聚合复用）。
func (s *Store) FindBookByDedup(key string) ([]*model.Book, error) {
	rows, err := s.db.Query(`SELECT data FROM books WHERE dedup_key=? ORDER BY updated_at DESC`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Book
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var b model.Book
		if err := jsonUnmarshal(data, &b); err != nil {
			continue
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

// ListBooks 书架。
func (s *Store) ListBooks(keyword string, limit, offset int) ([]*model.Book, int, error) {
	where := "1=1"
	args := []any{}
	if keyword != "" {
		where += ` AND (name LIKE ? OR author LIKE ?)`
		kw := "%" + keyword + "%"
		args = append(args, kw, kw)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM books WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query(`SELECT data FROM books WHERE `+where+` ORDER BY updated_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*model.Book
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, 0, err
		}
		var b model.Book
		if err := jsonUnmarshal(data, &b); err != nil {
			continue
		}
		out = append(out, &b)
	}
	return out, total, rows.Err()
}

// DeleteBook 删除书（级联删章节与任务）。
func (s *Store) DeleteBook(id int64) error {
	r, err := s.db.Exec(`DELETE FROM books WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------- Chapter ----------------

// ReplaceChapters 全量替换目录（书源刷新目录用）。
// 断点续传关键：按 URL 合并旧目录 —— 已缓存正文（status=done）与失败标记的章节
// 保留其状态与内容，仅 URL 变化（源更新）时重置；新章节插入，消失的章节删除。
func (s *Store) ReplaceChapters(bookID int64, chapters []*model.Chapter) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 旧章节按 URL 建索引（status/content/content_len/err_msg 以列为准）
	type oldCh struct {
		status     int
		content    string
		contentLen int
		errMsg     string
	}
	olds := map[string]oldCh{}
	rows, err := tx.Query(`SELECT url, status, content, content_len, err_msg FROM chapters WHERE book_id=?`, bookID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u string
		var st, cl int
		var content, errMsg string
		if err := rows.Scan(&u, &st, &content, &cl, &errMsg); err == nil {
			olds[u] = oldCh{status: st, content: content, contentLen: cl, errMsg: errMsg}
		}
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM chapters WHERE book_id=?`, bookID); err != nil {
		return err
	}
	for i, ch := range chapters {
		ch.BookID = bookID
		ch.Index = i
		// 合并：同 URL 且已完成（或失败）→ 保留原状态与已缓存内容
		if o, ok := olds[ch.URL]; ok {
			switch model.ChapterStatus(o.status) {
			case model.ChapterDone:
				ch.Status = int(model.ChapterDone)
				ch.Content = o.content
				ch.ContentLen = o.contentLen
				ch.ErrMsg = ""
			case model.ChapterFailed:
				ch.Status = int(model.ChapterFailed)
				ch.ErrMsg = o.errMsg
			}
		}
		data, err := marshal(ch)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO chapters(book_id,idx,url,title,is_volume,status,data,content,content_len,err_msg) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			bookID, i, ch.URL, ch.Title, b2i(ch.IsVolume), ch.Status, data, ch.Content, ch.ContentLen, ch.ErrMsg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListChapters 目录（status/content 等以列为准，覆盖 JSON 陈旧值）。
func (s *Store) ListChapters(bookID int64, withContent bool) ([]*model.Chapter, error) {
	cols := `data, status, content_len, err_msg`
	if withContent {
		cols = `data, status, content_len, err_msg, content`
	}
	rows, err := s.db.Query(`SELECT `+cols+` FROM chapters WHERE book_id=? ORDER BY idx ASC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Chapter
	for rows.Next() {
		var data string
		var status, contentLen int
		var errMsg string
		var content sql.NullString
		var scanErr error
		if withContent {
			scanErr = rows.Scan(&data, &status, &contentLen, &errMsg, &content)
		} else {
			scanErr = rows.Scan(&data, &status, &contentLen, &errMsg)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		var ch model.Chapter
		if err := jsonUnmarshal(data, &ch); err != nil {
			continue
		}
		ch.Status = status
		ch.ContentLen = contentLen
		ch.ErrMsg = errMsg
		if withContent {
			ch.Content = content.String
		}
		out = append(out, &ch)
	}
	return out, rows.Err()
}

// GetChapter 按序号取章节（含正文；status 列覆盖 JSON）。
func (s *Store) GetChapter(bookID int64, index int) (*model.Chapter, error) {
	var data string
	var status, contentLen int
	var errMsg string
	var content sql.NullString
	err := s.db.QueryRow(`SELECT data, status, content_len, err_msg, content FROM chapters WHERE book_id=? AND idx=?`, bookID, index).
		Scan(&data, &status, &contentLen, &errMsg, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var ch model.Chapter
	if err := jsonUnmarshal(data, &ch); err != nil {
		return nil, err
	}
	ch.Status = status
	ch.ContentLen = contentLen
	ch.ErrMsg = errMsg
	ch.Content = content.String
	return &ch, nil
}

// SaveChapterContent 保存章节正文与状态。
func (s *Store) SaveChapterContent(bookID, index int64, content string, status model.ChapterStatus, errMsg string) error {
	r, err := s.db.Exec(`UPDATE chapters SET content=?, status=?, content_len=?, err_msg=? WHERE book_id=? AND idx=?`,
		content, int(status), len([]rune(content)), errMsg, bookID, index)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FailedChapters 失败章节（含错误信息）。
func (s *Store) FailedChapters(bookID int64) ([]*model.Chapter, error) {
	rows, err := s.db.Query(`SELECT data, content_len, err_msg FROM chapters WHERE book_id=? AND status=3 ORDER BY idx ASC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Chapter
	for rows.Next() {
		var data string
		var contentLen int
		var errMsg string
		if err := rows.Scan(&data, &contentLen, &errMsg); err != nil {
			return nil, err
		}
		var ch model.Chapter
		if err := jsonUnmarshal(data, &ch); err != nil {
			continue
		}
		ch.Status = int(model.ChapterFailed)
		ch.ContentLen = contentLen
		ch.ErrMsg = errMsg
		out = append(out, &ch)
	}
	return out, rows.Err()
}

// PendingChapterIDs 待下载章节序号。
func (s *Store) PendingChapterIDs(bookID int64) ([]int, error) {
	rows, err := s.db.Query(`SELECT idx FROM chapters WHERE book_id=? AND status IN (0,3) AND is_volume=0 ORDER BY idx`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var i int
		if err := rows.Scan(&i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// MarkStaleTasksPaused 服务启动时将运行中/待处理/导出中的僵尸任务标记为已暂停（可恢复）。
func (s *Store) MarkStaleTasksPaused() (int64, error) {
	r, err := s.db.Exec(`UPDATE tasks SET status=2, updated_at=? WHERE status IN (0,1,3)`, NowMS())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ResetFailedChapters 重置失败章节为待下载。
func (s *Store) ResetFailedChapters(bookID int64) error {
	_, err := s.db.Exec(`UPDATE chapters SET status=0, err_msg='' WHERE book_id=? AND status=3`, bookID)
	return err
}

// CountChapterStatus 统计章节状态。
func (s *Store) CountChapterStatus(bookID int64) (total, done, failed int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status=2 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status=3 THEN 1 ELSE 0 END), 0)
		FROM chapters WHERE book_id=? AND is_volume=0`, bookID).Scan(&total, &done, &failed)
	return
}

// ---------------- Task ----------------

// CreateTask 创建任务。
func (s *Store) CreateTask(t *model.DownloadTask) (int64, error) {
	now := NowMS()
	t.CreatedAt = now
	t.UpdatedAt = now
	res, err := s.db.Exec(`INSERT INTO tasks(book_id,status,total_chapters,done_chapters,fail_chapters,data,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		t.BookID, int(t.Status), t.TotalChapters, t.DoneChapters, t.FailChapters, "{}", t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// ID 回写 JSON（GetTask 依赖 data 内的完整实体）
	t.ID = id
	if data, err := marshal(t); err == nil {
		_, _ = s.db.Exec(`UPDATE tasks SET data=? WHERE id=?`, data, id)
	}
	return id, nil
}

// UpdateTask 更新任务。
func (s *Store) UpdateTask(t *model.DownloadTask) error {
	data, err := marshal(t)
	if err != nil {
		return err
	}
	t.UpdatedAt = NowMS()
	_, err = s.db.Exec(`UPDATE tasks SET status=?,total_chapters=?,done_chapters=?,fail_chapters=?,data=?,updated_at=? WHERE id=?`,
		int(t.Status), t.TotalChapters, t.DoneChapters, t.FailChapters, data, t.UpdatedAt, t.ID)
	return err
}

// GetTask 按 ID 取任务（status 以列为准，覆盖 JSON 陈旧值）。
func (s *Store) GetTask(id int64) (*model.DownloadTask, error) {
	var data string
	var status int
	err := s.db.QueryRow(`SELECT data, status FROM tasks WHERE id=?`, id).Scan(&data, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var t model.DownloadTask
	if err := jsonUnmarshal(data, &t); err != nil {
		return nil, err
	}
	t.Status = model.TaskStatus(status)
	return &t, nil
}

// ListTasks 任务列表。
func (s *Store) ListTasks(limit int) ([]*model.DownloadTask, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT data, status FROM tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.DownloadTask
	for rows.Next() {
		var data string
		var status int
		if err := rows.Scan(&data, &status); err != nil {
			return nil, err
		}
		var t model.DownloadTask
		if err := jsonUnmarshal(data, &t); err != nil {
			continue
		}
		t.Status = model.TaskStatus(status)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// RunningTasks 运行中任务。
func (s *Store) RunningTasks() ([]*model.DownloadTask, error) {
	rows, err := s.db.Query(`SELECT data, status FROM tasks WHERE status IN (0,1,3) ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.DownloadTask
	for rows.Next() {
		var data string
		var status int
		if err := rows.Scan(&data, &status); err != nil {
			return nil, err
		}
		var t model.DownloadTask
		if err := jsonUnmarshal(data, &t); err != nil {
			continue
		}
		t.Status = model.TaskStatus(status)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// FindBookByURLAndOrigin 按 bookUrl+origin 精确查找书架记录。
func (s *Store) FindBookByURLAndOrigin(bookURL, origin string) (*model.Book, error) {
	var data string
	err := s.db.QueryRow(`SELECT data FROM books WHERE book_url=? AND origin=?`, bookURL, origin).Scan(&data)
	if err != nil {
		return nil, err
	}
	var b model.Book
	if err := jsonUnmarshal(data, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// TasksByBook 某书的全部任务（按 id 降序，最新在前）。
func (s *Store) TasksByBook(bookID int64) ([]*model.DownloadTask, error) {
	rows, err := s.db.Query(`SELECT data, status FROM tasks WHERE book_id=? ORDER BY id DESC`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.DownloadTask
	for rows.Next() {
		var data string
		var status int
		if err := rows.Scan(&data, &status); err != nil {
			return nil, err
		}
		var t model.DownloadTask
		if err := jsonUnmarshal(data, &t); err != nil {
			continue
		}
		t.Status = model.TaskStatus(status)
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ActiveTaskByBook 某书的活动任务（running/pending/exporting，最新优先），无则 nil。
func (s *Store) ActiveTaskByBook(bookID int64) (*model.DownloadTask, error) {
	tasks, err := s.TasksByBook(bookID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		switch t.Status {
		case model.TaskRunning, model.TaskPending, model.TaskExporting:
			return t, nil
		}
	}
	return nil, ErrNotFound
}

// ClearFinishedTasks 删除已完成/已取消的任务记录，返回删除数。
// 关联的导出记录保留（成品文件仍在磁盘）。
func (s *Store) ClearFinishedTasks() (int64, error) {
	r, err := s.db.Exec(`DELETE FROM tasks WHERE status IN (4, 6)`) // done=4, canceled=6
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// ---------------- Export ----------------

// AddExport 记录导出文件。
func (s *Store) AddExport(taskID, bookID int64, name, format, path string, size int64) error {
	_, err := s.db.Exec(`INSERT INTO exports(task_id,book_id,name,format,size,path,created_at) VALUES(?,?,?,?,?,?,?)`,
		taskID, bookID, name, format, size, path, NowMS())
	return err
}

// ListExports 导出记录。
// scanExportRow 扫描导出行并组装输出（originName 从任务 JSON 提取，供前端显示来源）。
func scanExportRow(rows *sql.Rows) ([]map[string]any, error) {
	var out []map[string]any
	for rows.Next() {
		var id, taskID, bookID, size, createdAt int64
		var name, format, path string
		var taskData []byte
		if err := rows.Scan(&id, &taskID, &bookID, &name, &format, &size, &path, &createdAt, &taskData); err != nil {
			return nil, err
		}
		originName := ""
		var td struct {
			OriginName string `json:"originName"`
		}
		if json.Unmarshal(taskData, &td) == nil {
			originName = td.OriginName
		}
		out = append(out, map[string]any{
			"id": id, "taskId": taskID, "bookId": bookID, "name": name,
			"format": format, "size": size, "path": path, "createdAt": createdAt,
			"originName": originName,
		})
	}
	return out, rows.Err()
}

func (s *Store) ListExports(limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT e.id,e.task_id,e.book_id,e.name,e.format,e.size,e.path,e.created_at,t.data
		FROM exports e LEFT JOIN tasks t ON t.id=e.task_id ORDER BY e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExportRow(rows)
}

// GetExport 单条导出记录（含 path/name/format 等）。
func (s *Store) GetExport(id int64) (map[string]any, error) {
	row := s.db.QueryRow(`SELECT e.id,e.task_id,e.book_id,e.name,e.format,e.size,e.path,e.created_at,t.data
		FROM exports e LEFT JOIN tasks t ON t.id=e.task_id WHERE e.id=?`, id)
	var id_, taskID, bookID, size, createdAt int64
	var name, format, path string
	var taskData []byte
	if err := row.Scan(&id_, &taskID, &bookID, &name, &format, &size, &path, &createdAt, &taskData); err != nil {
		return nil, err
	}
	originName := ""
	var td struct {
		OriginName string `json:"originName"`
	}
	if json.Unmarshal(taskData, &td) == nil {
		originName = td.OriginName
	}
	return map[string]any{
		"id": id_, "taskId": taskID, "bookId": bookID, "name": name,
		"format": format, "size": size, "path": path, "createdAt": createdAt,
		"originName": originName,
	}, nil
}

// DeleteExport 删除单条导出记录（并删除磁盘文件，由调用方处理）。
func (s *Store) DeleteExport(id int64) (string, error) {
	var path string
	err := s.db.QueryRow(`SELECT path FROM exports WHERE id=?`, id).Scan(&path)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`DELETE FROM exports WHERE id=?`, id)
	return path, err
}

// DeleteExportsByBook 删除某书全部导出记录，返回被删文件路径列表。
func (s *Store) DeleteExportsByBook(bookID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM exports WHERE book_id=?`, bookID)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err == nil {
			paths = append(paths, p)
		}
	}
	rows.Close()
	_, err = s.db.Exec(`DELETE FROM exports WHERE book_id=?`, bookID)
	return paths, err
}

// SweepGhostExports 启动一致性清理：删除指向不存在文件的导出记录，
// 以及同一路径的重复旧记录（保留最新一条）。返回清理条数。
func (s *Store) SweepGhostExports() (int, error) {
	rows, err := s.db.Query(`SELECT e.id, e.path, b.id FROM exports e
		LEFT JOIN books b ON b.id = e.book_id
		ORDER BY e.id ASC`)
	if err != nil {
		return 0, err
	}
	type rec struct {
		id       int64
		path     string
		bookGone bool // 书已删除（级联后导出记录成为孤儿）
	}
	var recs []rec
	for rows.Next() {
		var r rec
		var bookID sql.NullInt64
		if err := rows.Scan(&r.id, &r.path, &bookID); err == nil {
			r.bookGone = !bookID.Valid // 书已删除 → books 行不存在（b.id 为 NULL）
			recs = append(recs, r)
		}
	}
	rows.Close()

	// 同路径保留最大 id；文件不存在的直接删；书已删的孤儿记录连文件一起删
	latestByPath := map[string]int64{}
	for _, r := range recs {
		if !r.bookGone && r.id > latestByPath[r.path] {
			latestByPath[r.path] = r.id
		}
	}
	var del []int64
	var delFiles []string
	for _, r := range recs {
		if r.bookGone {
			del = append(del, r.id) // 孤儿记录（书已删除）
			delFiles = append(delFiles, r.path)
			continue
		}
		if _, err := os.Stat(r.path); err != nil {
			del = append(del, r.id) // 幽灵记录（文件已丢失）
			continue
		}
		if latestByPath[r.path] != r.id {
			del = append(del, r.id) // 同路径旧重复记录
		}
	}
	if len(del) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	for _, id := range del {
		if _, err := tx.Exec(`DELETE FROM exports WHERE id=?`, id); err != nil {
			tx.Rollback()
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	// 孤儿记录对应的磁盘文件一并清理；随之变空的源子目录与书目录也清理
	srcDirs := map[string]bool{}
	bookDirs := map[string]bool{}
	for _, p := range delFiles {
		_ = os.Remove(p)
		// p = {outDir}/{书名(作者)}/{源名}/{书名(作者)}.ext
		srcDirs[filepath.Dir(p)] = true
		bookDirs[filepath.Dir(filepath.Dir(p))] = true
	}
	removeEmptyDirs(srcDirs)
	removeEmptyDirs(bookDirs)
	return len(del), nil
}

// removeEmptyDirs 删除空目录（仅当目录内无任何条目时；不存在或不空则保留）。
func removeEmptyDirs(dirs map[string]bool) {
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			continue
		}
		_ = os.Remove(dir)
	}
}

// ListExportsByBook 按书过滤导出记录。
func (s *Store) ListExportsByBook(bookID int64, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT e.id,e.task_id,e.book_id,e.name,e.format,e.size,e.path,e.created_at,t.data
		FROM exports e LEFT JOIN tasks t ON t.id=e.task_id WHERE e.book_id=? ORDER BY e.id DESC LIMIT ?`, bookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanExportRow(rows)
}

// BatchSourceAction 批量启用/禁用/删除书源，返回成功数。
func (s *Store) BatchSourceAction(urls []string, action string) (int, error) {
	n := 0
	for _, u := range urls {
		var err error
		switch action {
		case "enable":
			err = s.batchSetSourceEnabled(u, true)
		case "disable":
			err = s.batchSetSourceEnabled(u, false)
		case "delete":
			err = s.DeleteSource(u)
		}
		if err == nil {
			n++
		}
	}
	return n, nil
}

func (s *Store) batchSetSourceEnabled(url string, enabled bool) error {
	_, err := s.db.Exec(`UPDATE book_sources SET enabled=? WHERE url=?`, b2i(enabled), url)
	return err
}

// BatchRemoveBooks 批量移除书架书籍，返回成功数。
func (s *Store) BatchRemoveBooks(ids []int64) (int, error) {
	n := 0
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM books WHERE id=?`, id); err == nil {
			n++
		}
	}
	return n, nil
}

// ---------------- helpers ----------------

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

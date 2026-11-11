package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"booksmith/internal/export"
	"booksmith/internal/model"
	"booksmith/internal/store"
	"booksmith/internal/util"
	"booksmith/internal/webbook"
)

// Manager 下载任务管理器：异步流水线（目录 → 正文 → 导出），立即返回任务 ID。
type Manager struct {
	st     *store.Store
	svc    *webbook.Service
	bus    *EventBus
	outDir string

	mu        sync.Mutex
	running   map[int64]*taskControl
	pauseReq  map[int64]bool
	cancelReq map[int64]bool

	// ChapterConcurrency 章节并发数（任务未指定时）
	ChapterConcurrency int
	// ChapterRetries 章节重试次数
	ChapterRetries int
	// TocTimeout 目录拉取超时
	TocTimeout time.Duration
	// ChapterTimeout 单章抓取超时
	ChapterTimeout time.Duration
	// LocalCover 读取手动上传封面（main 装配 server 层实现，避免循环依赖）；nil 则无本地上传
	LocalCover func(bookID int64) *export.CoverImage
	// CoverProvider 按 URL 获取封面（两级缓存：磁盘 + 远程），优先于 DownloadCover 实时抓取
	CoverProvider func(rawURL string) *export.CoverImage
}

// taskControl 运行中任务控制块。
type taskControl struct {
	task   *model.DownloadTask
	book   *model.Book
	src    *model.BookSource
	cancel context.CancelFunc
}

// New 创建管理器。
func New(st *store.Store, svc *webbook.Service, bus *EventBus, outDir string) *Manager {
	if outDir == "" {
		outDir = "downloads"
	}
	_ = os.MkdirAll(outDir, 0o755)
	// 服务重启后的僵尸任务恢复：running/pending/exporting → paused（用户可手动恢复）
	if n, err := st.MarkStaleTasksPaused(); err == nil && n > 0 {
		fmt.Fprintln(os.Stderr, "[task] paused stale tasks on startup:", n)
	}
	// 导出记录一致性清理：指向已丢失文件的幽灵记录、同路径重复旧记录
	if n, err := st.SweepGhostExports(); err == nil && n > 0 {
		fmt.Fprintln(os.Stderr, "[task] swept ghost export records on startup:", n)
	}
	return &Manager{
		st:                 st,
		svc:                svc,
		bus:                bus,
		outDir:             outDir,
		running:            map[int64]*taskControl{},
		pauseReq:           map[int64]bool{},
		cancelReq:          map[int64]bool{},
		ChapterConcurrency: 6,
		ChapterRetries:     3,
		TocTimeout:         60 * time.Second,
		ChapterTimeout:     60 * time.Second,
	}
}

// OutDir 导出目录。
func (m *Manager) OutDir() string { return m.outDir }

// ErrNotFound 任务不存在。
var ErrNotFound = errors.New("任务不存在")

// Create 创建下载任务：立即返回，后台异步执行 目录→正文→导出 流水线。
// 详情拉取失败时回退使用搜索提供的书籍信息（legado 行为），不会让任务创建失败。
// CreateOptions 创建任务的可选项。
type CreateOptions struct {
	CandidateSources []model.CandidateSource // 自动换源候选（按优先级排序，不含主源）
}

func (m *Manager) Create(_ context.Context, book *model.Book, bs *model.BookSource, formats []string, rangeStart, rangeEnd, concurrency int, opts ...CreateOptions) (int64, bool, error) {
	if len(formats) == 0 {
		formats = []string{"txt", "epub"}
	}
	if book.Name == "" {
		return 0, false, fmt.Errorf("书名不能为空")
	}
	// 同书同源去重：已有活动任务（running/pending/exporting）则直接复用
	if book.BookURL != "" && bs.BookSourceURL != "" {
		if existing, err := m.st.FindBookByURLAndOrigin(book.BookURL, bs.BookSourceURL); err == nil {
			if at, err := m.st.ActiveTaskByBook(existing.ID); err == nil {
				return at.ID, true, nil
			}
		}
	}
	book.Origin = bs.BookSourceURL
	book.OriginName = bs.BookSourceName
	// 持久化书源（断点恢复/重试需要完整规则）
	if bs.BookSourceURL != "" {
		if err := m.st.UpsertSource(bs, bs.Custom); err != nil {
			return 0, false, fmt.Errorf("保存书源失败: %w", err)
		}
	}
	bookID, err := m.st.UpsertBook(book)
	if err != nil {
		return 0, false, fmt.Errorf("保存书籍失败: %w", err)
	}
	if concurrency <= 0 {
		concurrency = m.ChapterConcurrency
	}
	// 上限保护：并发数过高会同时轰炸目标站并拖垮 SQLite（前端上限 16，此处兜底 API 直调）
	if concurrency > 32 {
		concurrency = 32
	}
	// rangeEnd 未指定（0 或负数）= 全本下载；仅在明确传正值时按区间下载
	if rangeEnd <= 0 {
		rangeEnd = -1
	}
	t := &model.DownloadTask{
		BookID:      bookID,
		BookName:    book.Name,
		Author:      book.Author,
		Origin:      bs.BookSourceURL,
		OriginName:  bs.BookSourceName,
		Status:      model.TaskPending,
		Phase:       "toc",
		Formats:     formats,
		RangeStart:  rangeStart,
		RangeEnd:    rangeEnd,
		Concurrency: concurrency,
	}
	if len(opts) > 0 && len(opts[0].CandidateSources) > 0 {
		t.CandidateSources = opts[0].CandidateSources
	}
	taskID, err := m.st.CreateTask(t)
	if err != nil {
		return 0, false, err
	}
	go m.run(taskID, book, bs)
	return taskID, false, nil
}

// run 异步流水线：目录 → 正文 → 导出。
func (m *Manager) run(taskID int64, book *model.Book, bs *model.BookSource) {
	// 重入保护：check + register 原子完成，快速双击恢复/重跑不会启动两条流水线
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if _, ok := m.running[taskID]; ok {
		m.mu.Unlock()
		cancel()
		return
	}
	m.running[taskID] = &taskControl{book: book, src: bs, cancel: cancel}
	m.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			if t, err := m.st.GetTask(taskID); err == nil {
				t.Status = model.TaskFailed
				t.ErrMsg = fmt.Sprintf("任务执行异常: %v", r)
				_ = m.st.UpdateTask(t)
				m.publish(t, "task.error", t.ErrMsg)
			}
		}
	}()
	defer cancel()
	defer func() {
		m.mu.Lock()
		delete(m.running, taskID)
		m.mu.Unlock()
	}()

	t, err := m.st.GetTask(taskID)
	if err != nil {
		return
	}

	// ---------- 阶段 1：目录 ----------
	t.Status = model.TaskRunning
	t.Phase = "toc"
	_ = m.st.UpdateTask(t)
	m.publish(t, "task.progress", "获取目录中")

	chapters, total, rangeEnd, tocErr := m.fetchToc(t, book, bs)
	if tocErr != nil {
		// 自动换源：目录失败时依次尝试候选源（快捷下载注入；显式选源不启用）
		if len(t.CandidateSources) > 0 {
			switched := false
			for _, cand := range t.CandidateSources {
				// 记录失败源
				t.TriedSources = append(t.TriedSources, model.TriedSource{SourceName: bs.BookSourceName, Err: tocErr.Error()})
				// 换下一个候选
				candBs, serr := m.st.GetSource(cand.SourceURL)
				if serr != nil {
					t.TriedSources = append(t.TriedSources, model.TriedSource{SourceName: cand.SourceName, Err: "书源已不存在"})
					continue
				}
				m.publish(t, "task.progress", fmt.Sprintf("目录获取失败，切换源：%s", cand.SourceName))
				book.Origin = cand.SourceURL
				book.OriginName = cand.SourceName
				book.BookURL = cand.BookURL
				chapters, total, rangeEnd, tocErr = m.fetchToc(t, book, candBs)
				if tocErr == nil {
					bs = candBs
					t.TriedSources = append(t.TriedSources, model.TriedSource{SourceName: cand.SourceName, Err: ""})
					switched = true
					break
				}
			}
			if !switched {
				var hist strings.Builder
				for _, ts := range t.TriedSources {
					if ts.Err == "" {
						continue
					}
					fmt.Fprintf(&hist, "%s(%s)；", ts.SourceName, ts.Err)
				}
				t.Status = model.TaskFailed
				t.Phase = "toc"
				t.ErrMsg = "目录获取失败，已尝试全部候选源：" + hist.String()
				_ = m.st.UpdateTask(t)
				m.publish(t, "task.error", t.ErrMsg)
				return
			}
			t.Origin = book.Origin
			t.OriginName = book.OriginName
			t.ErrMsg = ""
			_ = m.st.UpdateTask(t)
			m.publish(t, "task.progress", "已切换至源："+t.OriginName)
		} else {
			t.Status = model.TaskFailed
			t.Phase = "toc"
			t.ErrMsg = tocErr.Error()
			_ = m.st.UpdateTask(t)
			m.publish(t, "task.error", t.ErrMsg)
			return
		}
	}
	if m.cancelRequested(taskID) {
		t.Status = model.TaskCanceled
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.done", "已取消")
		return
	}

	// ---------- 阶段 2：正文 ----------
	t.TotalChapters = total
	t.RangeEnd = rangeEnd
	t.Phase = "content"
	_ = m.st.UpdateTask(t)

	m.runContent(ctx, t, book, bs, chapters)

	// 重新载入统计
	t, _ = m.st.GetTask(taskID)
	if t == nil {
		return
	}

	if m.cancelRequested(taskID) {
		t.Status = model.TaskCanceled
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.done", "已取消")
		return
	}
	if m.pauseRequested(taskID) {
		t.Status = model.TaskPaused
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.paused", "已暂停")
		return
	}
	if t.FailChapters > 0 && t.DoneChapters == 0 {
		t.Status = model.TaskFailed
		t.ErrMsg = fmt.Sprintf("%d 章下载失败", t.FailChapters)
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.error", t.ErrMsg)
		return
	}

	// ---------- 阶段 3：导出 ----------
	t.Status = model.TaskExporting
	t.Phase = "export"
	_ = m.st.UpdateTask(t)
	m.publish(t, "task.progress", "开始导出")

	exportPath, err := m.doExport(taskID, book, t.Formats)
	if err != nil {
		t.Status = model.TaskFailed
		t.Phase = "export"
		t.ErrMsg = "导出失败: " + err.Error()
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.error", t.ErrMsg)
		return
	}
	t.Status = model.TaskDone
	t.Phase = ""
	t.ExportPath = exportPath
	if t.FailChapters > 0 {
		t.ErrMsg = fmt.Sprintf("%d 章下载失败（其余已导出）", t.FailChapters)
	}
	_ = m.st.UpdateTask(t)
	m.publish(t, "task.done", "完成")
}

// fetchToc 拉取目录（详情回退 + 目录超时），返回章节/正文总数/结束序号/错误。
func (m *Manager) fetchToc(t *model.DownloadTask, book *model.Book, bs *model.BookSource) ([]*model.Chapter, int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), m.TocTimeout)
	defer cancel()

	// 详情补全（尽力而为：失败回退搜索提供的信息，不中断任务）
	if info, err := m.svc.GetBookInfo(ctx, bs, book.BookURL); err == nil {
		if book.Name == "" {
			book.Name = info.Name
		}
		if info.Author != "" {
			book.Author = info.Author
		}
		if info.Intro != "" {
			book.Intro = info.Intro
		}
		book.Kind = info.Kind
		if info.CoverURL != "" {
			book.CoverURL = info.CoverURL
		}
		if info.LatestChapter != "" {
			book.LatestChapter = info.LatestChapter
		}
		book.TocURL = info.TocURL
	} else if debugTask {
		fmt.Fprintln(os.Stderr, "[debug] 详情回退:", err)
	}

	chapters, err := m.svc.GetToc(ctx, bs, book)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("获取目录失败: %w", err)
	}
	if len(chapters) == 0 {
		return nil, 0, 0, fmt.Errorf("目录为空（站点可能反爬或规则失效）")
	}
	if err := m.st.ReplaceChapters(t.BookID, chapters); err != nil {
		return nil, 0, 0, err
	}

	// 有效正文范围
	contentCount := 0
	for _, ch := range chapters {
		if !ch.IsVolume {
			contentCount++
		}
	}
	rangeEnd := t.RangeEnd
	if rangeEnd < 0 || rangeEnd >= contentCount {
		rangeEnd = contentCount - 1
	}
	rangeStart := t.RangeStart
	if rangeStart < 0 {
		rangeStart = 0
	}
	if rangeStart > rangeEnd {
		return nil, 0, 0, fmt.Errorf("章节范围无效（共 %d 章）", contentCount)
	}
	total := rangeEnd - rangeStart + 1
	return chapters, total, rangeEnd, nil
}

// runContent 并发抓取正文。
func (m *Manager) runContent(ctx context.Context, t *model.DownloadTask, book *model.Book, bs *model.BookSource, chapters []*model.Chapter) {
	// 下一章 URL 供正文翻页终止判断
	m.svc.NextChapterProvider = func(b *model.Book, index int) string {
		if index+1 < len(chapters) {
			return chapters[index+1].URL
		}
		return ""
	}

	// 待下载章节序号（跳过卷；跳过区间外）
	indexes := make([]int, 0, t.TotalChapters)
	contentIdx := -1
	for _, ch := range chapters {
		if ch.IsVolume {
			continue
		}
		contentIdx++
		if contentIdx < t.RangeStart || contentIdx > t.RangeEnd {
			continue
		}
		// 已完成的章节跳过（断点续传）
		if existing, err := m.st.GetChapter(book.ID, ch.Index); err == nil && model.ChapterStatus(existing.Status) == model.ChapterDone {
			continue
		}
		indexes = append(indexes, ch.Index)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < max(1, t.Concurrency); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if m.checkPauseCancel(t.ID) {
					continue
				}
				m.fetchChapter(ctx, t.ID, book, bs, idx)
			}
		}()
	}

deliver:
	for _, idx := range indexes {
		select {
		case <-ctx.Done():
			break deliver
		default:
		}
		if m.checkPauseCancel(t.ID) {
			break deliver
		}
		select {
		case jobs <- idx:
		case <-ctx.Done():
			break deliver
		}
	}
	close(jobs)
	wg.Wait()
}

// checkPauseCancel 是否有暂停/取消请求。
func (m *Manager) checkPauseCancel(taskID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pauseReq[taskID] || m.cancelReq[taskID]
}

func (m *Manager) cancelRequested(taskID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancelReq[taskID]
}

func (m *Manager) pauseRequested(taskID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pauseReq[taskID]
}

// fetchChapter 抓取并保存单章（带重试与单章超时；规则异常不杀死进程）。
func (m *Manager) fetchChapter(ctx context.Context, taskID int64, book *model.Book, bs *model.BookSource, index int) {
	defer func() {
		if r := recover(); r != nil {
			_ = m.st.SaveChapterContent(book.ID, int64(index), "", model.ChapterFailed,
				fmt.Sprintf("章节解析异常: %v", r))
			m.bumpProgress(taskID)
		}
	}()
	var lastErr error
	for attempt := 0; attempt <= max(0, m.ChapterRetries); attempt++ {
		if m.checkPauseCancel(taskID) {
			return
		}
		ch, err := m.st.GetChapter(book.ID, index)
		if err != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, m.ChapterTimeout)
		text, err := m.svc.GetContent(cctx, bs, book, ch)
		cancel()
		if err == nil && text != "" {
			_ = m.st.SaveChapterContent(book.ID, int64(index), text, model.ChapterDone, "")
			m.bumpProgress(taskID)
			return
		}
		if err == nil && text == "" {
			err = fmt.Errorf("正文为空（可能被反爬或规则失效）")
		}
		lastErr = err
		// 限流（429）用长退避：2s/4s/8s/16s/32s；其余错误 400ms 递增。
		// 注意：退避等待放在下次请求前，不计入单章超时（ChapterTimeout 只覆盖请求本身）。
		var wait time.Duration
		if err != nil && strings.Contains(err.Error(), "429") {
			wait = time.Duration(2<<attempt) * time.Second
			if wait > 32*time.Second {
				wait = 32 * time.Second
			}
		} else {
			wait = time.Duration(400*(attempt+1)) * time.Millisecond
		}
		if m.checkPauseCancel(taskID) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	_ = m.st.SaveChapterContent(book.ID, int64(index), "", model.ChapterFailed, errMsg)
	m.bumpProgress(taskID)
}

// bumpProgress 进度更新。
func (m *Manager) bumpProgress(taskID int64) {
	t, err := m.st.GetTask(taskID)
	if err != nil {
		return
	}
	_, done, failed, _ := m.st.CountChapterStatus(t.BookID)
	t.DoneChapters = done
	t.FailChapters = failed
	_ = m.st.UpdateTask(t)
	m.publish(t, "task.progress", "")
}

// publish 推送事件。
func (m *Manager) publish(t *model.DownloadTask, typ, msg string) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(Event{
		Type:   typ,
		TaskID: t.ID,
		Data: ProgressData{
			BookName: t.BookName, Done: t.DoneChapters, Failed: t.FailChapters,
			Total: t.TotalChapters, Status: model.TaskStatusName(t.Status), Phase: t.Phase, Msg: msg,
		},
	})
}

// ExportNow 纯导出：从已缓存章节直接生成文件（不抓取），返回导出路径。
func (m *Manager) ExportNow(taskID int64, book *model.Book, formats []string) (string, error) {
	return m.doExport(taskID, book, formats)
}

// removeOldExports 删除该书旧的导出文件与记录（新导出生成前调用，保证成品始终对应最新缓存）。
func (m *Manager) removeOldExports(bookID int64) {
	paths, err := m.st.DeleteExportsByBook(bookID)
	if err != nil {
		return
	}
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// doExport 按格式导出（章节缓存复用）。
func (m *Manager) doExport(taskID int64, book *model.Book, formats []string) (result string, err error) {
	// panic 兜底：任何意外（字体解析、图片解码、内存）都不让任务静默卡在 exporting。
	// panic 转为 error 返回给调用方（同步 ExportNow 路径标 failed；异步 run 路径由 run 的 recover 记录）
	defer func() {
		if r := recover(); r != nil {
			result = ""
			err = fmt.Errorf("导出内部异常: %v\n%s", r, debug.Stack())
		}
	}()
	allChapters, err := m.st.ListChapters(book.ID, true)
	if err != nil {
		return "", err
	}
	// 新导出生成前清理旧成品（内容已随缓存更新，旧文件不再有意义）
	m.removeOldExports(book.ID)
	ctxData := &export.ExportContext{
		Book: export.BookMeta{
			Name:       book.Name,
			Author:     book.Author,
			Intro:      book.Intro,
			CoverURL:   book.CoverURL,
			SourceName: book.OriginName,
		},
	}
	// 手动上传的封面优先（EPUB/PDF 导出共用）
	if m.LocalCover != nil {
		if lc := m.LocalCover(book.ID); lc != nil {
			ctxData.Book.LocalCover = lc
		}
	}
	// 封面缓存提供器（磁盘缓存优先，解决远程站临时失败导致导出无封面）
	ctxData.CoverProvider = m.CoverProvider
	// 正文插图提供器（与封面共用磁盘缓存链路）
	ctxData.ImageProvider = func(rawURL string) ([]byte, string, bool) {
		if m.CoverProvider == nil {
			return nil, "", false
		}
		if ci := m.CoverProvider(rawURL); ci != nil {
			return ci.Data, ci.MimeType, true
		}
		return nil, "", false
	}
	chapterList := make([]export.ChapterMeta, 0, len(allChapters))
	curVolume := ""
	num := 0
	for _, ch := range allChapters {
		if ch.IsVolume {
			curVolume = ch.Title
			chapterList = append(chapterList, export.ChapterMeta{Index: ch.Index, IsVolume: true, Title: ch.Title})
			continue
		}
		// 只导出已缓存正文的章节：失败/未下载的章节进文件只会产生空目录
		if model.ChapterStatus(ch.Status) != model.ChapterDone || ch.Content == "" {
			continue
		}
		num++
		content := ch.Content
		// 去重复标题（正文首行为书名/章节名时删除）
		content = dropDupTitle(content, book.Name, ch.Title)
		chapterList = append(chapterList, export.ChapterMeta{
			Index: ch.Index, Number: num, Volume: curVolume, Title: ch.Title, Content: content,
		})
	}
	ctxData.Book.Chapters = chapterList

	// 零正文保护：全部章节都失败/未缓存时导出只会产出空壳文件
	if len(chapterList) == 0 {
		return "", fmt.Errorf("没有可导出的章节内容（正文全部未缓存）")
	}

	paths := make([]string, 0, len(formats))
	for _, f := range formats {
		exp, err := export.ForFormat(export.Format(f))
		if err != nil {
			continue
		}
		var buf syncBuffer
		if err := exp.Export(ctxData, &buf); err != nil {
			return "", fmt.Errorf("%s 导出失败: %w", f, err)
		}
		// 目录管理：downloads/{书名(作者)}/{源名}/{书名(作者)}.ext
		// 文件名不含源名（来源在目录层级中），同书同源重导出覆盖旧文件
		bookDir := util.SanitizeFileName(book.Name + "(" + book.Author + ")")
		srcDir := util.SanitizeFileName(book.OriginName)
		dir := filepath.Join(m.outDir, bookDir, srcDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		name := util.SanitizeFileName(book.Name + "(" + book.Author + ")." + exp.Ext())
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			return "", err
		}
		_ = m.st.AddExport(taskID, book.ID, filepath.Base(path), f, path, int64(buf.Len()))
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("无可导出格式")
	}
	return paths[0], nil
}

// dropDupTitle 导出前去重复标题。
func dropDupTitle(content, bookName, title string) string {
	first, rest, ok := strings.Cut(content, "\n")
	if !ok {
		first, rest = content, ""
	}
	first = strings.TrimSpace(first)
	if first == "" {
		return content
	}
	if (bookName != "" && strings.Contains(first, bookName)) || (title != "" && strings.Contains(first, title)) {
		return strings.TrimLeft(rest, " \t")
	}
	return content
}

// GetTask 获取任务。
func (m *Manager) GetTask(id int64) (*model.DownloadTask, error) { return m.st.GetTask(id) }

// ListTasks 任务列表。
func (m *Manager) ListTasks(limit int) ([]*model.DownloadTask, error) { return m.st.ListTasks(limit) }

// Pause 暂停任务。
func (m *Manager) Pause(id int64) error {
	t, err := m.st.GetTask(id)
	if err != nil {
		return err
	}
	if t.Status != model.TaskRunning && t.Status != model.TaskPending {
		return fmt.Errorf("任务状态 %s 不可暂停", model.TaskStatusName(t.Status))
	}
	m.mu.Lock()
	m.pauseReq[id] = true
	m.mu.Unlock()
	return nil
}

// Resume 恢复任务：无目录时重跑目录阶段，否则续传未完成章节。
func (m *Manager) Resume(id int64) error {
	t, err := m.st.GetTask(id)
	if err != nil {
		return err
	}
	if t.Status != model.TaskPaused && t.Status != model.TaskFailed && t.Status != model.TaskCanceled {
		return fmt.Errorf("任务状态 %s 不可恢复", model.TaskStatusName(t.Status))
	}
	m.mu.Lock()
	delete(m.pauseReq, id)
	delete(m.cancelReq, id)
	m.mu.Unlock()

	book, err := m.st.GetBook(t.BookID)
	if err != nil {
		return err
	}
	bs := &model.BookSource{BookSourceURL: t.Origin, BookSourceName: book.OriginName}
	if full, err := m.st.GetSource(t.Origin); err == nil {
		bs = full
	} else {
		// 书源已被清理/删除：空壳规则必然目录失败，提前明确报错而非静默失败
		return fmt.Errorf("书源已不存在，无法恢复下载（可在书架移除后重新搜索）")
	}
	// 失败章节重置为待下载
	if t.Phase != "toc" {
		_ = m.st.ResetFailedChapters(t.BookID)
	}
	t.Status = model.TaskPending
	t.ErrMsg = ""
	_ = m.st.UpdateTask(t)
	go m.run(t.ID, book, bs)
	return nil
}

// Cancel 取消任务。
func (m *Manager) Cancel(id int64) error {
	t, err := m.st.GetTask(id)
	if err != nil {
		return err
	}
	if t.Status == model.TaskDone || t.Status == model.TaskCanceled {
		return nil
	}
	m.mu.Lock()
	m.cancelReq[id] = true
	tc, running := m.running[id]
	m.mu.Unlock()
	if running && tc != nil {
		tc.cancel()
		return nil
	}
	// 非运行态（paused/failed/pending 未调度）：直接落终态，否则标志无人消费、任务永远卡住
	if t.Status == model.TaskPaused || t.Status == model.TaskFailed {
		t.Status = model.TaskCanceled
		_ = m.st.UpdateTask(t)
		m.publish(t, "task.done", "已取消")
	}
	return nil
}

// RetryFailed 重跑失败章节。
// 任务可能处于 done（部分失败但已导出，书架"重跑失败"入口）、failed、paused、canceled。
func (m *Manager) RetryFailed(id int64) error {
	t, err := m.st.GetTask(id)
	if err != nil {
		return err
	}
	if err := m.st.ResetFailedChapters(t.BookID); err != nil {
		return err
	}
	// done 任务的 Resume 会被状态机拒绝（done 不在可恢复集合），
	// 这里直接解除阻塞并重跑，绕过 Resume 的状态检查。
	m.mu.Lock()
	delete(m.pauseReq, id)
	delete(m.cancelReq, id)
	m.mu.Unlock()

	book, err := m.st.GetBook(t.BookID)
	if err != nil {
		return err
	}
	bs, serr := m.st.GetSource(t.Origin)
	if serr != nil {
		return fmt.Errorf("书源已不存在，无法重跑（可在书架移除后重新搜索）")
	}
	t.Status = model.TaskPending
	t.Phase = "toc" // run 总是先重拉目录（可能已更新），从 toc 开始
	t.ErrMsg = ""
	_ = m.st.UpdateTask(t)
	go m.run(t.ID, book, bs)
	return nil
}

// IsRunning 任务是否运行中。
func (m *Manager) IsRunning(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// debugTask 调试日志开关。
var debugTask = os.Getenv("BS_DEBUG") != ""

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

	// exportLocks 同书导出串行化锁（bookID → *sync.Mutex）：并发导出会互删对方成品
	exportLocks sync.Map
	// exporterOverride 测试注入点（nil 时走 export.ForFormat）
	exporterOverride func(f export.Format) (export.Exporter, error)
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
	// 清理崩溃遗留的导出暂存目录（doExport 用 .export-tmp-* 暂存，正常路径必清；
	// 进程被 kill 时可能遗留——真实书目录经 SanitizeFileName 不可能以 "." 开头，无冲突）
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".export-tmp-") {
				_ = os.RemoveAll(filepath.Join(outDir, e.Name()))
			}
		}
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
	// 同书同源去重 + 建任务放进临界区：check-then-act 分离时两个并发创建
	// 会产生同书双流水线（章节状态互相错配、导出互相覆盖）。m.mu 同时保护
	// pauseReq/cancelReq map，临界区内只做 store 操作（无嵌套锁，无死锁面）。
	m.mu.Lock()
	// 已有活动任务（running/pending/exporting）则直接复用
	if book.BookURL != "" && bs.BookSourceURL != "" {
		if existing, err := m.st.FindBookByURLAndOrigin(book.BookURL, bs.BookSourceURL); err == nil {
			if at, err := m.st.ActiveTaskByBook(existing.ID); err == nil {
				m.mu.Unlock()
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
		m.mu.Unlock()
		return 0, false, err
	}
	m.mu.Unlock()
	// go m.run 放在解锁后：run 内部立即取 m.mu（重入保护），持锁启动会引入无谓的锁排队
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
			// 原源失败只记一次（循环内再记会把原源名重复 n+1 次）
			t.TriedSources = append(t.TriedSources, model.TriedSource{SourceName: bs.BookSourceName, Err: tocErr.Error()})
			for _, cand := range t.CandidateSources {
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
				// 该候选源失败：记录其自身的失败原因（供最终错误信息排查）
				t.TriedSources = append(t.TriedSources, model.TriedSource{SourceName: cand.SourceName, Err: tocErr.Error()})
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
		// 作者防御：书源详情规则可能抓错元素（如简介正文），几百字脏值会覆盖正确作者
		// 并撑爆导出目录名（mkdir ENAMETOOLONG）。非空且不超合理长度才覆盖。
		if info.Author != "" && len([]rune(info.Author)) <= 50 {
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
	// 下一章 URL 映射：正文翻页终止判断用（显式传参给 GetContent，
	// 不能放 Service 单例字段上——并发任务会互相串台）
	nextURLByIndex := make(map[int]string, len(chapters))
	for i, ch := range chapters {
		if i+1 < len(chapters) {
			nextURLByIndex[ch.Index] = chapters[i+1].URL
		}
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
				m.fetchChapter(ctx, t.ID, book, bs, idx, nextURLByIndex)
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
// nextURLByIndex：章节序号 → 下一章 URL（正文翻页终止判断）。
func (m *Manager) fetchChapter(ctx context.Context, taskID int64, book *model.Book, bs *model.BookSource, index int, nextURLByIndex map[int]string) {
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
		text, err := m.svc.GetContent(cctx, bs, book, ch, nextURLByIndex[ch.Index])
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

// doExport 按格式导出（章节缓存复用）。
// 核心保证：**任何生成失败都不碰旧成品**——先写临时暂存目录，全部格式成功后
// 才删除旧记录/旧文件并原子搬入新文件；同书并发导出按 bookID 串行化。
func (m *Manager) doExport(taskID int64, book *model.Book, formats []string) (result string, err error) {
	// panic 兜底：任何意外（字体解析、图片解码、内存）都不让任务静默卡在 exporting。
	// panic 转为 error 返回给调用方（同步 ExportNow 路径标 failed；异步 run 路径由 run 的 recover 记录）
	defer func() {
		if r := recover(); r != nil {
			result = ""
			err = fmt.Errorf("导出内部异常: %v\n%s", r, debug.Stack())
		}
	}()
	// 同书串行化：两个并发导出若都走「删旧→写新」会互删对方刚写的文件
	lockAny, _ := m.exportLocks.LoadOrStore(book.ID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	allChapters, err := m.st.ListChapters(book.ID, true)
	if err != nil {
		return "", err
	}
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

	// 先写临时暂存目录（在 outDir 内保证同卷 rename）：全部格式成功后才替换旧成品——
	// 任何生成失败（字体/图片解码/磁盘/权限）都不碰旧文件，彻底消除「导出失败 = 旧文件丢失」
	stageDir, err := os.MkdirTemp(m.outDir, ".export-tmp-")
	if err != nil {
		return "", fmt.Errorf("创建暂存目录失败: %w", err)
	}
	defer os.RemoveAll(stageDir) // 成功路径暂存文件已被 rename 走，RemoveAll 只清残留

	bookBase := util.SafeBookFileName(book.Name, book.Author) // 书名/作者各自字节截断（ext4 NAME_MAX=255）
	srcDir := util.SanitizeFileName(book.OriginName)
	finalDir := filepath.Join(m.outDir, bookBase, srcDir)

	type stagedFile struct {
		path   string // 暂存文件绝对路径
		format string
	}
	staged := make([]stagedFile, 0, len(formats))
	for _, f := range formats {
		exp, err := m.exporterFor(export.Format(f))
		if err != nil {
			continue
		}
		var buf syncBuffer
		if err := exp.Export(ctxData, &buf); err != nil {
			return "", fmt.Errorf("%s 导出失败: %w", f, err)
		}
		name := bookBase + "." + exp.Ext() // 扩展名不参与截断
		sp := filepath.Join(stageDir, name)
		if err := os.WriteFile(sp, buf.Bytes(), 0o644); err != nil {
			return "", fmt.Errorf("写入暂存目录失败: %w", err)
		}
		staged = append(staged, stagedFile{path: sp, format: f})
	}
	if len(staged) == 0 {
		return "", fmt.Errorf("无可导出格式")
	}

	// ---- 生成全部成功，现在才替换旧成品 ----
	// MkdirAll 提前（幂等无副作用）：删旧成品后唯一允许的失败是 rename，
	// 而 rename 失败有 reAdd 恢复路径——确保任何失败都不丢旧成品。
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return "", fmt.Errorf("创建导出目录失败: %w", err)
	}
	oldPaths, _ := m.st.DeleteExportsByBook(book.ID)
	for _, p := range oldPaths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
	finalPaths := make([]string, 0, len(staged))
	for _, sf := range staged {
		fp := filepath.Join(finalDir, filepath.Base(sf.path))
		_ = os.Remove(fp) // Windows 上 Rename 不覆盖已存在目标；清掉无记录残留旧文件
		if err := os.Rename(sf.path, fp); err != nil {
			// 恢复：重新登记磁盘上仍存在的旧成品与已搬入的新成品（按路径去重，
			// 新旧同名时只登记一份，保持 DB 与磁盘一致）
			m.reAddExports(taskID, book.ID, append(oldPaths, finalPaths...))
			return "", fmt.Errorf("替换成品文件失败: %w", err)
		}
		finalPaths = append(finalPaths, fp)
	}
	for _, fp := range finalPaths {
		fi, _ := os.Stat(fp)
		size := int64(0)
		if fi != nil {
			size = fi.Size()
		}
		_ = m.st.AddExport(taskID, book.ID, filepath.Base(fp), formatByExt(fp), fp, size)
	}
	return finalPaths[0], nil
}

// exporterFor 获取格式导出器（测试可注入 override；生产走 export.ForFormat）。
func (m *Manager) exporterFor(f export.Format) (export.Exporter, error) {
	if m.exporterOverride != nil {
		return m.exporterOverride(f)
	}
	return export.ForFormat(f)
}

// reAddExports 恢复登记磁盘上仍存在的成品记录（替换中途失败时保持 DB 与磁盘一致）。
// 按路径去重：新旧成品同名时只登记一份。
func (m *Manager) reAddExports(taskID, bookID int64, paths []string) {
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			_ = m.st.AddExport(taskID, bookID, filepath.Base(p), formatByExt(p), p, fi.Size())
		}
	}
}

// formatByExt 由扩展名推导导出格式名（恢复登记用；未知扩展名原样返回小写）。
func formatByExt(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
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
	// exporting 阶段不可取消：doExport 不接收 ctx，取消请求无人消费——
	// 返回成功会让用户误以为已取消而任务照常完成（与 Pause 守卫对齐）
	if t.Status == model.TaskExporting {
		return fmt.Errorf("任务正在导出，不可取消")
	}
	m.mu.Lock()
	m.cancelReq[id] = true
	tc, running := m.running[id]
	m.mu.Unlock()
	if running && tc != nil {
		tc.cancel()
		return nil
	}
	// pending 未调度但 run goroutine 可能即将消费 cancelReq：置标志后落终态由 run 检查
	if t.Status == model.TaskPending {
		return nil // cancelReq 已置位，run 在阶段检查点落 canceled
	}
	// 非运行态（paused/failed）：直接落终态，否则标志无人消费、任务永远卡住
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
	// 状态守卫：运行中/排队/导出中的任务禁止重跑——重置失败章节会与活动流水线
	// 交叉（重置的章节不在当前 indexes 里 → 任务显示 done 却有 pending 章节）
	switch t.Status {
	case model.TaskRunning, model.TaskPending, model.TaskExporting:
		return fmt.Errorf("任务正在运行，无法重跑失败章节")
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

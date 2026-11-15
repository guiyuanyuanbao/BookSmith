package task

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"booksmith/internal/export"
	"booksmith/internal/model"
	"booksmith/internal/store"
)

// ---------- 测试桩 ----------

// okExporter 恒成功导出器（产出可识别内容）。
type okExporter struct {
	format export.Format
	ext    string
}

func (e *okExporter) Format() export.Format                            { return e.format }
func (e *okExporter) Ext() string                                      { return e.ext }
func (e *okExporter) Export(ctx *export.ExportContext, w io.Writer) error {
	_, err := w.Write([]byte("新成品-" + e.ext))
	return err
}

// failExporter 恒失败导出器。
type failExporter struct {
	format export.Format
	ext    string
}

func (e *failExporter) Format() export.Format                            { return e.format }
func (e *failExporter) Ext() string                                      { return e.ext }
func (e *failExporter) Export(ctx *export.ExportContext, w io.Writer) error {
	return fmt.Errorf("模拟导出失败")
}

// ---------- 场景 ----------

// exportTestEnv 导出测试环境：独立 DB + outDir + 一本带缓存正文的测试书。
type exportTestEnv struct {
	m      *Manager
	st     *store.Store
	book   *model.Book
	outDir string
}

func newExportTestEnv(t *testing.T) *exportTestEnv {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	outDir := t.TempDir()
	m := New(st, nil, nil, outDir)
	book := bookFixtureForExport()
	bookID, err := st.UpsertBook(book)
	if err != nil {
		t.Fatal(err)
	}
	book.ID = bookID
	ch := &model.Chapter{BookID: book.ID, Index: 0, Title: "第一章", URL: "http://c/1"}
	if err := st.ReplaceChapters(book.ID, []*model.Chapter{ch}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveChapterContent(book.ID, 0, "正文内容", 2, ""); err != nil {
		t.Fatal(err)
	}
	return &exportTestEnv{m: m, st: st, book: book, outDir: outDir}
}

// seedOldExport 预置一条旧成品记录 + 磁盘文件，返回 (记录路径, 文件内容)。
func seedOldExport(t *testing.T, env *exportTestEnv, taskID int64, ext, content string) string {
	t.Helper()
	finalDir := filepath.Join(env.outDir, "测试书(测试作者)", "测试源")
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(finalDir, "测试书(测试作者)."+ext)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := env.st.AddExport(taskID, env.book.ID, filepath.Base(p), ext, p, int64(len(content))); err != nil {
		t.Fatal(err)
	}
	return p
}

func (env *exportTestEnv) exportRecordPaths(bookID int64) []string {
	// DeleteExportsByBook 会删记录，这里用 ListExportsByBook 只读快照
	recs, err := env.st.ListExportsByBook(bookID, 100)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(recs))
	for _, r := range recs {
		if p, ok := r["path"].(string); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// ---------- 用例 ----------

// TestDoExport_FailureKeepsOldFiles 生成失败（任一格式）不得触碰旧成品：
// 历史 bug——removeOldExports 在生成前调用，任何后续失败（如目录名超长）都
// 会「导出失败 = 旧文件丢失」。
func TestDoExport_FailureKeepsOldFiles(t *testing.T) {
	env := newExportTestEnv(t)
	oldPath := seedOldExport(t, env, 1, "epub", "旧成品内容")

	// 注入：txt 成功、epub 失败（第二个格式失败 = 整批失败）
	env.m.exporterOverride = func(f export.Format) (export.Exporter, error) {
		switch f {
		case "txt":
			return &okExporter{format: f, ext: "txt"}, nil
		case "epub":
			return &failExporter{format: f, ext: "epub"}, nil
		}
		return nil, fmt.Errorf("未知格式")
	}

	_, err := env.m.doExport(2, env.book, []string{"txt", "epub"})
	if err == nil {
		t.Fatal("epub 失败应使整批导出失败")
	}
	if !strings.Contains(err.Error(), "模拟导出失败") {
		t.Fatalf("错误应来自失败导出器: %v", err)
	}

	// 旧文件必须原样保留
	data, rerr := os.ReadFile(oldPath)
	if rerr != nil {
		t.Fatalf("旧成品文件被删除: %v", rerr)
	}
	if string(data) != "旧成品内容" {
		t.Fatalf("旧成品内容被篡改: %q", string(data))
	}
	// 旧记录必须保留
	recs := env.exportRecordPaths(env.book.ID)
	if len(recs) != 1 || recs[0] != oldPath {
		t.Fatalf("旧成品记录应保留 1 条指向 %s，实际 %v", oldPath, recs)
	}
	// 暂存目录已清理
	entries, _ := os.ReadDir(env.outDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".export-tmp-") {
			t.Fatalf("暂存目录未清理: %s", e.Name())
		}
	}
}

// TestDoExport_SuccessReplaces 全部成功后替换旧成品（记录+文件均为新内容）。
func TestDoExport_SuccessReplaces(t *testing.T) {
	env := newExportTestEnv(t)
	oldTxt := seedOldExport(t, env, 1, "txt", "旧 txt")
	oldEpub := seedOldExport(t, env, 1, "epub", "旧 epub")

	env.m.exporterOverride = func(f export.Format) (export.Exporter, error) {
		switch f {
		case "txt":
			return &okExporter{format: f, ext: "txt"}, nil
		case "epub":
			return &okExporter{format: f, ext: "epub"}, nil
		}
		return nil, fmt.Errorf("未知格式")
	}

	_, err := env.m.doExport(2, env.book, []string{"txt", "epub"})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{oldTxt, oldEpub} {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("新成品文件缺失: %v", rerr)
		}
		if !strings.HasPrefix(string(data), "新成品-") {
			t.Fatalf("文件应为新内容: %q", string(data))
		}
	}
	recs := env.exportRecordPaths(env.book.ID)
	if len(recs) != 2 {
		t.Fatalf("应有 2 条新记录，实际 %d: %v", len(recs), recs)
	}
	// 暂存目录已清理
	entries, _ := os.ReadDir(env.outDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".export-tmp-") {
			t.Fatalf("暂存目录未清理: %s", e.Name())
		}
	}
}

// TestDoExport_ConcurrentSameBook 同书并发导出必须串行化：
// 历史风险——两路「删旧→写新」并发互删对方成品 + 记录重复。
func TestDoExport_ConcurrentSameBook(t *testing.T) {
	env := newExportTestEnv(t)
	env.m.exporterOverride = func(f export.Format) (export.Exporter, error) {
		return &okExporter{format: f, ext: "txt"}, nil
	}

	var wg sync.WaitGroup
	const n = 6
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := env.m.doExport(int64(100+i), env.book, []string{"txt"}); err != nil {
				t.Errorf("并发导出 #%d 失败: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// 串行化后：磁盘文件存在且为最新内容；记录恰好 1 条（每次导出替换上一轮记录）
	recs := env.exportRecordPaths(env.book.ID)
	if len(recs) != 1 {
		t.Fatalf("串行化后应恰好 1 条记录，实际 %d: %v", len(recs), recs)
	}
	data, rerr := os.ReadFile(recs[0])
	if rerr != nil || !strings.HasPrefix(string(data), "新成品-") {
		t.Fatalf("最终成品应为新内容: %v %q", rerr, string(data))
	}
}

// TestDoExport_ZeroChaptersKeepsOldFiles 零正文保护失败路径同样不得触碰旧成品。
func TestDoExport_ZeroChaptersKeepsOldFiles(t *testing.T) {
	env := newExportTestEnv(t)
	oldPath := seedOldExport(t, env, 1, "epub", "旧成品内容")
	// 清空章节缓存 → 「没有可导出的章节内容」
	if err := env.st.SaveChapterContent(env.book.ID, 0, "", 2, ""); err != nil {
		t.Fatal(err)
	}

	_, err := env.m.doExport(2, env.book, []string{"epub"})
	if err == nil || !strings.Contains(err.Error(), "没有可导出的章节内容") {
		t.Fatalf("应报零正文错误: %v", err)
	}
	if _, rerr := os.Stat(oldPath); rerr != nil {
		t.Fatal("旧成品文件应保留")
	}
	if len(env.exportRecordPaths(env.book.ID)) != 1 {
		t.Fatal("旧成品记录应保留")
	}
}

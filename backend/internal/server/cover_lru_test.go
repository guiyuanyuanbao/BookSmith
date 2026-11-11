package server

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"booksmith/internal/model"
)

func TestCoverLRU_EvictsColdestOnCountLimit(t *testing.T) {
	var evicted []string
	var mu sync.Mutex
	lru := NewCoverLRU(3, 1<<30, func(key string) {
		mu.Lock()
		evicted = append(evicted, key)
		mu.Unlock()
	})
	lru.Touch("a", 10)
	lru.Touch("b", 10)
	lru.Touch("c", 10)
	// 提升 a 为最新，则 b 应最先被驱逐
	lru.Touch("a", 10)
	lru.Touch("d", 10) // 超限 3 条

	if lru.Len() != 3 {
		t.Fatalf("条目数应保持 3，实际 %d", lru.Len())
	}
	mu.Lock()
	got := append([]string(nil), evicted...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("应驱逐最冷的 b，实际 %v", got)
	}
	// a 是热的（刚提升过），不该被驱逐
	for _, k := range got {
		if k == "a" {
			t.Fatal("热键 a 不应被驱逐")
		}
	}
}

func TestCoverLRU_EvictsOnByteLimit(t *testing.T) {
	var evicted []string
	lru := NewCoverLRU(100, 25, func(key string) { evicted = append(evicted, key) })
	lru.Touch("a", 10)
	lru.Touch("b", 10)
	lru.Touch("c", 10) // 总 30 > 25 → 驱逐最冷 a
	if lru.Len() != 2 {
		t.Fatalf("应剩 2 条，实际 %d", lru.Len())
	}
	if len(evicted) != 1 || evicted[0] != "a" {
		t.Fatalf("应驱逐 a，实际 %v", evicted)
	}
}

func TestCoverLRU_TouchUpdatesSize(t *testing.T) {
	lru := NewCoverLRU(100, 30, nil)
	lru.Touch("a", 10)
	lru.Touch("a", 50) // 大小更新为 50，超 30 → 因只剩 1 条不驱逐（保底 1 条）
	if lru.Len() != 1 {
		t.Fatalf("单条不应被驱逐（保底 1 条），实际 %d", lru.Len())
	}
	lru.Touch("b", 1) // 两条总 51 > 30 → 驱逐 a
	if lru.Len() != 1 {
		t.Fatalf("超限后应保留 1 条，实际 %d", lru.Len())
	}
}

func TestCoverLRU_Remove(t *testing.T) {
	lru := NewCoverLRU(10, 1<<30, nil)
	lru.Touch("a", 10)
	lru.Touch("b", 10)
	lru.Remove("a")
	if lru.Len() != 1 {
		t.Fatalf("Remove 后应剩 1 条，实际 %d", lru.Len())
	}
	lru.Remove("nonexistent") // 不应 panic
}

func TestMemCoverLRU_SingleEntryEviction(t *testing.T) {
	lru := NewCoverLRU(memCoverMax, 0, nil)
	// 写 300 条（超 256）→ 只驱逐最旧，不整体清空
	for i := 0; i < 300; i++ {
		lru.memPut(string(rune('a'+i%26))+string(rune('0'+i/26)), coverCacheEntry{data: []byte("x"), httpType: "image/jpeg"})
	}
	if lru.Len() > 256 {
		t.Fatalf("内存缓存不应超过 256 条，实际 %d", lru.Len())
	}
	if lru.Len() < 250 {
		t.Fatalf("应逐条驱逐而非整体清空，实际剩余 %d", lru.Len())
	}
	// 热键命中提温后仍在
	lru.memPut("hot", coverCacheEntry{data: []byte("h"), httpType: "image/jpeg"})
	for i := 0; i < 300; i++ {
		lru.memPut(string(rune('A'+i%26))+string(rune('0'+i/26)), coverCacheEntry{data: []byte("y"), httpType: "image/jpeg"})
		if _, ok := lru.memGet("hot"); !ok {
			t.Fatalf("热键在第 %d 次写入后被驱逐", i)
		}
	}
}

func TestScanCoversDir_IndexesExistingFiles(t *testing.T) {
	dir := t.TempDir()
	coversDir := filepath.Join(dir, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"aaa.img", "bbb.img", "skip.txt"} {
		if err := os.WriteFile(filepath.Join(coversDir, n), []byte("1234567890"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lru := NewCoverLRU(100, 1<<30, nil)
	scanCoversDir(coversDir, lru)
	if lru.Len() != 2 {
		t.Fatalf("应索引 2 个 .img 文件（忽略 .txt），实际 %d", lru.Len())
	}
}

func TestEvictCovers_DeletesUnreferenced(t *testing.T) {
	app, srv := newCoverTestApp(t)
	app.StartCoverProxy()
	// 造两个封面磁盘缓存
	urlA := "http://example.com/a.jpg"
	urlB := "http://example.com/b.jpg"
	app.coverCacheStore(urlA, []byte("aaa"), "image/jpeg")
	app.coverCacheStore(urlB, []byte("bbb"), "image/jpeg")
	pathA := coverCachePath(app.DataDir, urlA)
	pathB := coverCachePath(app.DataDir, urlB)

	// 书 1 用 urlA，书 2 也用 urlA（共享）→ 删书 1 时 urlA 仍被引用，不能删
	mustBook(t, app, "书一", "作者", "http://s1", urlA, "")
	mustBook(t, app, "书二", "作者", "http://s2", urlA, "")

	srv.evictCovers([]string{urlA})
	if _, err := os.Stat(pathA); err != nil {
		t.Fatal("urlA 仍被书 2 引用，不应删除")
	}

	// 删掉书 2 后，urlA 无引用 → 可删；urlB 一直无引用
	all, _, _ := app.Store.ListBooks("", 10000, 0)
	for _, b := range all {
		_ = app.Store.DeleteBook(b.ID)
	}
	srv.evictCovers([]string{urlA, urlB})
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Fatal("无引用的 urlA 缓存文件应被删除")
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Fatal("无引用的 urlB 缓存文件应被删除")
	}
	if app.CoverDiskLRU.Len() != 0 {
		t.Fatalf("LRU 索引应同步移除，实际剩 %d", app.CoverDiskLRU.Len())
	}
}

func TestRemoveBookExports_DeletesFilesAndEmptyDir(t *testing.T) {
	app, srv := newCoverTestApp(t)
	outDir := t.TempDir()
	// 导出目录结构: {outDir}/{书名(作者)}/{源名}/
	bookDir := filepath.Join(outDir, "测试书(作者)", "源甲")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(bookDir, "测试书(作者).epub")
	if err := os.WriteFile(filePath, []byte("epub"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := mustBook(t, app, "测试书", "作者", "http://s1", "", "")
	if err := app.Store.AddExport(0, b.ID, "测试书(作者).epub", "epub", filePath, 4); err != nil {
		t.Fatal(err)
	}

	srv.removeBookExports([]int64{b.ID})

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatal("导出文件应被删除")
	}
	if _, err := os.Stat(filepath.Join(outDir, "测试书(作者)")); !os.IsNotExist(err) {
		t.Fatal("变空的书目录应被清理")
	}
}

func TestRemoveBookExports_KeepsNonEmptyDir(t *testing.T) {
	app, srv := newCoverTestApp(t)
	outDir := t.TempDir()
	// 两个源目录：删其中一个的导出后，书目录仍有另一个源的文件 → 不删
	dirA := filepath.Join(outDir, "书(作者)", "源甲")
	dirB := filepath.Join(outDir, "书(作者)", "源乙")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fileA := filepath.Join(dirA, "书(作者).epub")
	fileB := filepath.Join(dirB, "书(作者).epub")
	_ = os.WriteFile(fileA, []byte("a"), 0o644)
	_ = os.WriteFile(fileB, []byte("b"), 0o644)

	b := mustBook(t, app, "书", "作者", "http://s1", "", "")
	if err := app.Store.AddExport(0, b.ID, "书(作者).epub", "epub", fileA, 1); err != nil {
		t.Fatal(err)
	}

	srv.removeBookExports([]int64{b.ID})

	if _, err := os.Stat(fileA); !os.IsNotExist(err) {
		t.Fatal("该书导出文件应被删除")
	}
	if _, err := os.Stat(fileB); err != nil {
		t.Fatal("其他源的导出文件不应被删除")
	}
	if entries, err := os.ReadDir(filepath.Join(outDir, "书(作者)")); err != nil || len(entries) != 1 {
		t.Fatalf("书目录应保留（含 1 个源子目录），实际 %v %d", err, len(entries))
	}
}

// 保证 model 包被引用（mustBook 用 model.Book 类型别名场景）
var _ = model.Book{}

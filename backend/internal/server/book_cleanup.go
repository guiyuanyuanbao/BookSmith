// book_cleanup.go 书籍删除时的磁盘清理：导出文件即时删除（不再等下次启动 Sweep）。
package server

import (
	"os"
	"path/filepath"
)

// removeBookExports 删除书籍的导出记录与磁盘文件，并尝试清理因此变空的
// 一级目录 downloads/{书名(作者)}（非递归，防误删其他书的目录）。
// 需在 Store.DeleteBook 之前调用（删除后 exports 记录随外键不可查）。
func (s *Server) removeBookExports(ids []int64) {
	outRoot := ""
	if s.app.TaskMgr != nil {
		outRoot = s.app.TaskMgr.OutDir()
	}
	bookDirs := map[string]bool{}
	srcDirs := map[string]bool{}
	for _, id := range ids {
		paths, err := s.app.Store.DeleteExportsByBook(id)
		if err != nil {
			continue
		}
		for _, p := range paths {
			_ = os.Remove(p)
			// p = {outDir}/{书名(作者)}/{源名}/{书名(作者)}.ext
			srcDir := filepath.Dir(p)
			bookDir := filepath.Dir(srcDir)
			if outRoot == "" || bookDir != outRoot {
				bookDirs[bookDir] = true
				srcDirs[srcDir] = true
			}
		}
	}
	// 先清空源子目录，再清空书目录（从深到浅，书目录清完源目录后可能变空）
	removeExportDirsIfEmpty(srcDirs)
	removeExportDirsIfEmpty(bookDirs)
}

// removeExportDirsIfEmpty 批量删除空目录（仅当目录内无任何条目时）。
func removeExportDirsIfEmpty(dirs map[string]bool) {
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			continue // 目录不存在或不空（其他源的文件还在），保留
		}
		_ = os.Remove(dir)
	}
}

// Package store 提供基于纯 Go SQLite (modernc.org/sqlite) 的持久化仓储。
// 表设计原则：实体完整 JSON 存列（兼容 legado 全部字段），关键查询字段单列索引。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store SQLite 存储实例。
type Store struct {
	db *sql.DB
	// walStop 停止周期性 WAL checkpoint（Close 时回收）
	walStop chan struct{}
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc sqlite 单写多读，限制连接数避免 SQLITE_BUSY
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// 主动 WAL checkpoint：被动阈值（autocheckpoint 1000 页）会被写入期间的
	// 其他查询顶掉（实测 WAL 涨到 17MB 后 10 小时无写入仍不回收）——
	// 启动时 + 每小时 TRUNCATE 模式强制截断 WAL 为 0，控制占盘与崩溃恢复成本
	s.walStop = make(chan struct{})
	go s.walCheckpointLoop()
	return s, nil
}

// WalCheckpoint 强制 TRUNCATE 模式 checkpoint（WAL 截断为 0 字节）。
// 单连接模式下无并发读者，理论上总能成功；失败（如瞬时锁）静默等下轮。
func (s *Store) WalCheckpoint() {
	var one int
	if err := s.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&one, &one, &one); err != nil {
		return
	}
}

// walCheckpointLoop 启动先做一次（回收上次运行遗留的 WAL），此后每小时一次。
func (s *Store) walCheckpointLoop() {
	// 稍等迁移/启动清理完成再首次 checkpoint，避免抢锁
	select {
	case <-time.After(5 * time.Second):
	case <-s.walStop:
		return
	}
	s.WalCheckpoint()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.WalCheckpoint()
		case <-s.walStop:
			return
		}
	}
}

// Close 关闭数据库（停掉 checkpoint 循环 + 关连接）。
func (s *Store) Close() error {
	if s.walStop != nil {
		select { case <-s.walStop: default: close(s.walStop) }
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS book_sources (
			url TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			group_name TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			custom_order INTEGER NOT NULL DEFAULT 0,
			custom INTEGER NOT NULL DEFAULT 0,
			origin TEXT NOT NULL DEFAULT '',
			verified INTEGER NOT NULL DEFAULT 0,
			last_update_time INTEGER NOT NULL DEFAULT 0,
			respond_time INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 0,
			data TEXT NOT NULL,
			imported_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bs_enabled ON book_sources(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_bs_group ON book_sources(group_name)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS book_sources_fts USING fts5(name, group_name, url, content='book_sources', content_rowid='rowid')`,
		`CREATE TRIGGER IF NOT EXISTS bs_ai AFTER INSERT ON book_sources BEGIN
			INSERT INTO book_sources_fts(rowid, name, group_name, url) VALUES (new.rowid, new.name, new.group_name, new.url);
		END`,
		`CREATE TRIGGER IF NOT EXISTS bs_ad AFTER DELETE ON book_sources BEGIN
			INSERT INTO book_sources_fts(book_sources_fts, rowid, name, group_name, url) VALUES ('delete', old.rowid, old.name, old.group_name, old.url);
		END`,
		`CREATE TRIGGER IF NOT EXISTS bs_au AFTER UPDATE ON book_sources BEGIN
			INSERT INTO book_sources_fts(book_sources_fts, rowid, name, group_name, url) VALUES ('delete', old.rowid, old.name, old.group_name, old.url);
			INSERT INTO book_sources_fts(rowid, name, group_name, url) VALUES (new.rowid, new.name, new.group_name, new.url);
		END`,
		`CREATE TABLE IF NOT EXISTS replace_rules (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '',
			pattern TEXT NOT NULL DEFAULT '',
			replacement TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT '',
			is_regex INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			order_num INTEGER NOT NULL DEFAULT 0,
			scope_title INTEGER NOT NULL DEFAULT 0,
			scope_content INTEGER NOT NULL DEFAULT 1,
			exclude_scope TEXT NOT NULL DEFAULT '',
			apply_to TEXT NOT NULL DEFAULT 'all',
			data TEXT NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rr_enabled ON replace_rules(enabled, order_num)`,
		`CREATE TABLE IF NOT EXISTS books (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			book_url TEXT NOT NULL,
			origin TEXT NOT NULL,
			name TEXT NOT NULL,
			author TEXT NOT NULL DEFAULT '',
			dedup_key TEXT NOT NULL,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE(book_url, origin)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_books_dedup ON books(dedup_key)`,
		`CREATE TABLE IF NOT EXISTS chapters (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
			idx INTEGER NOT NULL,
			url TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			is_volume INTEGER NOT NULL DEFAULT 0,
			status INTEGER NOT NULL DEFAULT 0,
			content TEXT NOT NULL DEFAULT '',
			content_len INTEGER NOT NULL DEFAULT 0,
			err_msg TEXT NOT NULL DEFAULT '',
			data TEXT NOT NULL,
			UNIQUE(book_id, idx)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ch_book ON chapters(book_id, idx)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
			status INTEGER NOT NULL DEFAULT 0,
			total_chapters INTEGER NOT NULL DEFAULT 0,
			done_chapters INTEGER NOT NULL DEFAULT 0,
			fail_chapters INTEGER NOT NULL DEFAULT 0,
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_status ON tasks(status)`,
		`CREATE TABLE IF NOT EXISTS exports (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL DEFAULT 0,
			book_id INTEGER NOT NULL DEFAULT 0,
			name TEXT NOT NULL,
			format TEXT NOT NULL,
			size INTEGER NOT NULL DEFAULT 0,
			path TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		)`,
	}
	for _, st := range stmts {
		if _, err := s.db.Exec(st); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(st), err)
		}
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// KV 读写。

func (s *Store) KVGet(key string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM kv WHERE k=?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func (s *Store) KVSet(key, val string) error {
	_, err := s.db.Exec(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, val)
	return err
}

// NowMS 当前毫秒时间戳。
func NowMS() int64 { return time.Now().UnixMilli() }

// jsonColumns 便捷函数：结构体 ↔ JSON。
func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

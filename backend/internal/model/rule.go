package model

import (
	"fmt"
	"strings"
)

// ReplaceRule legado 替换净化规则（兼容其 JSON 字段命名）。
type ReplaceRule struct {
	ID           int64  `json:"id"`                           // 主键
	Name         string `json:"name"`                         // 规则名称
	Group        string `json:"group"`                        // 分组
	Pattern      string `json:"pattern"`                      // 匹配模式（正则或纯文本）
	Replacement  string `json:"replacement"`                  // 替换为（支持 $1 引用；@js: 开头为 JS）
	Scope        string `json:"scope"`                        // 作用域（书名/书源URL 逗号分隔，支持正则）
	IsRegex      bool   `json:"isRegex"`                      // pattern 是否为正则
	Enabled      bool   `json:"isEnabled"`                    // 是否启用
	Order        int    `json:"order"`                        // 排序（小→大）
	ScopeTitle   bool   `json:"scopeTitle"`                   // 作用于标题
	ScopeContent bool   `json:"scopeContent"`                 // 作用于正文（默认 true）
	ExcludeScope string `json:"excludeScope,omitempty"`       // 排除范围（子串匹配）
	TimeoutMS    int64  `json:"timeoutMillisecond,omitempty"` // JS 替换超时毫秒（默认 3000）
	ApplyTo      string `json:"applyTo,omitempty"`            // 作用目标（BookSmith 扩展：title/content/all）
	CreatedAt    int64  `json:"createdAt,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
}

// EffectiveTimeout 生效超时（毫秒）。
func (r *ReplaceRule) EffectiveTimeout() int64 {
	if r.TimeoutMS > 0 {
		return r.TimeoutMS
	}
	return 3000
}

// ApplyTarget 净化应用目标。
type ApplyTarget string

const (
	ApplyAll     ApplyTarget = "all"
	ApplyTitle   ApplyTarget = "title"
	ApplyContent ApplyTarget = "content"
)

// Book 本地书籍（聚合自搜索结果或手动添加）。
type Book struct {
	ID            int64  `json:"id"`         // 本库 ID
	BookURL       string `json:"bookUrl"`    // 详情页 URL（同源内唯一）
	TocURL        string `json:"tocUrl"`     // 目录页 URL（默认同 bookUrl）
	Origin        string `json:"origin"`     // 书源 URL
	OriginName    string `json:"originName"` // 书源名
	Name          string `json:"name"`
	Author        string `json:"author"`
	Kind          string `json:"kind"` // 分类（逗号分隔）
	CoverURL      string `json:"coverUrl"`
	LocalCover    string `json:"localCover,omitempty"` // 手动上传的封面文件名（存于 data/uploads，优先于 CoverURL）
	Intro         string `json:"intro"`
	LatestChapter string `json:"latestChapter"`
	WordCount     string `json:"wordCount"`
	TotalChapters int    `json:"totalChapters"`
	Charset       string `json:"charset,omitempty"` // 指定编码
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

// Chapter 章节信息。
type Chapter struct {
	ID         int64  `json:"id"`
	BookID     int64  `json:"bookId"`
	Index      int    `json:"index"` // 从 0 开始的章节序号
	Title      string `json:"title"`
	URL        string `json:"url"`
	IsVolume   bool   `json:"isVolume"` // 是否卷名
	IsVip      bool   `json:"isVip"`
	UpdateTime string `json:"updateTime,omitempty"`
	Tag        string `json:"tag,omitempty"`
	Content    string `json:"content,omitempty"` // 已净化的正文（缓存后填充）
	Status     int    `json:"status"`            // ChapterStatus
	ErrMsg     string `json:"errMsg,omitempty"`
	ContentLen int    `json:"contentLen,omitempty"`
}

// ChapterStatus 章节下载状态。
type ChapterStatus int

const (
	ChapterPending     ChapterStatus = iota // 未下载
	ChapterDownloading                      // 下载中
	ChapterDone                             // 完成
	ChapterFailed                           // 失败
)

// SearchResult 聚合搜索结果项（跨源去重后）。
type SearchResult struct {
	Name          string         `json:"name"`
	Author        string         `json:"author"`
	Kind          string         `json:"kind,omitempty"`
	CoverURL      string         `json:"coverUrl,omitempty"`
	Intro         string         `json:"intro,omitempty"`
	LatestChapter string         `json:"latestChapter,omitempty"`
	WordCount     string         `json:"wordCount,omitempty"`
	Origins       []SearchOrigin `json:"origins"` // 命中的各源明细
}

// SearchOrigin 单源命中明细。
type SearchOrigin struct {
	SourceURL     string `json:"sourceUrl"`
	SourceName    string `json:"sourceName"`
	BookURL       string `json:"bookUrl"`
	Name          string `json:"name"`
	Author        string `json:"author"`
	Kind          string `json:"kind,omitempty"`
	LatestChapter string `json:"latestChapter,omitempty"`
	CoverURL      string `json:"coverUrl,omitempty"`
	Intro         string `json:"intro,omitempty"`
	WordCount     string `json:"wordCount,omitempty"`
	RespondTime   int64  `json:"respondTime"` // ms
}

// DownloadTask 下载任务。
type DownloadTask struct {
	ID            int64      `json:"id"`
	BookID        int64      `json:"bookId"`
	BookName      string     `json:"bookName"`
	Author        string     `json:"author"`
	Origin        string     `json:"origin"`
	OriginName    string     `json:"originName,omitempty"`
	Status        TaskStatus `json:"status"`
	Formats       []string   `json:"formats"`    // 导出格式：txt/epub/pdf/html/md
	RangeStart    int        `json:"rangeStart"` // 起始章（0 基，含）
	RangeEnd      int        `json:"rangeEnd"`   // 结束章（0 基，含；-1 表示到最后）
	Concurrency   int        `json:"concurrency"`
	TotalChapters int        `json:"totalChapters"`
	DoneChapters  int        `json:"doneChapters"`
	FailChapters  int        `json:"failChapters"`
	ExportPath    string     `json:"exportPath,omitempty"` // 导出文件路径
	ErrMsg        string     `json:"errMsg,omitempty"`
	Phase         string     `json:"phase,omitempty"` // 执行阶段：toc / content / export
	// 自动换源（目录失败时依次尝试候选源；仅搜索页快捷下载注入，显式选源下载不启用）
	CandidateSources []CandidateSource `json:"candidateSources,omitempty"`
	TriedSources     []TriedSource     `json:"triedSources,omitempty"`
	CreatedAt        int64             `json:"createdAt"`
	UpdatedAt        int64             `json:"updatedAt"`
}

// CandidateSource 自动换源候选。
type CandidateSource struct {
	SourceURL  string `json:"sourceUrl"`
	SourceName string `json:"sourceName"`
	BookURL    string `json:"bookUrl"`
}

// TriedSource 换源尝试记录。
type TriedSource struct {
	SourceName string `json:"sourceName"`
	Err        string `json:"err"`
}

// TaskStatus 任务状态。
type TaskStatus int

const (
	TaskPending   TaskStatus = iota // 等待
	TaskRunning                     // 运行中
	TaskPaused                      // 已暂停
	TaskExporting                   // 导出中
	TaskDone                        // 完成
	TaskFailed                      // 失败
	TaskCanceled                    // 已取消
)

// MarshalJSON 序列化为状态名（前端友好）。
func (s TaskStatus) MarshalJSON() ([]byte, error) {
	return []byte(`"` + TaskStatusName(s) + `"`), nil
}

// UnmarshalJSON 支持从状态名或数字解析。
func (s *TaskStatus) UnmarshalJSON(data []byte) error {
	str := strings.Trim(string(data), `"`)
	switch str {
	case "pending":
		*s = TaskPending
	case "running":
		*s = TaskRunning
	case "paused":
		*s = TaskPaused
	case "exporting":
		*s = TaskExporting
	case "done":
		*s = TaskDone
	case "failed":
		*s = TaskFailed
	case "canceled":
		*s = TaskCanceled
	default:
		var n int
		if _, err := fmt.Sscanf(str, "%d", &n); err == nil && n >= 0 && n <= 6 {
			*s = TaskStatus(n)
		} else {
			*s = TaskPending
		}
	}
	return nil
}

// TaskStatusName 任务状态名。
func TaskStatusName(s TaskStatus) string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskRunning:
		return "running"
	case TaskPaused:
		return "paused"
	case TaskExporting:
		return "exporting"
	case TaskDone:
		return "done"
	case TaskFailed:
		return "failed"
	case TaskCanceled:
		return "canceled"
	}
	return "unknown"
}

// SourceRepo 书源仓库条目（运行时从仓库站点动态发现，代码不写死具体 URL）。
type SourceRepo struct {
	Name string `json:"name"` // 显示名，如"全量书源"、"精品书源合集"
	URL  string `json:"url"`  // 书源 JSON 直链
	Kind string `json:"kind"` // bookSource / replaceRule
	Note string `json:"note,omitempty"`
	// 动态发现元数据
	Provider     string `json:"provider,omitempty"`     // 所属站点 key（aoaostar / yiove）
	CollectionID string `json:"collectionId,omitempty"` // yiove 合集 ID
	Total        int    `json:"total,omitempty"`        // 合集/仓库书源数
	UpdatedAt    int64  `json:"updatedAt,omitempty"`    // 仓库条目更新时间（秒）
}

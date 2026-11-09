// Package model 定义 BookSmith 的核心实体，字段与 legado 书源/规则 JSON 保持兼容，
// 使 legado 生态的书源与净化规则可以直接导入运行。
package model

// BookSource legado 书源（兼容其 JSON 字段命名）。
type BookSource struct {
	BookSourceURL     string        `json:"bookSourceUrl"`     // 主键：源站点 URL
	BookSourceName    string        `json:"bookSourceName"`    // 源名称
	BookSourceGroup   string        `json:"bookSourceGroup"`   // 分组
	BookSourceType    int           `json:"bookSourceType"`    // 0文本 1音频 2图片 3文件 4视频
	BookURLPattern    string        `json:"bookUrlPattern"`    // 详情页 URL 正则
	CustomOrder       int           `json:"customOrder"`       // 自定义排序
	Enabled           bool          `json:"enabled"`           // 是否启用
	EnabledExplore    bool          `json:"enabledExplore"`    // 发现是否启用
	EnabledCookieJar  bool          `json:"enabledCookieJar"`  // 是否启用 CookieJar
	ConcurrentRate    string        `json:"concurrentRate"`    // 并发率：N/M（N次/M毫秒）或 M（1次/M毫秒）
	Header            string        `json:"header"`            // 请求头（JSON 或 key:value 行）
	LoginURL          string        `json:"loginUrl"`          // 登录 URL
	LoginUI           string        `json:"loginUi"`           // 登录界面定义
	LoginCheckJs      string        `json:"loginCheckJs"`      // 登录校验 JS
	BookSourceComment string        `json:"bookSourceComment"` // 源注释
	LastUpdateTime    int64         `json:"lastUpdateTime"`    // 最后更新时间戳(ms)
	RespondTime       int64         `json:"respondTime"`       // 响应时间(ms)
	Weight            int           `json:"weight"`            // 权重
	SearchURL         string        `json:"searchUrl"`         // 搜索 URL 规则
	RuleSearch        *RuleSearch   `json:"ruleSearch"`        // 搜索规则
	RuleBookInfo      *RuleBookInfo `json:"ruleBookInfo"`      // 详情规则
	RuleToc           *RuleToc      `json:"ruleToc"`           // 目录规则
	RuleContent       *RuleContent  `json:"ruleContent"`       // 正文规则
	JSLib             string        `json:"jsLib"`             // 公共 JS 库
	VariableComment   string        `json:"variableComment"`
	Variable          string        `json:"variable"` // 源变量 JSON

	// ---- BookSmith 扩展字段（不参与 legado 兼容导出）----
	ImportedAt int64  `json:"importedAt,omitempty"` // 导入时间
	Verified   bool   `json:"verified,omitempty"`   // 校验通过
	VerifyErr  string `json:"verifyErr,omitempty"`  // 校验错误信息
	Custom     bool   `json:"custom,omitempty"`     // 用户自定义导入
	Origin     string `json:"origin,omitempty"`     // 导入来源（仓库 URL / 文件名）
}

// RuleSearch 搜索规则。
type RuleSearch struct {
	CheckKeyWord string `json:"checkKeyWord"`
	BookList     string `json:"bookList"`
	Name         string `json:"name"`
	Author       string `json:"author"`
	Intro        string `json:"intro"`
	Kind         string `json:"kind"`
	LastChapter  string `json:"lastChapter"`
	UpdateTime   string `json:"updateTime"`
	BookURL      string `json:"bookUrl"`
	CoverURL     string `json:"coverUrl"`
	WordCount    string `json:"wordCount"`
}

// RuleBookInfo 详情规则。
type RuleBookInfo struct {
	Init         string `json:"init"`
	Name         string `json:"name"`
	Author       string `json:"author"`
	Intro        string `json:"intro"`
	Kind         string `json:"kind"`
	LastChapter  string `json:"lastChapter"`
	UpdateTime   string `json:"updateTime"`
	CoverURL     string `json:"coverUrl"`
	TocURL       string `json:"tocUrl"`
	WordCount    string `json:"wordCount"`
	CanReName    string `json:"canReName"`
	DownloadURLs string `json:"downloadUrls"`
}

// RuleToc 目录规则。
type RuleToc struct {
	PreUpdateJS string `json:"preUpdateJs"`
	ChapterList string `json:"chapterList"`
	ChapterName string `json:"chapterName"`
	ChapterURL  string `json:"chapterUrl"`
	FormatJS    string `json:"formatJs"`
	IsVolume    string `json:"isVolume"`
	IsVip       string `json:"isVip"`
	IsPay       string `json:"isPay"`
	UpdateTime  string `json:"updateTime"`
	NextTocURL  string `json:"nextTocUrl"`
}

// RuleContent 正文规则。
type RuleContent struct {
	Content        string `json:"content"`
	SubContent     string `json:"subContent"`
	Title          string `json:"title"`
	NextContentURL string `json:"nextContentUrl"`
	WebJS          string `json:"webJs"`
	SourceRegex    string `json:"sourceRegex"`
	ReplaceRegex   string `json:"replaceRegex"`
	ImageStyle     string `json:"imageStyle"`
	PayAction      string `json:"payAction"`
	CallBackJS     string `json:"callBackJs"`
	ContentBatch   string `json:"contentBatch"`
	MaxBatchSize   int    `json:"maxBatchSize"`
}

// Clone 深拷贝书源。
func (s *BookSource) Clone() *BookSource {
	cp := *s
	if s.RuleSearch != nil {
		r := *s.RuleSearch
		cp.RuleSearch = &r
	}
	if s.RuleBookInfo != nil {
		r := *s.RuleBookInfo
		cp.RuleBookInfo = &r
	}
	if s.RuleToc != nil {
		r := *s.RuleToc
		cp.RuleToc = &r
	}
	if s.RuleContent != nil {
		r := *s.RuleContent
		cp.RuleContent = &r
	}
	return &cp
}

// GetKey 实现 BaseSource 语义：限流键即 bookSourceUrl。
func (s *BookSource) GetKey() string { return s.BookSourceURL }

// GetName 源显示名。
func (s *BookSource) GetName() string { return s.BookSourceName }

// GetHeader 解析书源请求头。
func (s *BookSource) GetHeader() string { return s.Header }

// GetConcurrentRate 并发率配置。
func (s *BookSource) GetConcurrentRate() string { return s.ConcurrentRate }

// GetJSLib 公共 JS 库。
func (s *BookSource) GetJSLib() string { return s.JSLib }

// GetSearchURL 搜索 URL 规则。
func (s *BookSource) GetSearchURL() string { return s.SearchURL }

// GetLoginCheckJS 登录校验 JS。
func (s *BookSource) GetLoginCheckJS() string { return s.LoginCheckJs }

// IsTextSource 是否文本类书源（可下载正文）。
func (s *BookSource) IsTextSource() bool { return s.BookSourceType == 0 }

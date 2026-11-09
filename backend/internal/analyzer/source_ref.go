package analyzer

// BookSourceRef 规则引擎所需的书源只读视图。
// 定义为接口以避免 analyzer 依赖 model 包（由 model.BookSource 实现或经适配器包装）。
type BookSourceRef interface {
	GetKey() string            // 限流/缓存键（bookSourceUrl）
	GetName() string           // 显示名
	GetHeader() string         // 请求头定义（JSON 或行式）
	GetConcurrentRate() string // 并发率
	GetJSLib() string          // 公共 JS 库
	GetSearchURL() string      // 搜索 URL 规则
	GetLoginCheckJS() string   // 登录校验 JS
}

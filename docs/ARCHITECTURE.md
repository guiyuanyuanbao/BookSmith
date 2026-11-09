# BookSmith 架构设计

> 目标：以「开源阅读」(legado) 的书源规则引擎为核心，用 **纯 Go 原生实现**（零桥接）构建一个
> B/S 架构的书籍获取与电子书制作系统：聚合搜索 → 净化 → 多格式导出（TXT/EPUB/PDF/HTML/Markdown）。
> 定位与 So Novel 同台竞技，但书源生态直接复用 legado 数千书源与净化规则仓库，维护成本极低。

## 1. 总体架构

```
┌────────────────────────── 浏览器 (Vue 3 + Vite + TS + Pinia + Element Plus) ──────────────────────────┐
│  书源管理 │ 净化规则 │ 聚合搜索 │ 书籍详情/目录预览 │ 下载任务(进度/日志) │ 导出文件 │ 设置            │
└──────────────────────────────────────┬───────────────────────────────────────────────┘
                                       │ REST + SSE
┌──────────────────────────────────────┴───────────────────────────────────────────────┐
│  Go 后端 (net/http 原生路由, go:embed 内嵌前端产物, 单二进制)                                        │
│                                                                                      │
│  server/   API 层：路由、JSON 编解码、SSE 事件总线、静态资源                                        │
│  task/     下载任务管理器：任务队列、工作池、进度/事件广播、断点续传                                       │
│  webbook/  获取编排：聚合搜索(并发+去重)、详情、目录(翻页/去重)、正文(nextContentUrl 翻页)                 │
│  replace/  净化管线：替换净化规则(pattern/replacement/js) + 内容格式化                                │
│  source/   书源管理：导入(URL/文件/文本)、去重、校验、导出、内置仓库实时同步(aoaostar 等)                   │
│  export/   导出器：TXT / EPUB3 / PDF / HTML / Markdown（接口统一，管线复用缓存）                        │
│  store/    SQLite(modernc 纯 Go) 持久化：书源、净化规则、书籍、章节缓存、任务                          │
│  analyzer/ ★规则引擎(legado 兼容)：RuleAnalyzer 词法 + AnalyzeRule 调度 +                            │
│            AnalyzeByJSoup/CSS/XPath/JSon/Regex + AnalyzeUrl + goja JS 运行时                          │
│  httpx/    HTTP 客户端：编码自动检测、UA 池、Cookie、并发限流、重试                                    │
└──────────────────────────────────────────────────────────────────────────────────────┘
              │                                                        │
       书源仓库(实时拉取)                                          目标书源站点
   legado.aoaostar.com/sources/*.json                          HTML/JSON/GBK…
   （书源 3900+、净化规则、XIU2/破冰/关耳等合集）                     规则引擎解析
```

## 2. 关键设计决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 规则引擎 | 按 legado 语义纯 Go 重实现 | 直接运行 legado 生态书源；goja 为纯 Go JS 引擎，无任何桥接 |
| JS 运行时 | dop251/goja | 纯 Go ES5.1+，书源 JS 规则绝大多数可运行；实现 JsExtensions 子集 |
| JSONPath | ohler55/ojg | 纯 Go、支持 jayway 风格（legado JSON 规则语法） |
| HTML 解析 | goquery + cascadia；antchfx/htmlquery | CSS 与 XPath 双支持 |
| 存储 | modernc.org/sqlite（纯 Go，无 CGO） | 单二进制、免驱动安装 |
| HTTP 路由 | Go 1.22+ net/http ServeMux 原生 | 零依赖、优雅、方法+路径模式 |
| 前端内嵌 | go:embed（vite 产物） | 单二进制分发 |
| PDF | go-pdf/fpdf + 系统/自定义 CJK TTF | 纯 Go；字体解析顺序 simhei→msyh→Noto→用户配置 |
| EPUB | 原生实现（archive/zip + 模板） | EPUB 3 规范直控，无依赖 |
| 下载编排 | 章节先缓存(SQLite+磁盘) 后导出 | 断点续传、多格式复用（借鉴 so-novel 架构并强化） |

## 3. 目录结构

```
BookSmith/
├── backend/
│   ├── cmd/booksmith/main.go        # 入口：加载配置、初始化 store、启动 server
│   ├── internal/
│   │   ├── config/                  # 配置（data 目录、监听地址、并发、字体等）
│   │   ├── httpx/                   # HTTP 客户端 + 编码 + 限流 + UA 池
│   │   ├── analyzer/                # ★ legado 兼容规则引擎
│   │   │   ├── rule_analyzer.go     #   规则词法：|| && ## {{}} <> 切分
│   │   │   ├── analyze_rule.go      #   调度：CSS/JSoup/XPath/JSON/Regex/JS
│   │   │   ├── analyze_jsoup.go     #   默认 jsoup 语法 (class.x@tag.a@text)
│   │   │   ├── analyze_css.go       #   @CSS:
│   │   │   ├── analyze_xpath.go     #   @XPath:
│   │   │   ├── analyze_json.go      #   @json: / $.
│   │   │   ├── analyze_regex.go     #   正则
│   │   │   ├── analyze_url.go       #   URL 规则 + options + {{page}}
│   │   │   └── js/                  #   goja 运行时 + JsExtensions(java.ajax 等)
│   │   ├── model/                   # 实体：BookSource/ReplaceRule/Book/Chapter/Task…
│   │   ├── store/                   # SQLite 仓储层 + 迁移
│   │   ├── source/                  # 书源导入/去重/导出/仓库同步
│   │   ├── replace/                 # 净化规则管理 + ContentProcessor
│   │   ├── webbook/                 # 搜索/详情/目录/正文编排
│   │   ├── export/                  # TXT/EPUB/PDF/HTML/MD 导出器
│   │   ├── task/                    # 下载任务管理器 + 事件
│   │   └── server/                  # REST API + SSE + 静态资源
│   ├── go.mod
│   └── web/                         # embed 包（前端构建产物）
├── frontend/                        # Vue 3 + Vite + TS + Element Plus
├── docs/                            # 研究/架构/API 文档
└── scripts/                         # 构建与开发脚本
```

## 4. 核心数据流

### 4.1 聚合搜索
`POST /api/search` → webbook.Search(keyword, sourceIDs)：
- 并发限流（默认同时 8 源），每源超时 30s；
- 每源：AnalyzeUrl(ruleSearch.url, {{key}}) → httpx 请求 → analyzer 解析 bookList → SearchResult；
- **去重**：以 (书名规整化+作者规整化) 为键聚合，同书多源合并为聚合结果并记录各源明细；
- 源级错误不中断整体，聚合进响应的 errors。

### 4.2 下载任务
`POST /api/tasks` → task.Manager.Create(book, source, opts)：
1. 拉取目录（nextTocUrl 翻页、卷分组、**章节去重**：同名同 URL 重复剔除、可倒序）；
2. 工作池并发抓正文（默认 8 并发，源 concurrentRate 限流）；
3. 正文管线：`原始 HTML → ruleContent → nextContentUrl 翻页拼接 → HtmlFormatter
   (实体解码/去标签/分段) → 净化替换规则(scope 匹配) → 不可见字符清理 → 存储`；
4. 进度/日志实时 SSE；失败章节自动重试，任务可暂停/恢复/重跑失败章节；
5. 完成后按勾选格式调用 export：共用章节缓存，产出 `书名(作者).{txt,epub,pdf,…}`。

### 4.3 导出器接口
```go
type Exporter interface { Format() Format; Export(ctx *ExportContext, w io.Writer) error }
```
- **TXT**：元信息头 + 章节 `《卷名》章节名\n正文`，可选 GBK/UTF-8 编码；
- **EPUB 3**：mimetype/container/OPF/nav.xhtml/章节 XHTML/封面图片，zip 存储（mimetype 首位不压缩）；
- **PDF**：fpdf + CJK TTF（simhei→msyh→Noto→自定义），章节分页、标题样式、2 字符缩进、页码页眉；
- **HTML/Markdown**：单文件全量（附录格式）。

### 4.4 净化规则（legado ReplaceRule 兼容）
- 导入源：aoaostar 仓库（乌云净化等）实时同步 + 用户自定义；
- 应用：pattern 为正则（isRegex）或纯文本（默认全文替换）、replacement 支持 `$1` 引用与
  `@js:` JS 替换、scope（书名/书源 URL 正则，validScope）过滤、order 排序、
  可配置仅作用于书名/正文。

## 5. 规则引擎（analyzer）兼容范围

legado 书源规则五类语法的完整移植（语义对照 legado 源码逐条实现，详见 RESEARCH-legado.md）：

1. **默认 JSoup 语法**：`class.x@tag.a@text`、`id.x`、`!` 排除、`children`、索引/负索引、
   `@attr`、`text/html/all/ownText/textNodes`、`,` 并列。
2. **@CSS:** 标准 CSS 选择器 + `@text/@html/@all/@ownText/@href…` 属性获取。
3. **@XPath:** antchfx XPath + `@text/@html/@all`。
4. **@json: / $.**：ojg JSONPath + `@js:` 段 + put/get 变量。
5. **正则** 与 **JS**（`<js></js>`、`{{}}`，goja + JsExtensions：java.ajax*/get/post/connect、
   base64/md5/aes 等、timeFormat、put/get、cookie、cacheFile 等）。
6. **组合**：`||`（多规则或，取首个非空）、`&&`（合并列表）、`##正则##替换#` 内容后处理、
   `{{}}` 模板插值、变量 put/get（`$.` 与 `result` 传递）。
7. **AnalyzeUrl**：URL 内 `{{}}`/`<js>`、POST body、`{"method","charset","headers","webView",…}`
   options、`{{page}}`/searchPage 页码语义（page-1、BigInt 等）。

## 6. API 设计概要（详见 docs/API.md）

```
GET  /api/sources            书源列表(过滤/排序/分页)
POST /api/sources/import     导入 {url|text|file}
POST /api/sources/sync       内置仓库实时同步(aoaostar)
POST /api/sources/export     导出
PATCH /api/sources/{id}      启用/禁用/排序
POST /api/sources/check      校验(可选登录 URL)
GET/POST/DELETE /api/replace*   净化规则 CRUD/导入
POST /api/search             聚合搜索（SSE 进度可选）
GET  /api/books              本地书架
GET  /api/books/{id}/toc     目录预览
POST /api/tasks              创建下载任务 {bookId, formats[], ranges, replaceRuleIds}
GET  /api/tasks/{id}         任务详情(章节状态)
GET  /api/events             SSE 全局事件(任务进度/日志)
GET  /api/exports            导出文件列表
GET  /api/files/{name}       导出文件下载
```

## 7. 测试策略

- **单元测试**：analyzer 每种语法（含组合/转义/JS）、httpx 编码检测、净化管线、
  各导出器（校验 zip 结构/OPF/NAV 合法性、PDF 文本可提取、TXT 编码）。
- **集成测试**：内嵌 fixture HTTP 站点（GBK 编码、广告、目录翻页、正文分页、JSON 接口、
  POST 搜索、JS 规则源），跑通 搜索→详情→目录→正文→导出 全链路。
- **真实书源冒烟**：可选 `-tags=live` 用 aoaostar 真实书源对活站验证（CI 不强依赖网络）。
- **E2E**：构建产物启动后用浏览器自动化验证 WebUI 关键路径。

# So Novel（freeok/so-novel）研究报告

> 分析对象：https://github.com/freeok/so-novel（v1.12.0，Java/Maven 实现）
> 目的：为 BookSmith（Go + Vue）提供下载编排与多格式导出的对标参考。
> 注意：So Novel 是 **Java** 项目（pom.xml），其中 JS 执行通过 javet（V8 桥接）实现——
> BookSmith 明确不使用任何桥接，改用纯 Go 的 goja 原生实现等价能力。

## 1. 项目结构

```
src/main/java/com/pcdd/sonovel/
├── actions/        # CLI/TUI 动作：聚合搜索、批量下载、单源搜索等
├── context/        # OkHttpClient 工厂、HttpClient 上下文
├── core/           # Crawler(核心爬取)、Source(规则加载)、HtmlExtractor、
│                   # ChapterFilter/Formatter/Renderer（正文→HTML 渲染）
├── dsl/            # @js DSL：StepEngine + JavaScriptExecutor(通过 javet 调 V8)
├── handler/        # 后处理：TxtMergeHandler / EpubMergeHandler / PdfMergeHandler /
│                   # HtmlTocHandler / CrawlerPostHandler / SearchResultsHandler
├── model/          # Rule(书源规则)、Chapter、Book、SearchResult、AppConfig
├── parser/         # SearchParser / BookParser / TocParser / ChapterParser
├── utils/          # CrawlUtils(限流/UA/CF检测)、FileUtils、ChineseConverter(hanlp 简繁)
└── web/            # Jetty WebUI + SSE 下载进度
```

依赖库：jsoup(HTML 解析)、okhttp(HTTP)、epub4j(EPUB)、openhtmltopdf-pdfbox(PDF)、
hutool、hanlp(简繁转换)、javet(V8 JS)、picocli、jetty(WebUI)、freemarker(HTML 模板)。

## 2. 书源规则体系（bundle/rules/*.json）

规则为自定义 JSON 格式（非 legado 格式），每个站点一个对象，核心结构：

```json5
{
  "url": "站点首页", "name": "站点名", "comment": "", "language": "zh-CN", "disabled": false,
  "search": {            // 搜索
    "url": "搜索链接（可 @js: 转换，JS 接收 keyword 返回完整 URL）",
    "method": "GET|POST", "data": "{searchkey: %s}",   // %s 会被关键字替换
    "cookies": "{k:v}", "result": "CSS 选择器（每条搜索记录；可 @js: 转换响应体）",
    "bookName": "...", "author": "...", "category": "...", "wordCount": "...",
    "latestChapter": "...", "lastUpdateTime": "...", "status": "...", "nextPage": "..."
  },
  "book":  { "bookName": "...", "author": "...", "intro": "...", "coverUrl": "...",
             "latestChapter": "...", "latestChapterUrl": "...", "lastUpdateTime": "...", "status": "..." },
  "toc":   { "url": "...", "list": "...", "item": "CSS 选择器", "isDesc": false, "nextPage": "..." },
  "chapter": {
    "title": "...", "content": "...",
    "paragraphTagClosed": true|false,  // true: 标签已闭合 <p>..</p>；false: <br> 分段
    "paragraphTag": "<br>+",
    "filterTxt": "正则1|正则2",        // 广告文本过滤
    "filterTag": "div, p, script",     // 要移除的标签
    "nextPage": "正文分页下一页元素", "nextPageInJs": "JS 中的下一页链接", "nextChapterLink": "..."
  },
  "crawl": { "concurrency": 8, "minInterval": 0, "maxInterval": 0,
             "maxAttempts": 3, "retryMinInterval": 1000, "retryMaxInterval": 2000 }
}
```

选择器语法：CSS Selector 为主（jsoup），少量 XPath，`@attr` 取属性（如 `a@href`），
`@js:` 前缀做响应体/URL 转换（JS 运行于 V8，BookSmith 用 goja 原生替代）。

## 3. 下载编排（core/Crawler.java）

1. `TocParser.parseAll(bookUrl)` 拉取目录（支持 `toc.nextPage` 翻页、`isDesc` 倒序）；
   目录为空则中止（多为反爬）。
2. `BookParser.parse(bookUrl)` 拉取详情（书名/作者/简介/封面）。
3. 并发下载正文：`maxConcurrent = min(config.concurrency, len(toc))`（上限 50），
   虚拟线程 + `VirtualThreadLimiter` 限流；每章独立重试（maxAttempts，重试前随机间隔）。
4. 每章落盘缓存：`下载目录/书名(作者)/%0{N}d_章节名.html`（N=章节数位数，零填充保证排序；
   下划线分隔符有兼容用途）。缓存文件是**渲染后的 HTML 片段**（freemarker 模板）。
5. 进度：CLI 进度条；WebUI 通过 SSE 每 50 章或最后一章推送 `{type:"download-progress",index,total}`。
6. 全部完成后 `CrawlerPostHandler` 按 `extName` 调用对应 MergeHandler 合并导出。

请求细节（utils/CrawlUtils.java）：
- 随机 User-Agent + `Referer: 站点host`。
- 随机下载间隔 `min/maxInterval`（重试用 retryMin/retryMax）。
- **不可见字符清理**（中文乱码根源）：`[\p{C}\p{Cf}\p{Co}\p{Zl}\p{Zp}\u200B\uFEFF]` 全部移除。
- Cloudflare 检测：`document.title` ∈ {"Just a moment...", "403 Forbidden",
  "Attention Required", "Checking your browser before accessing"}。
- POST body：JSON 模板 `{key: %s}` → 表单编码。

## 4. 正文格式化（core/ChapterFormatter.java）

- 先 `clearAllAttributes` 去掉所有 HTML 属性（防 XSS/样式污染）。
- `paragraphTagClosed=true`：非 `<p>` 闭合标签整体替换为 `<p>正文</p>`
  （正则 `<(?!p\b)([^>]+)>(.*?)</\1>` → `<p>$2</p>`）。
- `paragraphTagClosed=false`：按 `paragraphTag`（默认 `<br>+`）切分，每行非空包 `<p>`。
- `ChapterFilter`：应用 `filterTxt` 正则（删除匹配行）与 `filterTag`（移除标签）。

## 5. 导出实现（handler/*MergeHandler.java）

统一命名：`{downloadPath}/{书名}({作者}).{txt|epub|pdf}`。

### TXT
- 首部元信息块：`书名：%s\n作者：%s\n简介：%s`（简介为空写"暂无"，去 HTML 标签）。
- 依次按文件名排序追加各章缓存；编码可配（`txtEncoding`，默认 UTF-8）。

### EPUB（epub4j）
- metadata：title、author（ creator ）、description（简介）、language=zh、
  date=now、publisher=so-novel、rights=版权声明字符串。
- 封面：下载 `coverUrl` → `cover.jpg` + 封面页 `cover.html`（失败不中断）。
- 每章一个 XHTML 资源（id 零填充序号），`guide` 指定封面页。

### PDF（openhtmltopdf）
- 字体解析顺序：`fonts/SoNovel.ttf`（自定义）→ 系统字体：
  Windows: `msyh.ttc, simsun.ttc, simhei.ttf`；macOS: `PingFang.ttc` 等；
  Linux: `wqy-zenhei.ttc, NotoSansCJK-Regular.ttc` 等。找不到则报错提示放字体。
- 页面 7.36×9.76 英寸（10.3 寸电纸书比例），body 字体 'SoNovel'，
  `p { font-size:18px; text-indent:2em; line-height:1.6 }`，
  `.chapter { page-break-before: always }`（首章除外）。
- 流程：各章缓存 HTML 包 `<div class="chapter">` 合并 → 渲染 PDF。

## 6. 配置体系

`config.ini`（INI）：`[global] autoUpdate/ghProxy/cfBypass`，
`[download] downloadPath/extName/txtEncoding/preserveChapterCache/enableProgressbar`，
`[source] language/active-rules/sourceId`，
`[proxy] enabled/host/port`，`[performance] thread`。支持 `ghProxy` GitHub 加速前缀。

## 7. WebUI

- Jetty + 静态页（`static/index.html` + js），REST + SSE。
- 端点（web/servlet）：搜索、书源列表、下载（SSE `download-progress`）。
- 交互流：输入书名 → 聚合搜索（多源并发）→ 用户选书 → 选格式 → 下载 → 进度条。

## 8. 值得借鉴 / 应当避免

**借鉴**：
- 章节先缓存后合并的架构（断点续传、多格式复用一次抓取）。
- 零填充序号文件名保证排序；下划线分隔"序号_标题"。
- 每源独立限流参数（并发/间隔/重试）。
- 不可见字符清理（PUA 区字符是中文乱码根源）+ CF 标题检测。
- 随机 UA/Referer、随机间隔，降低封禁风险。
- 导出命名 `书名(作者).扩展名`；TXT 头部元信息；PDF 章节分页 + 2em 缩进排版。
- 简繁转换作为可选项（hanlp）。

**避免**：
- javet（V8 桥接）平台绑定期重、分发困难 → goja 纯 Go。
- 规则格式私有、生态小，站点覆盖有限 → BookSmith 采用 legado 书源生态（数千源）。
- 缓存目录无 DB 元数据，重下载需全量重新判断 → BookSmith 用 SQLite 记录任务/章节状态。
- 硬编码合并（txt 直接 append 文件）→ BookSmith 抽象为统一的 Exporter 接口 + 管线。

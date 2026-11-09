# legado 书籍获取编排层研究报告

> 分析对象：https://github.com/LegadoTeam/legado（app/src/main/java/io/legado/app/）
> 本报告聚焦编排层（WebBook/BookList/BookInfo/BookChapterList/BookContent/限流/Web API），
> 规则引擎语法详见 RESEARCH-legado-rule.md。

## A. 聚合搜索流程

### A.1 单源搜索（WebBook.kt）
- 入口 `WebBook.searchBookAwait`（WebBook.kt:53-114）：
  1. `searchUrl` 为空抛"搜索url不能为空"。
  2. 构造 `AnalyzeUrl(mUrl=searchUrl, key, page, baseUrl=bookSource.bookSourceUrl, source, ruleData=RuleData())`。
     URL 中 `{{page}}`/`<,>` 占位由 AnalyzeUrl 处理：`pagePattern = <(.*?)>`（AnalyzeUrl.kt:818），
     `paramPattern = \s*,\s*(?=\{)`（:817）用于切 URL 选项 JSON；`{{...}}` 内嵌 JS 先执行（:216-243）。
  3. 请求前若书源有 `loginCheckJs`，用响应该给 JS 校验登录态；请求失败时用 `getErrStrResponse` 再试一次 JS。
  4. 交给 `BookList.analyzeBookList(..., isSearch=true)`。

### A.2 搜索结果解析（BookList.kt）
- `analyzeBookList`（BookList.kt:35-151）：
  - body 为空抛"获取网页内容失败"。
  - **详情页捷径**：书源配置 `bookUrlPattern` 且本次 baseUrl 匹配该正则时，直接按详情页解析单本书返回；
    列表为空且无 bookUrlPattern 时也回退详情页解析（:100-108）。
  - **列表规则前缀**：`ruleList` 以 `-` 开头 → 结果倒序并去 `-`；以 `+` 开头仅去 `+`（:89-96）。
  - 逐项 `getSearchItem`（:194-289）：name 经 `BookHelp.formatBookName`，author 经 `formatBookAuthor`；
    name 为空丢弃该项；kind 用 `getStringList(...)?.joinToString(",")`；wordCount 经 `wordCountFormat`；
    intro 经 `HtmlFormatter.formatIntro`；coverUrl 用 `NetworkUtils.getAbsoluteURL(baseUrl, it)`；
    bookUrl 用 `getString(ruleBookUrl, isUrl=true)`，空则取 baseUrl。
  - filter 回调（name, author, kind）返回 false 即跳过。
  - **源内去重**：`LinkedHashSet(bookList)`——SearchBook 的 `equals/hashCode` 只看 `bookUrl`（SearchBook.kt:65-67），
    再按 reverse 标志翻转（:142-147）。
  - 当 `baseUrl == searchBook.bookUrl` 时把整个响应体存入 `searchBook.infoHtml` 复用，省一次详情页请求。

### A.3 聚合搜索（SearchModel.kt）
- **并发度**：`threadCount = AppConfig.threadCount`，默认 32；`flatMapMerge(concurrency)` + `buffer(0)`。
- **单源超时**：`withTimeout(30000L)`（:108），单源 30 秒；单源异常吞掉不中断整体。
- **分页续搜**：`search(searchId, key)`——同 searchId 且仍在跑则忽略；同 searchId 则 `searchPage++`；
  新 searchId 重置 page=1、清结果。
- **暂停/恢复**：`workingState: MutableStateFlow<Boolean>`，flow 发射每个源前 `workingState.first { it }`。
- **进度**：`SearchProgressReporter`（:299-334）按完成源数回报 `onSearchProgress(searched, total)`。
- **结果合并与去重（mergeItems, :153-234）**——跨源去重按"书名+作者"分四桶：
  1. `equalData`：`name == key || author == key`（完全命中关键词）；
  2. `tagsData`：`kind?.contains(key)`；
  3. `containsData`：`name.contains(key) || author.contains(key)`；
  4. `otherData`：其余，仅非精确搜索时保留。
  - 同桶内去重：`pBook.name == nBook.name && pBook.author == nBook.author` 相同则 `pBook.addOrigin(nBook.origin)`
    （origins: LinkedHashSet），不新增条目。
  - 排序：各桶按 `origins.size` 降序（同名书被越多源命中越靠前），拼接 equalData + tagsData + containsData (+otherData)。
  - **精确搜索开关**：开启时请求侧 filter 丢弃 name/author/kind 均不含 key 的结果；合并时丢弃 otherData。
- 精准搜书（换源）：`preciseSearchAwait` filter 为 `fName==name && fAuthor==author`，命中即停。

## B. 目录获取流程（BookChapterList.kt）

### B.1 入口（WebBook.kt:270-367）
- `getChapterListAwait`：先（可选）执行 `ruleToc.preUpdateJs`；`book.bookUrl == book.tocUrl && tocHtml 非空` 时
  直接用缓存 tocHtml 解析，否则请求 `book.tocUrl`。
- 详情页若 `tocUrl` 解析结果等于 baseUrl，把响应体存入 `book.tocHtml`（BookInfo.kt:157-161）——目录与详情同页的源免二次请求。

### B.2 单页解析（BookChapterList.kt:188-307）
- **列表规则前缀**：`chapterList` 以 `-` 开头 → reverse=true 并去 `-`；`+` 仅去符号。
- 每个元素：
  - `title = chapterName`，`url = chapterUrl`，`info = updateTime` 规则文本。
  - **isVolume**：`isVolume` 规则结果 `isTrue()` → `chapter.isVolume=true, tag=info`；否则若 `tocCountWords` 开启，
    用 `wordCountRegex` 从 info 提取字数，剩余进 `tag`。
  - url 为空：卷章节用 `title + index` 占位；普通章节用 baseUrl 代替。
  - title 非空才入列表；`isVip`/`isPay` 规则结果 `isTrue()` 时置位。
- **nextTocUrl 翻页**：`getStringList(nextTocRule, isUrl=true)` 取**下一页链接列表**，过滤等于 redirectUrl 的项：
  - 0 个：单页目录。
  - 1 个：**串行循环**——`while (nextUrl.isNotEmpty() && !nextUrlList.contains(nextUrl))` 逐页请求解析追加；
    `nextUrlList` 防环。
  - N 个：**并发**——`mapAsync(threadCount)` 并发请求全部下页，按完成序追加。
- 防环：`nextUrlList.contains(nextUrl)` 即停。

### B.3 汇总、去重与倒序（:121-159）
1. 列表为空抛 `TocEmptyException`。
2. `if (!reverse) chapterList.reverse()`（本分支语义：规则无 `-` 时先整体翻转一次）。
3. **章节去重**：`LinkedHashSet(chapterList)`——BookChapter 的 `equals/hashCode` 只看 `url`。
   因第 2 步先翻转，重复 URL 时保留采集序中较后出现的对象（规则带 `-` 时保留较前者）。
4. `if (!book.getReverseToc()) list.reverse()`（用户"目录倒序"设置再翻转一次）。
   净效果：index 0 = 采集序第一条（无 `-` 且未设倒序时）。
5. 逐章赋 `index`。
6. **formatJs**：`ruleToc.formatJs` 对每个标题执行 JS，绑定 `index`（1 起）、`chapter`、`title`，返回值覆盖标题。
7. `updateBookTocInfo`：更新最新章节、总章数等。

## C. 正文获取流程（BookContent.kt + WebBook.kt）

### C.1 入口 `WebBook.getContentAwait`（WebBook.kt:395-509）
1. **缓存优先**：`BookHelp.contentSaveToken` 取版本令牌，`version > 0` 时缓存命中即返回。
2. 卷章节（`isVolume && url.startsWith(title)`）直接返回空串（一级目录正文不解析规则）。
3. 正文规则 `content` 为空 → 返回 `chapter.url`。
4. `chapter.url == book.bookUrl && tocHtml 非空` → 用 tocHtml 解析；否则请求 `chapter.getAbsoluteURL()`
   （该函数保留 URL 后 `,` 选项段）。
5. 请求传 `jsStr=contentRule.webJs, sourceRegex=contentRule.sourceRegex`（WebView 渲染场景）。
6. 保存：`BookHelp.saveContent(..., saveToken)`；令牌过期抛"正文缓存已更新，请重试"。

### C.2 解析与翻页（BookContent.analyzeContent）
- **nextContentUrl 翻页**（每页私有函数返回 `(本页正文, 下一页URL列表)`）：
  - 1 个下一页：串行 `while` 拼接，`nextUrlList` 防环；**若绝对化后的 nextUrl 等于绝对化后的下一章 URL 则终止**
    （防止把下一章当第二页）；页间请求同样限流。
  - N 个下一页：`mapAsync(threadCount)` 并发取页，不再取下下页。
  - 各页以 `"\n"` join 成全文。
- **正文净化（内建处理）**：
  1. `getString(contentRule.content, unescape=false)` 取原始正文；
  2. `adaptSpecialStyle` 开启时把 `<usehtml>` 片段替换为占位符保护；
  3. `HtmlFormatter.formatKeepImg(content, rUrl)`；
  4. 含 `&` 时 `StringEscapeUtils.unescapeHtml4`；
  5. 回填 usehtml 占位符。
- **书源级 replaceRegex 全文替换**：按行 split → 每行 trim → `analyzeRule.getString(replaceRegex, contentStr)`
  （规则里可用 `{{...}}` JS）；在线文本源再逐行加全角缩进 `"　　"`。
- **正文规则里的 title**：正文中取标题覆盖 `bookChapter.title`；若标题是 `<img ...>`，
  第一捕获组为标题、第二组存 `imgUrl`（题图）。
- 空正文且非卷 → `ContentEmptyException`。
- **批量正文 contentBatch**：仅 JS；书源经 `java.cacheContent(url, content)` 回存；整批占一个并发配额；
  返回未回存章节列表走普通单章兜底。

### C.3 HtmlFormatter（utils/HtmlFormatter.kt:31-86）
- `formatText`：`\r\n|\r` 归一 → `&nbsp;`→空格 → 特殊空格去除 → 不可打印字符去除 →
  换行标签（br/p/div 等 wrapHtmlRegex）→`\n` → 注释删除 → 其它标签删除 → 段首替换为 `\n` + 缩进 → 尾部清理。
  `format()` 缩进 `"　　"`（两个全角空格），`formatIntro()` 不缩进。
- `formatKeepImg`：在 format 基础上用 formatImagePattern 重构 `<img src="...">`，src 绝对化，
  支持 `src,{...}` URL 选项参数保留。`getAbsoluteURL`：绝对 URL/data URL 原样返回，`javascript:` 返回空串。
- 段落缩进的阅读期应用在 ContentProcessor.processBookParagraphs：逐行 trim（含全角空格），
  其余行加缩进前缀。

### C.4 BookHelp 正文存储（help/book/BookHelp.kt）
- 目录：`{files}/book_cache/{folderName}/`；`folderName = name.replace(fileNameRegex /*[\\/:*?"<>|.]*/, "")`
  前 `min(9, len)` 字符 + `md5Encode16(bookUrl)`。
- 章节文件名：`%05d-{index}-{md5Encode16(title)}.{suffix}`，默认后缀 `nb`。
- 图片：`book_cache/{folder}/images/{md5Encode16(src)}.{suffix}`，按 src 加 Mutex 去重下载。
- 写入经 `ContentSaveFence` 版本化令牌防止旧响应覆盖新缓存。
- 书名/作者清洗正则：`nameRegex = \s+作\s*者.*|\s+\S+\s+著`；`authorRegex = ^\s*作\s*者[:：\s]+|\s+著`；
  `fileNameRegex = [\\/:*?"<>|.]`。

## D. concurrentRate 限流精确语义（help/ConcurrentRateLimiter.kt）

- 字段：`BaseSource.concurrentRate: String?`，限流键 = `source.getKey()`（BookSource 即 bookSourceUrl）。
- **格式**：
  - `"N/M"`：N 次访问 / M 毫秒窗口；N、M 必须 >0。
  - 纯正整数 M：等价 `"1/M"`。
  - 空、`"0"`：**不限流**。
- **判定算法**（fetchStart）：每 key 一条 `ConcurrentRecord{time(窗口起点), accessLimit, interval, frequency}`：
  - 新记录：直接放行（frequency 初始 1）。
  - 已有记录 synchronized 内判定：
    - `nowTime >= record.time + interval` → 窗口过期：`time=now, frequency=1`，放行；
    - `frequency < accessLimit` → `frequency++`，放行；
    - 否则 → 抛 `ConcurrentException(waitTime = time + interval - now)`。
  - **frequency 在请求发起时自增（而非完成时），是固定窗口速率限制，不是在途数限制**。
- **排队方式**：循环 `fetchStart()`，捕获异常后 `delay(waitTime)` 再试（睡眠排队）。
- **挂接点**：每个 `AnalyzeUrl` 实例化时建 limiter，`getStrResponseAwait`/`getResponseAwait` 经 `withLimit{}` 包裹；
  `skipRateLimit=true` 可绕过。目录/正文翻页的每次请求都经此。

## E. Web API 端点规格（legado 官方 webService，B/S 版参考）

所有 JSON 响应统一包装 `ReturnData`：`{"isSuccess": bool, "errorMsg": string, "data": any}`。

POST（body 为 `postData` 字段中的 JSON）：
| 路径 | 说明 |
|---|---|
| /saveBookSource /saveBookSources /deleteBookSources | 书源 CRUD |
| /startBookSourceCheck /stopBookSourceCheck | 批量校验启停 |
| /saveBook /deleteBook /saveBookProgress | 书籍 CRUD/进度 |
| /addLocalBook | multipart 导入本地书 |
| /saveReplaceRule /deleteReplaceRule /testReplaceRule | 净化规则；testReplaceRule body={rule,text} 返回替换结果 |
| /saveRssSource(s) /deleteRssSources | 订阅源 |

GET：
| 路径 | 说明 |
|---|---|
| /getBookSource?url= | 单个书源 |
| /getBookSources | 全部书源 |
| /getBookSourceCheckStates | 校验状态 map |
| /getBookshelf | 全部书，按 bookshelfSort 排序（0 最近阅读/1 更新时间/2 书名/3 手动） |
| /getChapterList?url= | 章节数组；库中无目录时自动触发 refreshToc |
| /refreshToc?url= | 重建目录（网络书 tocUrl 空先 getBookInfoAwait） |
| /getBookContent?url=&index= | 正文：先查缓存；无则同步抓取再经 ContentProcessor 净化返回；章节行不存在轮询等待最多 30 秒 |
| /cover?path= /image?url=&path=&width= | 封面/正文图片 |
| /getReplaceRules | 全部替换规则 |

WebSocket（端口+1）：`/searchBook` 消息 `{"key": "关键词"}`，每次命中推送全量合并结果，完成后 NormalClosure+“Search finish"；
`/bookSourceDebug` 消息 `{"key": 搜索词, "tag": 书源URL}`。

## F. 实体字段表

### F.1 Book（表 `books`，PK bookUrl，唯一索引 (name, author)）
bookUrl(PK) / tocUrl / origin(书源URL) / originName / name / author / kind / coverUrl / intro /
charset / type(位标志) / group / latestChapterTitle / latestChapterTime / lastCheckTime / lastCheckCount /
totalChapterNum / durChapterTitle/Index/Pos / wordCount / canUpdate / order / originOrder /
variable(书籍变量 JSON) / readConfig(reverseToc 等) / syncTime；
非持久化：infoHtml, tocHtml, downloadUrls。
辅助：`getRealAuthor()` 去掉"著"后缀；`getFolderName()` 见 C.4。

### F.2 BookChapter（表 `chapters`，PK (url, bookUrl)，唯一索引 (bookUrl, index)）
url / title / isVolume / baseUrl / bookUrl / index / isVip / isPay / resourceUrl / tag /
wordCount / start / end / startFragmentId / endFragmentId / variable / imgUrl。
方法要点：`equals/hashCode` 只看 url——目录与搜索去重的基础；
`getFileName(suffix) = %05d-index-md5Encode16(title).suffix`；
`getAbsoluteURL()` 保留 `,` 后 URL 选项。

### F.3 SearchBook（表 `searchBooks`，PK bookUrl）
bookUrl / origin / originName / type / name / author / kind / coverUrl / intro / wordCount /
latestChapterTitle / tocUrl / time / variable / originOrder / chapterWordCountText /
chapterWordCount(默认-1) / respondTime(默认-1)；非持久化 `origins: LinkedHashSet<String>`、infoHtml/tocHtml。
`compareTo` 按 originOrder 降序。`toBook()` 复制公共字段并带 html 缓存。

### F.4 BookSource（表 `book_sources`，PK bookSourceUrl）
`bookSourceUrl, bookSourceName, bookSourceGroup, bookSourceType(0文本/1音频/2图片/3文件/4视频),
bookUrlPattern(详情页正则), customOrder, enabled, enabledExplore, jsLib, enabledCookieJar,
concurrentRate, header, loginUrl, loginUi, loginCheckJs, coverDecodeJs, bookSourceComment,
variableComment, lastUpdateTime, respondTime(默认180000), weight, exploreUrl, exploreScreen,
ruleExplore, searchUrl, ruleSearch, ruleBookInfo, ruleToc, ruleContent, ruleReview, mainJs,
eventListener, customButton`。

规则子对象（data/entities/rule/）：
- **SearchRule/BookListRule**：checkKeyWord, bookList, name, author, intro, kind, lastChapter,
  updateTime, bookUrl, coverUrl, wordCount。
- **TocRule**：preUpdateJs, chapterList, chapterName, formatJs, isVolume, isVip, isPay, updateTime, nextTocUrl。
- **BookInfoRule**：init, name, author, intro, kind, lastChapter, updateTime, coverUrl, tocUrl,
  wordCount, canReName, downloadUrls。
- **ContentRule**：content, subContent, title, nextContentUrl, webJs, sourceRegex, replaceRegex,
  imageStyle, imageDecode, payAction, callBackJs, contentBatch, maxBatchSize。

## 重实现关键提示
1. 三层并发互相独立：聚合搜索并发源数（默认 32）；目录/正文多页并发（threadCount）；
   单书源请求速率（concurrentRate 固定窗口，发起时计数、delay 排队）。
2. 去重三处：单源搜索结果按 bookUrl；聚合搜索按 (name, author) 四桶合并 origins；
   目录按 chapter.url。
3. 缓存竞态用版本化令牌：获取前取 token，保存时校验版本——Go 版可用 per-(bookUrl,index) 原子版本号。
4. 正文缓存文件名不含书名/URL 明文：`%05d-index-md5title16.nb`。
5. Web 端 getBookContent 是同步阻塞模型——B/S 版可复用"入库驱动"：saveBook 后异步抓目录，HTTP 轮询读缓存。

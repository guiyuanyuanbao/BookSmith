# legado 规则引擎核心规格报告（面向 Go 重实现）

**分析基线**：LegadoTeam/legado（`app/src/main/java/io/legado/app/`）。

**关键差异说明**：
- 本仓库**没有独立的 `AnalyzeByCSS.kt`**。CSS 规则内嵌在 `AnalyzeByJSoup.kt` 中
  （`@CSS:` 前缀由 `AnalyzeRule` 映射为 `Mode.Default`，再由 AnalyzeByJSoup 的 `SourceRule.isCss` 分支处理）。
- JSON 规则文件名为 `model/analyzeRule/AnalyzeByJSonPath.kt`。
- 此 fork 中官方旧版的 `searchKey`/`searchPage-1` 字面替换、`$=` JSON 变量语法、`utf8ToGbk` 已不存在
  （已全局 grep 确认）。但社区书源大量使用这些旧语法，BookSmith 需实现**兼容垫片**。
- `ContentProcessor.kt` 位于 `help/book/ContentProcessor.kt`。
- 协同类：`RuleAnalyzer.kt`（词法器）、`SourceHtmlParser.kt`（HTML 预解析）、`CustomUrl.kt`、
  `AnalyzeUrlNetworkOptions.kt`。

## A. 规则调度逻辑（AnalyzeRule.kt）

### A.1 总体架构

`AnalyzeRule` 实现 `JsExtensions`，是所有内容规则的调度中心。构造参数：`ruleData`（变量载体）、
`source`（BaseSource）、`preUpdateJs`、`isFromBookInfo`。

模式枚举：`enum class Mode { XPath, Json, Default, Js, Regex, WebJs }`。

入口方法：
| 方法 | 用途 |
|---|---|
| `getString(ruleStr, mContent=null, isUrl=false, unescape=true)` | 单值提取；链式应用多条 SourceRule |
| `getStringList(ruleStr, mContent, isUrl)` | 列表提取；String 结果按 `\n` 切分 |
| `getElement(ruleStr)` | 取单个元素/对象 |
| `getElements(ruleStr)` | 取元素列表（书单/目录列表） |
| `setContent(content, baseUrl)` | 设定解析对象；`isJSON = content 是 JSON` |

`setContent` 清空三个子解析器缓存；规则作用于链中途新结果时每次新建子解析器。

### A.2 词法切分：splitSourceRule

`splitSourceRule(ruleStr, allInOne=false)` 只做一件事：**把规则切成「普通段」与「JS 段」交替的序列**。
JS 段由两个全局正则识别：

```kotlin
JS_PATTERN    = "<js>([\\w\\W]*?)</js>|@js:([\\w\\W]*)"   // 忽略大小写
WebJS_PATTERN = "@webjs:([\\w\\W]{5,})"                    // 忽略大小写
```

- 扫描 `JS_PATTERN` 的每个匹配：匹配点之前的文本（trim 后非空）作为 `SourceRule(text, mMode)`，
  匹配体作为 `SourceRule(js, Mode.Js)`。
- `WebJS_PATTERN` 同理产出 `Mode.WebJs` 段（WebView 中执行的 JS）。
- `allInOne=true`（供 `getElements`/`getElement`）且规则以 `:` 开头时，整段为 `Mode.Regex`，跳过首字符：
  正则列表规则写作 `:正则1&&正则2`。`isRegex` 状态位粘滞。

**注意**：`||`、`&&`、`%%` 不在这一层切分——下推到子解析器，由 `RuleAnalyzer.splitRule` 完成。
`@CSS:`/`@@`/`@XPath:`/`@Json:` 前缀识别在 `SourceRule.init` 中完成。

### A.3 单条 SourceRule 的类型识别与预切分（SourceRule.init）

优先级顺序：

```kotlin
rule = when {
    mode == Mode.Js || mode == Mode.Regex -> ruleStr             // 已定型
    ruleStr.startsWith("@CSS:", true)  -> { mode = Mode.Default; ruleStr }   // 保留全串
    ruleStr.startsWith("@@")           -> { mode = Mode.Default; ruleStr.substring(2) }
    ruleStr.startsWith("@XPath:", true)-> { mode = Mode.XPath;   ruleStr.substring(7) }
    ruleStr.startsWith("@Json:", true) -> { mode = Mode.Json;   ruleStr.substring(6) }
    isJSON || ruleStr.startsWith("$.") || ruleStr.startsWith("$[") -> { mode = Mode.Json; ruleStr }
    ruleStr.startsWith("/")            -> { mode = Mode.XPath;   ruleStr }
    else -> ruleStr                                              // 默认 jsoup
}
```

要点：
- **`isJSON`（内容本身是 JSON）时任何规则都强制走 Json 模式**。
- `@CSS:` 串不剥离前缀，原样传给 AnalyzeByJSoup（后者再剥）。
- `/` 开头即 XPath。

随后三步预切分：

1. **分离 `@put:`**（splitPutRule）：正则 `putPattern = "@put:(\{[^}]+?\})"`。命中则从规则中删除该段，
   JSON 解析为 `Map<String,String>` 放入 `putMap`（严格 GSON 失败降级宽松并告警）。

2. **分离 `@get:` 与 `{{}}`**（evalPattern = `"@get:\{[^}]+?\}|\{\{[\w\W]*?\}\}"`）。
   **若命中且 mode 不是 Js/Regex，且（匹配点在开头 或 匹配点之前的文本不含 `##`），则 mode 改为 Regex**。
   规则切成段序列：`@get:{key}` → type=-2；`{{js}}` → type=-1（参数为花括号内 JS）；其余文本 → splitRegex。

3. **分离 `$N` 反向引用**（splitRegex）：先按 `##` 切开，只对 `##` 前的第一段扫 `regexPattern = "\$\d{1,2}"`。
   若命中且 mode 非 Js/Regex → **mode 改为 Regex**；`$N` → type=N、其余文本 → type=0（字面量）。
   尾部 `##xxx##yyy…` 作为字面量拼接保留，最终 makeUpRule 末尾再按 `##` 切出替换参数。

### A.4 规则链执行（getString）

```kotlin
for (sourceRule in ruleList) {
    putRule(sourceRule.putMap)          // 1. 先执行 @put（值为规则串，递归 getString）
    sourceRule.makeUpRule(result)       // 2. 重建规则文本：替换 $N/@get/{}/##
    result ?: continue
    result = when (sourceRule.mode) {
        Mode.WebJs  -> getWebJsResult(rule, result)
        Mode.Js     -> evalJS(rule, result)
        Mode.Json   -> getAnalyzeByJSonPath(result).getString(rule)
        Mode.XPath  -> getAnalyzeByXPath(result).getString(rule)
        Mode.Default-> if (isUrl) getAnalyzeByJSoup(result).getString0(rule)
                       else         getAnalyzeByJSoup(result).getString(rule)
        else -> rule                        // Mode.Regex：返回重建后的字面规则文本
    }
    if (result != null && sourceRule.replaceRegex.isNotEmpty())
        result = replaceRegex(result.toString(), sourceRule)   // 3. ## 正则替换
}
```

- 每段规则以上一段输出为输入（链式传递）。
- `Mode.Regex` 段不执行提取，返回 `makeUpRule` 重建后的字面文本——即 `$N`/`{{}}`/`@get:` 的纯替换模板。
  `$N` 取值来源：传入 result 是 `List<String?>` 且 size>N 时取 `result[N]`。
- result 是 JS NativeObject 时只取第一条规则：Js 求值 / JsonPath / 组合规则返回文本 / 否则按键直取。

### A.5 makeUpRule 与 ## 正则替换

`makeUpRule(result)`：**逆序**遍历 (ruleType, ruleParam)，insert(0) 拼接：

| ruleType | 含义 | 行为 |
|---|---|---|
| > 0 | `$N` 引用 | result 是 List 且 size>N → 取 `result[N]`；否则用字面参数 |
| == -1 | `{{js}}` | 内容是规则（`@`/`$.`/`$[`/`//` 开头）→ 递归 getString；否则 evalJS。JS 返回 Double 整除时 `%.0f` |
| == -2 | `@get:{key}` | `get(key)` |
| == 0 | 字面量 | 原样拼接 |

末尾按 `##` 切分：`rule=[0].trim()`、`replaceRegex=[1]`、`replacement=[2]`、**存在第 4 段则 replaceFirst=true**。

`replaceRegex(result, rule)` 语义：

```kotlin
// ##match##replace###（4 段，replaceFirst=true）：
if (matcher.find()) return matcher.group(0).replaceFirst(regex, replacement)
else return ""                     // 找不到匹配 → 整体清空
// ##match##replace（3 段）：
return result.replace(regex, replacement)      // replaceAll
// 正则编译失败时：return result.replace(replaceRegex, replacement)  // 退化为字面替换
```

- replacement 中 `$1`…`$9` 原生捕获组引用。
- 正则缓存上限 16 条。
- **`##` 切分是朴素 split，无引号/括号保护**——正则中不能含未转义的 `##`。

### A.6 组合符 `||` / `&&` / `%%`（子解析器层，RuleAnalyzer.splitRule）

1. **首个匹配的分隔符决定整个规则的组合类型**。`consumeToAny("&&","||","%%")` 找最早出现者 →
   `elementsType`；后续全部按该串切分。`a||b&&c` 切分为 `["a", "b&&c"]`——**组合符不可混用**。
2. **平衡组保护**：分隔符若落在 `[...]`/`(...)` 内则不切。JSON/JS 用 `chompCodeBalanced`
   （处理 `\` 转义、引号、`[]` 深度+另一对括号深度）；jsoup/xpath 用 `chompRuleBalanced`
   （引号内不处理转义、引号外 `\` 转义下一字符）。
3. 语义：
   - **`&&`**：所有子规则结果**拼接**。
   - **`||`**：**短路**——首个结果非空的子规则胜出。
   - **`%%`**：**按索引交叉合并**（zip）：`for i in results[0].indices → for temp in results → temp[i]`。

各模式支持：JSoup getElements/getStringList 三种；XPath 同三种；JSON getString：`&&`/`||`；
JSON getStringList/getList：三种。

### A.7 变量 put/get

- `put(key, value)`：优先级 `chapter → book → ruleData → source`。
- `get(key)`：localBindings → `bookName`→book.name、`title`→chapter.title 特判 →
  chapter → book → ruleData → source，全空返回 `""`。落盘：BaseSource 写 `v_{key}_{name}`。

### A.8 evalJS 绑定环境

AnalyzeRule.evalJS 绑定：`java=this`、`cookie=CookieStore`、`cache=CacheManager`、`source`、`book`、
`result`（上一步结果）、`baseUrl`、`chapter`、`title`、`src=content`、`nextChapterUrl`、`chapters`、
`fromBookInfo`、`paraIndex/paraData/page`。作用域链：优先源级共享作用域（jsLib 预载），脚本缓存 16 条。

### A.9 取值模式最终处理

- 结果含 `&` 且 unescape=true → `StringEscapeUtils.unescapeHtml4`。
- `isUrl=true`：`getAbsoluteURL(redirectUrl, str)`；结果空白 → 返回 `baseUrl`。
  `getStringList(isUrl=true)` 逐条转绝对 URL 并去重。

## B. AnalyzeByJSoup 完整语法

### B.1 文档解析

- 入参已是 Element → 直用；JXNode → 元素化或 parseSourceHtml。
- 字符串以 `<?xml` 开头 → XML 解析器。
- 其余 parseSourceHtml：HTML 命名空间标签一律标记自闭合（复刻 `<a/>` 闭合行为）。

### B.2 规则结构与 @ 组切分

`@CSS:` 前缀（忽略大小写）→ isCss=true，规则体 substring(5).trim()。

`getStringList(ruleStr)`：
- 规则空 → `element.data()`（script/style 内嵌文本）。
- CSS 模式：每个子规则 `lastIndexOf('@')` 切成「CSS 选择器 + 末尾取值符」，
  `element.select(selector)` 后 `getResultLast(elements, attr)`。**CSS 规则必须以 `@xxx` 结尾**。
- 默认模式：`getResultList`：trim 后按 `@` 切分；**除最后一段外的每段都是元素选择器**，链式过滤；
  最后一段交给 `getResultLast` 取内容。

`getElements(ruleStr)`：同样拆 `&&/||/%%`；每个子规则再按 `@` 切分，**所有段（含最后一段）都按元素选择器
处理**——列表规则末段不能写 `@text`。单段时 `getElementsSingle`。`%%` 按索引逐位交叉。

### B.3 getResultLast 取值模式

| 末段 | 行为 |
|---|---|
| `text` | `element.text()`（含后代、空白归一），空文本跳过 |
| `textNodes` | **直接文本子节点**逐个 trim，按 `\n` 连接（保留段落结构） |
| `ownText` | `element.ownText()`（仅直接文本） |
| `html` | 先移除 script/style 再 outerHtml() |
| `all` | outerHtml()（保留 script/style） |
| 其他 | `element.attr(名)`；空白或重复的属性值跳过 |

CSS 模式下末段同样支持上述关键字与属性名。

### B.4 ElementsSingle：前置选择器与索引语法

先 `findIndexSet(rule)` 解析尾部索引集，再按 beforeRule 取元素，最后按索引过滤。

**beforeRule**（按 `.` 切分取首词）：

| 首词 | 行为 |
|---|---|
| `children` | `temp.children()` |
| `class` | `temp.getElementsByClass(第2段)` |
| `tag` | `temp.getElementsByTag(第2段)` |
| `id` | 全树递归找 id |
| `text` | `getElementsContainingOwnText(第2段)`（own text 包含匹配） |
| 其他 | `temp.select(beforeRule)`（整段作为 jsoup/CSS 选择器） |
| 空 | `temp.children()`（裸索引 `[0]` 直接作用于子元素） |

**索引语法两种**：

1. **传统写法**：`tag.div.-1:10:2` / `tag.div!0:3`。尾部 `:` 分隔索引串，前置界定符 `.`=选取、`!`=排除。
   索引可为负（`-1` 最后一个）。
2. **`[]` 写法**：`beforeRule[idx,idx]` 或 `[idx,...]`。内容为逗号分隔的单索引或区间
   `start:end` / `start:end:step`；`[!...]` 排除。区间端点与步长均可为负，start=0 可省略、end=-1 可省略；
   **`[-1:0]` 使列表反向**。

索引语义：
- 单索引：正数须 < len；负数 `it + len`（要求 len >= -it）；越界忽略。
- 区间：省略 start→0、省略 end→len-1；负值先 +len；start/end 同侧越界整个区间无效；
  单侧越界钳制；start==end 或步长≥len → 仅取 start；步长 ≤0 归一为 stepX+len 或 1
  （**负步长不改变方向，方向由 end>start 决定**）。
- 过滤：`'!'` → 结果集按索引倒序 deselect；`'.'` → 按索引集新建 Elements。
- 结果顺序：LinkedHashSet 逆向写入还原原顺序。

### B.5 组合符

与 A.6 相同：`&&` 拼接、`||` 短路、`%%` 交叉。

## C. AnalyzeByJSonPath（JSON 规则）

### C.1 进入条件

规则以 `$.` 或 `$[` 开头、或内容本身是 JSON（isJSON）、或显式 `@Json:` 前缀 → Mode.Json。

### C.2 语法

Jayway JsonPath：`$.a.b`、`$..a`、`$.list[*]`、`$[?(@.x==1)]`、`$.a[0:2]` 等。

### C.3 组合与内嵌

- `getString`：`RuleAnalyzer(rule, code=true)`（代码平衡组，正确处理规则中含 `}` 的 JSON）拆 `&&`/`||`。
  单条规则时先执行 **`{$.xxx}` 内嵌替换**：用平衡组拉出整个 `{$.…}` 递归求值；
  **替换失败（求值为空）则该 `{$.` 字面跳过**。若无任何成功内嵌 → `ctx.read(rule)`：
  List 结果 `joinToString("\n")`，否则 toString。
- `getStringList`：同上但拆分含 `%%`；结果每元素 toString。
- `getList`（getElements 用）：`ctx.read<List<Any>>`；`&&` 拼接 / `||` 短路 / `%%` 交叉。
- `getObject`：直接 `ctx.read(rule)`。
- **本 fork 无 `$=` 语法**；变量读写在 AnalyzeRule 层（`@put:{}` / `@get:{}` / `{{}}`）。

## D. AnalyzeUrl 详解

### D.1 初始化流水线 initUrl() 三步

1. **analyzeJs**：对 ruleUrl 扫 `JS_PATTERN`。JS 段求值 `evalJS(js, result)`；JS 之间的普通文本段若有内容，
   则 `text.replace("@result", result)`——**`@result` 占位符替换为上一个 JS 的结果**。
2. **replaceKeyPageJs**：
   - `{{` 与 `}}` → `innerRule("{{","}}")` 逐个求值 JS（未命中平衡组时按字面跳过）。
     JS 环境绑定 `page`、`key`、`book`、`source`、`baseUrl` 等。Double 整除格式化 `%.0f`。
   - **`<a,b,c,…>` 页码数组**（`pagePattern = "<(.*?)>"`）：仅当 page != null。`group(1).split(",")`，
     `page < pages.size` 时取 `pages[page-1]`（**1-based**），否则取最后一项。
     惯用法 `<index,{{page}}>`：page=1 → "index"，page=N → "N"。
   - 关键字注入靠 `{{key}}`（JS 绑定）；query 阶段做「已编码检测」，中文关键字 RFC3986 编码。
3. **analyzeUrl**：`paramPattern = "\s*,\s*(?=\{)"` 切出首个 `,{` 之前的 URL 主体；
   `getAbsoluteURL(baseUrl, url)`；剩余 JSON 解析为 `UrlOption`（严格解析失败降级宽松并告警）。

### D.2 UrlOption 全字段

| 字段 | 语义 |
|---|---|
| method | `POST`/`HEAD`/其他一律 GET |
| charset | 编码；`"escape"` → JS escape 编码（%xx/%uXXXX） |
| headers | Map 或 JSON 字符串，覆盖源请求头；键 `proxy` 抽出作代理 |
| body | 请求体；JSON 字符串会反序列化再序列化（规范化） |
| retry | 重试次数 |
| type | 类型标记；非空时返回 **hex 编码的 bytes**（二进制如 zip）；也用于 multipart |
| webView | `null/false/"false"` → 不用，其余 → 用 |
| webJs | WebView 中执行的取值 JS |
| timeout | 读超时 ms |
| followRedirects | false + 3xx 且 webView → 返回原始响应 |
| dnsIp（别名 resolveIp） | 逗号分隔 IP 绑定 DNS |
| js | **URL 参数解析完成后执行**，返回值覆写 `url` |
| bodyJs | 响应返回后执行，返回值作为新 body |
| serverID / webViewDelayTime | 服务器 id / WebView 延迟 |

### D.3 请求构造

- **POST**：body 非 JSON/XML 且无 Content-Type → `analyzeFields` 表单编码（k=v&…）；
  body 是 JSON → JSON post；有 Content-Type → 原样 body。
- **GET/HEAD**：URL `?` 后 query 重新编码（按 charset；已编码原样）。
- **data: URI**：`urlNoQuery` 以 `data:` 开头 → base64 解码直接返回。
- **Cookie**：`CookieStore.getCookie(domain)` 合并到 Cookie 头（按二级域名）；
  `enabledCookieJar` 时附加 cookieJarHeader。
- **登录头跨域保护**：token/Authorization 类登录头只发给与书源同二级域的请求。

### D.4 并发率（ConcurrentRateLimiter）

- `"N/M"`（N次/M毫秒）或纯整数 M（=1次/M毫秒）。空/"0" 不限。
- 每 key 一条 `{time, accessLimit, interval, frequency}`：
  - 新记录直接放行；窗口过期 → 重置；frequency < accessLimit → 自增放行；
  - 否则 `ConcurrentException(waitTime = time + interval - now)`，调用方 delay 后重试。
- **frequency 在请求发起时自增：固定窗口速率限制**。
- JS 侧 `source.putConcurrent(value)` 动态更新。

### D.5 JS 绑定（AnalyzeUrl.evalJS）

`java=this`、`baseUrl`、`cookie`、`cache`、`page`、`key`、`speakText`、`speakSpeed`、`book`、`source`、
`result`、`extraParams`（键值直接注入）。put/get：extraParams → bookName → title → chapter/ruleData 变量。

## E. JsExtensions JS API 清单（java.xxx）

### 网络（最常用 ★）

| 方法 | 说明 |
|---|---|
| ★ `ajax(url[, callTimeout])` | body 字符串（失败返回堆栈串）；url 为 List 取首个 |
| ★ `ajaxAll(urlList[, skipRateLimit])` | `Array<StrResponse>` 并发 |
| ★ `connect(urlStr[, headerJson[, callTimeout]])` | `StrResponse`（url、body、header） |
| ★ `get(urlStr, headers[, timeout])` | 不跟随重定向（可拿 Location） |
| ★ `post(urlStr, body, headers[, timeout])` | POST |
| `head(urlStr, headers[, timeout])` | HEAD |
| `webView(html, url, js[, cacheFirst])` | WebView 渲染后 JS 取值 |
| `webViewGetSource / webViewGetOverrideUrl` | 抓资源/跳转 URL |
| `startBrowser / startBrowserAwait` | 人工过防爬 |
| `getVerificationCode(imageUrl)` | 验证码 |

### 变量与缓存（★ 每日必用）

| 方法 | 说明 |
|---|---|
| ★ `put(key,value)` / `get(key)` | 变量层级存储 |
| ★ `cookie.getCookie(url)` / `getKey` / `setCookie` / `replaceCookie` / `removeCookie` | Cookie |
| ★ `cache.put(k,v[,saveTime])` / `cache.get(k[,onlyDisk])` 等 | 缓存（内存+磁盘） |
| `cacheFile(urlStr[, saveTime])` | 下载文本文件并缓存 |
| `getCookie(tag[,key])` | 读某源/某键 cookie |

### 编解码与摘要

| 方法 | 说明 |
|---|---|
| ★ `base64Encode(str[, flags])`（默认 NO_WRAP） | |
| ★ `base64Decode(str[, charset])` / `base64DecodeToByteArray` | |
| ★ `md5Encode(str)` / ★ `md5Encode16(str)`（16 位中段） | |
| `digestHex(data, algorithm)` / `digestBase64Str` | SHA 等 |
| `HMacHex / HMacBase64` | |
| ★ `createSymmetricCrypto(transformation, key[, iv])` | AES 等：`.encrypt/.decrypt/.encryptBase64/.encryptHex/.decryptStr` |
| `encodeURI(str[, enc])` | URLEncoder |
| `strToBytes/bytesToStr(str[, charset])` | |
| `hexEncodeToString/hexDecodeToString/hexDecodeToByteArray` | |
| ★ `timeFormat(time[, format])`（默认 yyyy-MM-dd HH:mm:ss） | |
| ★ `htmlFormat(str[, redirectUrl])` | 正文 HTML 格式化（保留图片） |
| ★ `t2s(s)` / `s2t(s)` | 简繁转换 |
| `queryTTF/replaceFont` | 字体反爬 |
| `toNumChapter(s)` | 中文数字章节 → 阿拉伯 |
| `randomUUID()` | |

### 文件与其他

`getFile/readFile/readTxtFile/deleteFile/unzipFile/getTxtInFolder/getZipStringContent/downloadFile`、
`toast/log/logType`、`singleFlight/lock/tick`（源级并发原语）、`cacheContent`（批量正文回存）、
`refreshBookInfo/refreshBookToc/refreshContent`。

**书源最高频 Top 20**：`java.ajax`、`java.get`、`java.post`、`java.connect`、`java.put`、`java.get`（变量）、
`java.base64Encode/Decode`、`java.md5Encode16`、`java.md5Encode`、`java.timeFormat`、`java.encodeURI`、
`java.t2s`、`java.htmlFormat`、`java.webView`、`java.ajaxAll`、`java.digestHex`、
`java.createSymmetricCrypto(...)`、`cookie.getCookie(...)`、`cache.put/get(...)`、`java.getString`。

**兼容垫片（官方旧版 API，社区老书源在用，BookSmith 应补齐）**：
`searchKey`/`searchPage-1` 字面替换、`$=` JSON 变量、`java.utf8ToGbk`、`java.checkConcurrentRate(url)`。

## F. BookSource 实体完整字段表

主实体（GSON 序列化名=属性名）：`bookSourceUrl`(主键) / `bookSourceName` / `bookSourceGroup`(逗号分隔) /
`bookSourceType`(0文本/1音频/2图片/3文件/4视频) / `bookUrlPattern`(详情页正则) / `customOrder` /
`enabled` / `enabledExplore` / `jsLib` / `enabledCookieJar` / `concurrentRate` / `header` /
`loginUrl` / `loginUi` / `loginCheckJs` / `coverDecodeJs` / `bookSourceComment` / `variableComment` /
`lastUpdateTime` / `respondTime`(默认180000) / `weight` / `exploreUrl` / `exploreScreen` /
`ruleExplore` / `ruleSearch` / `ruleBookInfo` / `ruleToc` / `ruleContent` / `ruleReview` / `mainJs` /
`eventListener` / `customButton`。

派生：`getCheckKeyword`（ruleSearch.checkKeyWord 不含 http/::/++/-- 时作校验关键字）、`isJsSource()`、
`supportContentBatch()`（maxBatchSize ∈ [1,50]）。

BaseSource 行为：`getHeaderMap`（header 支持 `<js>`；自动补 UA；合并登录头）；
`getLoginInfo/putLoginInfo`（AES 加密存）；`getLoginHeader/putLoginHeader`；`setVariable/getVariable`；
`put/get`（`v_{key}_{k}`）；`putConcurrent`；`evalJS`。

规则子对象：
- **SearchRule/ExploreRule**（共用 BookListRule）：checkKeyWord, bookList, name, author, intro, kind,
  lastChapter, updateTime, bookUrl, coverUrl, wordCount。
- **BookInfoRule**：init, name, author, intro, kind, lastChapter, updateTime, coverUrl, tocUrl,
  wordCount, canReName, downloadUrls。
- **TocRule**：preUpdateJs, chapterList, chapterName, formatJs, isVolume, isVip, isPay, updateTime, nextTocUrl。
- **ContentRule**：content, subContent, title, nextContentUrl, webJs, sourceRegex, replaceRegex,
  imageStyle, imageDecode, payAction, callBackJs, contentBatch, maxBatchSize。

## G. ReplaceRule 与 ContentProcessor

### G.1 ReplaceRule 实体

| 字段 | 类型 | 默认 | 含义 |
|---|---|---|---|
| id | Long | 时间戳 | 主键 |
| name | String | "" | 名称 |
| group | String? | null | 分组 |
| pattern | String | "" | 匹配内容（正则或纯文本） |
| replacement | String | "" | 替换为；**以 `@js:` 开头时执行 JS** |
| scope | String? | null | 作用范围（书名/书源 URL，子串匹配） |
| scopeTitle | Boolean | false | 作用于标题 |
| scopeSource | Boolean | false | 作用于源 |
| scopeContent | Boolean | true | 作用于正文 |
| excludeScope | String? | null | 排除范围 |
| isEnabled | Boolean | true | 启用 |
| isRegex | Boolean | true | 是否正则 |
| timeoutMillisecond | Long | 3000 | 超时 |
| order（列名 sortOrder） | Int | MIN_VALUE | 排序 |

`isValid()`：pattern 非空、正则可编译、末尾不得有未转义的 `|`。
导入格式兼容：非标准 JSON 时读 `$.regex/replaceSummary/replacement/isRegex/useTo/enable/serialNumber`。

### G.2 scope 匹配（SQL）

```sql
WHERE isEnabled = 1 AND scopeContent = 1
  AND (scope LIKE '%'||:name||'%' OR scope LIKE '%'||:origin||'%' OR scope IS NULL OR scope = '')
  AND (excludeScope IS NULL OR (excludeScope NOT LIKE '%'||:name||'%' AND excludeScope NOT LIKE '%'||:origin||'%'))
  ORDER BY sortOrder
```

scope/excludeScope 是**子串包含**匹配（书名或书源 URL 任一命中），scope 空视为全局。

### G.3 应用逻辑（ContentProcessor）

- **正文替换主循环**（getContent）：
  1. 先按行 trim。
  2. 依 sortOrder 顺序逐条应用；pattern 空 → 跳过。
  3. `isRegex=true` → `replace(name, regex, replacement, timeout, chapter, replaceBook)`：
     - **`replacement` 以 `@js:` 开头时**：对每个正则匹配执行一次 JS，绑定 `result=matcher.group()`、
       `chapter`、`book`、`java=RegexJsExtensions`，返回值经 quoteReplacement 后 appendReplacement。
     - 否则原生 `appendReplacement`（`$1` 组引用可用）。
     - 超时看门狗：超 timeoutMillisecond → 规则被禁用。
  4. `isRegex=false` → 纯文本替换。
- **标题替换**：`getDisplayTitle(titleRules, …)` 用同一逻辑处理标题（scopeTitle 规则）。
- **去重复标题**：正文首行匹配 `书名*标题` 模式则删除。
- 其余步骤：重新分段（可选）→ 简繁转换 → `<usehtml>` 占位保护 → 替换净化 → 占位还原 →
  显示标题 → `\u00A0`→空格 → processBookParagraphs（每行 trim 含全角空格，首行不缩进，其余加缩进）。

## H. 从 ruleContent 到展示的内容处理链

1. `getString(contentRule.content, **unescape=false**)`——正文规则刻意先不做 HTML 实体解码。
2. `<usehtml>` 段占位保护。
3. **HtmlFormatter.formatKeepImg**：
   - `(&nbsp;)+`→空格、`&ensp;|&emsp;`→空格、`&thinsp;|&zwnj;|&zwj;|U+2009-200D`→删除；
   - 块级标签 `</?(div|p|br|hr|h\d|article|dd|dl)…>`→`\n`；HTML 注释删除；非 img 标签全部删除；
   - 空白/换行压缩为 `\n + 段落缩进"　　"`；首尾空白清理；
   - `<img>` 重写：src 优先级 = src 中含 `,{…}` 参数 > `data-src/data-original/data-srcset` > `src` >
     任意 `data-*`；URL 转绝对并拼接参数。
4. 之后若含 `&` → unescapeHtml4。**实体解码时机 = HtmlFormatter 之后**。
5. 正文翻页 nextContentUrl：单条循环（防环 + 下一章 URL 终止判断）/ 多条并发；页内容 `\n` 连接。
6. subContent 副文规则（歌词/弹幕）。
7. **源级 replaceRegex**：按行 trim 后 `analyzeRule.getString(contentRule.replaceRegex, contentStr)`——
   **作为规则串执行**（可 `##…##…`、可 JS），文本类源结果再补全角缩进。
8. `contentRule.title` 覆写章节名（imgRegex 特例：标题尾部 URL 提为章节封面 imgUrl）。
9. 非卷章节内容为空 → ContentEmptyException。
10. 展示前净化（ContentProcessor.getContent）：去重复标题 → 重新分段（可选）→ 简繁（可选）→
    替换净化规则 → 加显示标题 → nbsp 处理 → 段落重排+缩进。

## 关键重实现要点清单

1. 类型识别顺序（A.3）与 `isJSON` 强制 Json 模式必须逐字复刻。
2. `||`/`&&`/`%%` 由首个出现的分隔符定全局组合类型；分隔符在平衡组 `[...]`/`(...)` 内失效。
3. `##` 三段/四段语义（replaceAll / 首匹配截取后 replaceFirst / 失配清空）；replacement 支持 `$N`；
   `##` 无引号保护。
4. `$N` 模板替换基于前一步结果是 List 时按下标取值；`{{}}` 内可嵌规则（`@`/`$.`/`$[`/`//` 开头）。
5. jsoup 语法：`class/tag/id/text/children` 前缀 + 其他一律 `select()`；`@` 链式；
   末段 text/textNodes/ownText/html/all/attr；两种索引语法与负索引/区间/`%%`。
6. 正文规则 `unescape=false` → HtmlFormatter（实体清洗+块标签转行+img 重写）→ unescapeHtml4；
   展示层再过 ContentProcessor。
7. AnalyzeUrl：`@result` 占位、`{{js}}` 内嵌、`<a,b,c>` 页码数组（1-based、越界取末项）、
   `,{options}` JSON、`js`/`bodyJs` 双钩子、并发率 `N/M`。
8. JS 绑定名固定：`java/cookie/cache/source/book/result/baseUrl/chapter/title/src/page/key`。
9. 兼容垫片：`searchKey`/`searchPage-1` 字面替换、`$=`、`utf8ToGbk`、`checkConcurrentRate`。

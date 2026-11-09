# 📚 BookSmith · 书籍工坊

> 以「开源阅读」(legado) 规则引擎为核心的书籍获取与电子书制作系统。
> **纯 Go 原生实现（零桥接）** + Vue 3 前端，单二进制分发。

## 项目定位

与 [So Novel](https://github.com/freeok/so-novel) 同台竞技的书籍获取工具，但核心优势在于：

| 能力 | So Novel | BookSmith |
|---|---|---|
| 书源生态 | 私有规则格式，30+ 站点 | **兼容「阅读」APP 全部书源（数千个，实时可取）** |
| 规则引擎 | 自研 CSS/JS 规则 | 完整移植 legado 规则语法（CSS/JSoup/XPath/JSONPath/正则/JS） |
| 净化能力 | 每源 filterTxt | **兼容 legado 替换净化规则生态（正则/纯文本/JS 三模式）** |
| 导出格式 | TXT/EPUB/PDF | TXT/EPUB/PDF/HTML/Markdown |
| 技术栈 | Java + V8 桥接 | **纯 Go（goja 原生 JS 引擎，无任何桥接）** |
| 界面 | TUI/WebUI | Vue 3 B/S 架构，SSE 实时进度 |

## 核心特性

- **规则引擎**（`internal/analyzer`）：逐条对齐 legado `AnalyzeRule` 语义——
  默认 JSoup 语法（`class.x@tag.a@text`、索引/负索引/排除）、`@CSS:`、`@XPath:`、
  `@json:`/`$.`、`##正则##替换#`、`&&/||/%%` 组合、`@put/@get` 变量、`{{js}}` 内嵌、
  `@result` 占位、`<a,b,c>` 页码数组、`URL,{options}` 请求选项。
- **JS 规则运行时**（`internal/jsenv`）：goja（纯 Go ES 引擎）实现 `JsExtensions`
  常用子集：`java.ajax/connect/get/post`、`base64/md5/AES`、`cookie/cache`、
  `timeFormat/t2s/s2t/toNumChapter` 等；`source/book/chapter` 对象字段名与 legado 一致。
- **获取编排**（`internal/webbook`）：聚合搜索（并发 + 四桶合并去重）、详情（`init`
  规则换根）、目录（`nextTocUrl` 串行/并发翻页、URL 去重、倒序）、正文
  （`nextContentUrl` 翻页防环、`HtmlFormatter.formatKeepImg` 对齐、源级 `replaceRegex`）。
- **净化管线**（`internal/replace`）：legado `ContentProcessor` 语义——scope 子串匹配、
  排除范围、标题/正文分离、`@js:` 逐匹配替换、超时保护；正则优先 RE2、
  **自动回退 regexp2（支持环视断言，兼容乌云净化等复杂规则）**。
- **书源管理**（`internal/source`）：URL/文本/文件导入，`bookSourceUrl` 主键去重，
  内置仓库（aoaostar 全量/精选书源与乌云净化）**一键实时同步**。
- **下载任务**（`internal/task`）：工作池并发、章节级重试、断点续传（SQLite 状态）、
  暂停/恢复/取消/失败重跑、SSE 实时进度。
- **多格式导出**（`internal/export`）：TXT（元信息头/全角缩进/可选 GBK）、
  **EPUB 3**（原生 zip + OPF/NAV/NCX，含卷分组）、**PDF**（fpdf + simhei 等系统中文字体，
  章节分页/页眉页码/2em 缩进）、HTML/Markdown。
- **B/S 服务**（`internal/server`）：net/http 原生路由、go:embed 单二进制内嵌前端、
  REST + SSE。

## 快速开始

```bash
# 构建前端（首次）
cd frontend && npm install && npm run build   # 产物输出至 backend/web/dist

# 构建后端（单二进制）
cd ../backend && go build -o booksmith.exe ./cmd/booksmith

# 启动
./booksmith.exe --addr :8484 --data ./data
# 打开 http://localhost:8484
```

1. 「书源管理」→「内置仓库同步」→ 同步 **XIU2精品书源**（或全量书源）；
2. 「净化规则」→「导入规则」→ 填入乌云净化 URL（`https://legado.aoaostar.com/sources/3f067eb2.json`）；
3. 「聚合搜索」输入书名 → 结果流式返回（可随时取消）→ 点击来源标签下载；
4. 「下载任务」异步流水线（获取目录 → 并发正文 → 导出），支持暂停/恢复/重跑失败章节；
5. 「书源管理」可对单个书源「调试」（逐步骤 trace 定位规则失效）与批量「校验」剔除死源。

### 开发模式

```bash
# 前端热更新（代理 /api 至 :8484）
cd frontend && npm run dev

# 模拟书站（端到端测试）
cd backend && go run ./cmd/fixture --addr :18501
```

### Docker

镜像由 CI 自动构建并发布到 GHCR（push 到 main 或打 `v*` 标签时触发）：

```bash
docker pull ghcr.io/guiyuanyuanbao/booksmith:latest

docker run -d --name booksmith \
  -p 18500:18500 \
  -v booksmith-data:/data \
  ghcr.io/guiyuanyuanbao/booksmith:latest
# 打开 http://localhost:18500
```

本地构建：

```bash
docker build -t booksmith .
docker run -d -p 18500:18500 -v booksmith-data:/data booksmith
```

### 公网部署与访问鉴权

设置 `BOOKSMITH_API_KEY` 环境变量即开启访问保护：打开页面会要求输入密钥，全部 API 需要携带密钥；**不设置则完全关闭鉴权**（本地使用无需任何配置）。

docker compose（推荐，见仓库 `docker-compose.yml`）：

```yaml
services:
  booksmith:
    image: ghcr.io/guiyuanyuanbao/booksmith:latest
    ports: ["18500:18500"]
    environment:
      - BOOKSMITH_API_KEY=change-me-to-a-long-random-key
    volumes:
      - booksmith-data:/data
```

或直接 `docker run -e BOOKSMITH_API_KEY=your-key ...`。也可用启动参数 `--api-key your-key`。

技术细节：密钥经 `X-API-Key` 头（或 `?key=` 参数、Bearer token）传递，常量时间比较防时序攻击；SPA 路由与静态资源放行、登录端点 `/api/auth/login`、状态端点 `/api/auth/status` 免鉴权。

### CI

`.github/workflows/ci.yml`：push/PR 时运行 Go 测试与前端类型检查；通过后构建多架构镜像（amd64/arm64）发布到 GHCR，标签规则：分支名 / `v*` 语义化版本 / `latest`（默认分支）。

## 测试

```bash
cd backend
go test ./internal/...       # 10 个包：单测 + fixture 书站全链路集成测试
go vet ./...
```

覆盖：规则引擎全部语法、HTTP 编码检测、净化三模式（含 JS/超时）、
书源导入去重、fixture 书站（GBK/POST/JSON 源、目录翻页、正文分页、去重）全流程、
导出器结构校验（EPUB zip/XML 合法性、PDF 字体嵌入）、任务暂停恢复取消。

## 目录结构

```
BookSmith/
├── backend/
│   ├── cmd/booksmith/          # 主程序入口
│   ├── cmd/fixture/            # 模拟书站（开发/测试）
│   ├── internal/
│   │   ├── analyzer/           # ★ legado 兼容规则引擎（+ goja JS）
│   │   ├── httpx/              # HTTP 客户端（编码/限流/重试）
│   │   ├── jsenv/              # goja 运行时 + JsExtensions
│   │   ├── content/            # 正文格式化（HtmlFormatter 对齐）
│   │   ├── replace/            # 净化替换管线
│   │   ├── source/             # 书源/规则导入去重与仓库同步
│   │   ├── webbook/            # 搜索/详情/目录/正文编排
│   │   ├── export/             # TXT/EPUB/PDF/HTML/MD 导出器
│   │   ├── task/               # 下载任务管理器 + 事件总线
│   │   ├── store/              # SQLite（modernc 纯 Go）仓储
│   │   ├── server/             # REST API + SSE
│   │   ├── model/ varstore/ util/ testutil/
│   └── web/embed.go            # go:embed 前端产物
├── frontend/                   # Vue 3 + Vite + TS + Pinia + Element Plus
└── docs/                       # 研究报告与架构/API 文档
```

## 文档

- [架构设计](docs/ARCHITECTURE.md)
- [legado 规则引擎规格](docs/RESEARCH-legado-rule.md) / [编排层规格](docs/RESEARCH-legado-flow.md) / [So Novel 研究](docs/RESEARCH-so-novel.md)
- [API 参考](docs/API.md)

## 合规说明

本项目仅用于学习研究，书籍版权归原站与作者所有，请支持正版。

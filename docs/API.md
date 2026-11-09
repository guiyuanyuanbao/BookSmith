# BookSmith HTTP API

所有响应统一为 `{"isSuccess": bool, "errorMsg"?: string, "data": any}`。
下载任务状态以字符串名传输（`pending/running/paused/exporting/done/failed/canceled`）。

## 系统

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 健康检查（版本/时间） |
| GET | `/api/events` | SSE 事件流：`task.progress` / `task.paused` / `task.done` / `task.error` |

## 书源

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/sources?keyword=&enabled=-1&limit=50&offset=0` | 分页列表（FTS 关键词过滤） |
| POST | `/api/sources/import` | 导入 `{url}` 或 `{text}`（支持 legado:// 协议提取） |
| POST | `/api/sources/sync` | 实时同步仓库 `{url}`（内置仓库见 `/api/sources/repos`） |
| GET | `/api/sources/repos` | 内置仓库清单（aoaostar 全量/精选书源等） |
| GET | `/api/sources/export?enabledOnly=1` | 导出书源 JSON（附件下载） |
| PATCH | `/api/sources/{url}` | 启用/禁用 `{enabled: bool}` |
| DELETE | `/api/sources/{url}` | 删除书源 |

## 净化规则

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/replace?enabledOnly=1` | 规则列表（order 升序） |
| POST | `/api/replace/import` | 导入 `{url}` 或 `{text}`（兼容 legado 与 legacy 字段名） |
| POST | `/api/replace` | 新建/更新规则（`ReplaceRule` JSON） |
| DELETE | `/api/replace/{id}` | 删除规则 |

## 搜索（异步任务）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/search` | 启动聚合搜索 `{key, sourceUrls?}`，立即返回 `{searchId}`（并发 96、单源超时 10s；同名同作者聚合 origins 且按源 URL 去重） |
| GET | `/api/search/{id}` | 搜索快照：`{running, searched, total, elapsedMs, items[], errors[]}`（结果流式累积） |
| DELETE | `/api/search/{id}` | 取消搜索 |
| GET | `/api/cover?url=` | 封面代理（绕过防盗链，内存缓存） |

## 书源校验与调试

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/sources/verify` | 批量校验 `{urls[]}`：试搜索并记录 respondTime/verified |
| GET | `/api/sources/debug?url=&key=` | 书源调试：逐步骤 trace（搜索→字段→详情→目录→正文抽样） |

## 书架

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/books?keyword=&limit=&offset=` | 书架列表 |
| GET | `/api/books/{id}` | 书籍详情 |
| DELETE | `/api/books/{id}` | 移除书籍（级联章节/任务） |
| GET | `/api/books/{id}/toc?content=1` | 目录（content=1 附正文） |
| GET | `/api/books/{id}/stats` | 书籍统计：章节缓存（done/failed/pending）+ 活动任务摘要 |
| POST | `/api/books/{id}/download` | 书架发起下载：`{formats[], rangeStart?, rangeEnd?, concurrency?}`；复用章节缓存断点；同书同源已有活动任务时返回 `{taskId, existing: true}` |
| GET | `/api/books/{id}/chapters/{index}` | 单章（含净化后正文） |

## 下载任务

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/tasks` | 创建任务：`{bookUrl, bookName?, author?, tocUrl?, sourceUrl, formats[], rangeStart, rangeEnd(=-1 末尾), concurrency}` |
| GET | `/api/tasks?limit=50` | 任务列表 |
| GET | `/api/tasks/{id}` | 任务详情 |
| POST | `/api/tasks/{id}/pause` | 暂停 |
| POST | `/api/tasks/{id}/resume` | 恢复（续传未完成章节） |
| POST | `/api/tasks/{id}/cancel` | 取消 |
| POST | `/api/tasks/{id}/retry` | 重跑失败章节 |
| GET | `/api/tasks/{id}/failures` | 失败章节明细（含错误信息） |

任务创建为**异步流水线**：返回后依次经历 `toc`（获取目录）→ `content`（并发正文）→ `export` 三阶段，详情拉取失败自动回退搜索提供的信息。
**同书同源去重**：该书已有活动任务（running/pending/exporting）时直接返回 `{taskId, existing: true}`，不重复创建。

## 导出文件

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/exports?limit=100` | 导出记录列表 |
| GET | `/api/files/{name}` | 文件下载（按扩展名设置 Content-Type；basename 防目录穿越） |

## 前端静态资源

非 `/api` 路径全部由 go:embed 内嵌的 Vue SPA 承载（history 路由回退 index.html）。

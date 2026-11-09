// API 客户端：统一封装 axios 与后端交互。
import axios from 'axios'

export interface ApiResult<T = any> {
  isSuccess: boolean
  errorMsg?: string
  data: T
}

const http = axios.create({ timeout: 180_000 })

http.interceptors.response.use(
  (resp) => resp.data,
  (err) => {
    const msg = err?.response?.data?.errorMsg || err.message || '请求失败'
    return Promise.reject(new Error(msg))
  },
)

export async function apiGet<T = any>(url: string, params?: any): Promise<T> {
  const r = (await http.get(url, { params })) as ApiResult<T>
  if (!r.isSuccess) throw new Error(r.errorMsg)
  return r.data
}

export async function apiPost<T = any>(url: string, body?: any): Promise<T> {
  const r = (await http.post(url, body)) as ApiResult<T>
  if (!r.isSuccess) throw new Error(r.errorMsg)
  return r.data
}

export async function apiPatch<T = any>(url: string, body?: any): Promise<T> {
  const r = (await http.patch(url, body)) as ApiResult<T>
  if (!r.isSuccess) throw new Error(r.errorMsg)
  return r.data
}

export async function apiPut<T = any>(url: string, body?: any): Promise<T> {
  const r = (await http.put(url, body)) as ApiResult<T>
  if (!r.isSuccess) throw new Error(r.errorMsg)
  return r.data
}

export async function apiDelete<T = any>(url: string): Promise<T> {
  const r = (await http.delete(url)) as ApiResult<T>
  if (!r.isSuccess) throw new Error(r.errorMsg)
  return r.data
}

// ---------------- 类型定义 ----------------

export interface BookSource {
  bookSourceUrl: string
  bookSourceName: string
  bookSourceGroup?: string
  enabled: boolean
  custom?: boolean
  verified?: boolean
  verifyErr?: string
  respondTime?: number
  lastUpdateTime?: number
  customOrder?: number
}

export interface ReplaceRule {
  id: number
  name: string
  group?: string
  pattern: string
  replacement: string
  isRegex: boolean
  isEnabled: boolean
  scope?: string
  scopeTitle?: boolean
  scopeContent?: boolean
  excludeScope?: string
  order: number
}

export interface SearchOrigin {
  sourceUrl: string
  sourceName: string
  bookUrl: string
  name: string
  author: string
  kind?: string
  latestChapter?: string
  coverUrl?: string
  intro?: string
  wordCount?: string
}

export interface SearchResult {
  name: string
  author: string
  kind?: string
  coverUrl?: string
  intro?: string
  latestChapter?: string
  origins: SearchOrigin[]
}

export interface SearchJobState {
  searchId: string
  key: string
  running: boolean
  searched: number
  total: number
  elapsedMs: number
  items: SearchResult[]
  errors?: { source: string; err: string }[]
}

export interface BookStats {
  totalChapters: number
  doneChapters: number
  failedChapters: number
  pendingChapters: number
  activeTask?: { id: number; status: string; phase?: string; done: number; failed: number; total: number; errMsg?: string; exportPath?: string }
  lastTask?: { id: number; status: string; phase?: string; done: number; failed: number; total: number; errMsg?: string; exportPath?: string }
  hasTask: boolean
}

export interface SourceBook {
  id: number
  bookUrl: string
  origin: string
  originName?: string
  name: string
  author: string
  coverUrl?: string
  intro?: string
  latestChapter?: string
  totalChapters: number
  updatedAt?: number
  stats?: BookStats
  /** 书源已被删除（清理/手动），无法续传 */
  sourceMissing?: boolean
}

export interface BookGroup {
  primary: Book
  sources: SourceBook[]
  sourceCount: number
  totalChapters: number
  doneChapters: number
  hasCover: boolean
  coverUrl?: string
  intro?: string
  kind?: string
}

export interface Overview {
  sources: { total: number; enabled: number; disabled: number; verified: number; abnormal: number }
  books: { count: number; totalChapters: number; cachedChapters: number }
  tasks: { recent: number; byStatus: Record<string, number> }
  exports: { count: number; totalSize: number; byFormat: Record<string, number> }
}

export interface Book {
  id: number
  bookUrl: string
  tocUrl: string
  origin: string
  originName?: string
  name: string
  author: string
  kind?: string
  coverUrl?: string
  intro?: string
  latestChapter?: string
  totalChapters: number
  updatedAt?: number
}

export interface Chapter {
  index: number
  title: string
  url: string
  isVolume: boolean
  status: number
  content?: string
  contentLen?: number
  errMsg?: string
}

export interface DownloadTask {
  id: number
  bookId: number
  bookName: string
  author: string
  origin?: string
  originName?: string
  status: string
  phase?: string
  formats: string[]
  totalChapters: number
  doneChapters: number
  failChapters: number
  exportPath?: string
  errMsg?: string
  createdAt: number
  updatedAt?: number
}

export interface ExportItem {
  id: number
  taskId: number
  bookId: number
  name: string
  format: string
  size: number
  path: string
  createdAt: number
}

export interface SourceRepo {
  name: string
  url: string
  kind: string
  note?: string
}

export interface ImportResult {
  total: number
  inserted: number
  updated: number
  errors?: string[]
}

export interface DebugStep {
  name: string
  detail?: string
  ok: boolean
  elapsedMs: number
}

export interface DebugTrace {
  steps: DebugStep[]
  items?: SearchOrigin[]
  err?: string
}

// ---------------- 工具 ----------------

// 封面代理地址（绕过防盗链；无封面返回空串）
export function coverProxy(u?: string | null): string {
  if (!u) return ''
  return `/api/cover?url=${encodeURIComponent(u)}`
}

// SSE 事件订阅。
export function subscribeEvents(handlers: {
  onTaskProgress?: (data: any) => void
  onTaskDone?: (data: any) => void
  onTaskError?: (data: any) => void
  onSearchProgress?: (data: any) => void
  onSearchDone?: (data: any) => void
  onCleanupProgress?: (data: any) => void
}): () => void {
  const es = new EventSource('/api/events')
  const bind = (name: string, fn?: (d: any) => void) => {
    es.addEventListener(name, (e) => {
      try {
        fn?.(JSON.parse((e as MessageEvent).data))
      } catch {
        /* 忽略坏帧 */
      }
    })
  }
  bind('task.progress', handlers.onTaskProgress)
  bind('task.paused', handlers.onTaskProgress)
  bind('task.done', handlers.onTaskDone)
  bind('task.error', handlers.onTaskError)
  bind('search.progress', handlers.onSearchProgress)
  bind('search.done', handlers.onSearchDone)
  bind('cleanup.progress', handlers.onCleanupProgress)
  return () => es.close()
}

// ---------------- 业务 API ----------------

export const api = {
  health: () => apiGet('/api/health'),

  // 书源
  listSources: (params?: { keyword?: string; enabled?: number; limit?: number; offset?: number }) =>
    apiGet<{ items: BookSource[]; total: number }>('/api/sources', params),
  importSources: (body: { url?: string; text?: string }) =>
    apiPost<ImportResult>('/api/sources/import', body),
  syncRepo: (url: string) => apiPost<ImportResult>('/api/sources/sync', { url }),
  repos: () => apiGet<SourceRepo[]>('/api/sources/repos'),
  exportSources: () => `/api/sources/export`,
  setSourceEnabled: (url: string, enabled: boolean) =>
    apiPatch(`/api/sources/${encodeURIComponent(url)}`, { enabled }),
  deleteSource: (url: string) => apiDelete(`/api/sources/${encodeURIComponent(url)}`),
  verifySources: (urls: string[]) =>
    apiPost<{ url: string; name: string; ok: boolean; elapsedMs: number; err?: string }[]>(
      '/api/sources/verify',
      { urls },
    ),
  cleanupStart: (body: { scope?: 'all' | 'enabled'; mode?: 'check' | 'disable' | 'delete' }) =>
    apiPost<{ cleanupId: string; running: boolean; total: number }>('/api/sources/cleanup', body),
  cleanupState: (id: string) =>
    apiGet<{
      checked: number
      total: number
      dead: number
      alive: number
      disabled: number
      deleted: number
      elapsedMs: number
      running: boolean
      results: { url: string; name: string; ok: boolean; elapsedMs: number; err?: string }[]
    }>(`/api/sources/cleanup/${id}`),
  cleanupCancel: (id: string) => apiDelete(`/api/sources/cleanup/${id}`),
  sourceDebug: (url: string, key?: string) =>
    apiGet<DebugTrace>('/api/sources/debug', { url, key: key || '斗破苍穹' }),

  // 净化规则
  listReplace: (enabledOnly = false) =>
    apiGet<ReplaceRule[]>('/api/replace', { enabledOnly: enabledOnly ? 1 : 0 }),
  importReplace: (body: { url?: string; text?: string }) =>
    apiPost<ImportResult>('/api/replace/import', body),
  saveReplace: (rule: Partial<ReplaceRule>) => apiPost<ReplaceRule>('/api/replace', rule),
  deleteReplace: (id: number) => apiDelete(`/api/replace/${id}`),
  testReplace: (body: { pattern: string; replacement: string; isRegex: boolean; text: string }) =>
    apiPost<{ result: string }>('/api/replace/test', body),

  // 搜索（异步任务）
  searchStart: (key: string, sourceUrls?: string[]) =>
    apiPost<{ searchId: string }>('/api/search', { key, sourceUrls }),
  searchState: (id: string) => apiGet<SearchJobState>(`/api/search/${id}`),
  searchCancel: (id: string) => apiDelete(`/api/search/${id}`),

  // 书架
  listBooks: (params?: { keyword?: string; limit?: number; offset?: number }) =>
    apiGet<{ items: (Book & { stats?: BookStats })[]; total: number }>('/api/books', params),
  bookStats: (id: number) => apiGet<BookStats>(`/api/books/${id}/stats`),
  bookDownload: (id: number, body: { formats: string[]; rangeStart?: number; rangeEnd?: number; concurrency?: number }) =>
    apiPost<{ taskId: number; existing: boolean }>(`/api/books/${id}/download`, body),
  exportNow: (id: number, formats: string[]) =>
    apiPost<{ taskId: number; exportPath: string }>(`/api/books/${id}/export-now`, { formats }),
  bookToc: (id: number, content = false) =>
    apiGet<Chapter[]>(`/api/books/${id}/toc`, { content: content ? 1 : 0 }),
  deleteBook: (id: number) => apiDelete(`/api/books/${id}`),

  // 任务
  createTask: (body: {
    bookUrl: string
    bookName?: string
    author?: string
    tocUrl?: string
    sourceUrl: string
    coverUrl?: string
    intro?: string
    formats: string[]
    rangeStart?: number
    rangeEnd?: number
    concurrency?: number
  }) => apiPost<{ taskId: number; existing?: boolean }>('/api/tasks', body),
  listTasks: (limit = 50) => apiGet<DownloadTask[]>('/api/tasks', { limit }),
  taskFailures: (id: number) => apiGet<Chapter[]>(`/api/tasks/${id}/failures`),
  pauseTask: (id: number) => apiPost(`/api/tasks/${id}/pause`),
  resumeTask: (id: number) => apiPost(`/api/tasks/${id}/resume`),
  cancelTask: (id: number) => apiPost(`/api/tasks/${id}/cancel`),
  retryTask: (id: number) => apiPost(`/api/tasks/${id}/retry`),
  getTask: (id: number) => apiGet<DownloadTask>(`/api/tasks/${id}`),

  // 书架聚合视图（同书多源合并）
  listBookGroups: (keyword?: string) =>
    apiGet<{ items: BookGroup[]; total: number }>('/api/books/grouped', keyword ? { keyword } : undefined),

  // 搜索历史
  getSearchHistory: () => apiGet<string[]>('/api/search-history'),
  addSearchHistory: (key: string) => apiPost<string[]>('/api/search-history', { key }),
  clearSearchHistory: () => apiDelete('/api/search-history'),

  // 任务
  clearFinishedTasks: () => apiPost<{ deleted: number }>('/api/tasks/clear-finished'),

  // 导出（支持按书过滤）
  listExports: (limit = 100, bookId?: number) =>
    apiGet<ExportItem[]>('/api/exports', bookId ? { limit, bookId } : { limit }),
  deleteExport: (id: number) => apiDelete(`/api/exports/${id}`),
  batchSources: (action: 'enable' | 'disable' | 'delete', urls: string[]) =>
    apiPost<{ affected: number }>('/api/sources/batch', { action, urls }),
  batchBooks: (action: 'remove', ids: number[]) =>
    apiPost<{ affected: number }>('/api/books/batch', { action, ids }),
  overview: () => apiGet<Overview>('/api/overview'),
  getSettings: () => apiGet<Record<string, string>>('/api/settings'),
  putSettings: (s: Record<string, string>) => apiPut<Record<string, string>>('/api/settings', s),


  fileUrl: (name: string) => `/api/files/${encodeURIComponent(name)}`,
}

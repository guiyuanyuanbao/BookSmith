// 下载任务全局状态 store：跨页面共享的任务列表与轮询。
// 浮窗组件挂在 App 级别，切换页面不丢失下载进度可见性。
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api, subscribeEvents } from '@/api'

export interface DlTaskInfo {
  name: string
  author?: string
  done: number
  total: number
  status: string
  failChapters?: number
  errMsg?: string
  originName?: string
  origin?: string
}

export const useTasksStore = defineStore('tasks', () => {
  // taskId → 任务信息
  const activeDownloads = ref<Record<number, DlTaskInfo>>({})
  const panelOpen = ref(true)
  const panelMin = ref(false)

  let timer: number | null = null
  let sseBound = false

  const entries = computed(() =>
    Object.entries(activeDownloads.value).map(([tid, v]) => ({ tid: Number(tid), ...v })),
  )
  // 浮窗跟踪的任务中非结束态的数量（本会话发起的）
  const panelActiveCount = computed(() => entries.value.filter((d) => !['done', 'failed', 'canceled'].includes(d.status)).length)
  // 后端真实活动任务数（刷新后依然正确；侧边栏展示用）
  const serverActiveCount = ref(0)

  /** 拉取后端活动任务数（running/pending/exporting），供侧边栏等全局展示 */
  async function refreshServerActive() {
    try {
      const tasks = await api.listTasks(100)
      serverActiveCount.value = (tasks ?? []).filter((t) => ['running', 'pending', 'exporting'].includes(t.status)).length
    } catch { /* 忽略 */ }
  }

  /** 从后端任务列表同步进度；后端已不存在的任务（被清空/删除）自动移出面板 */
  async function refresh() {
    if (Object.keys(activeDownloads.value).length === 0) return
    try {
      const tasks = await api.listTasks(100)
      const byId = new Map((tasks ?? []).map((t) => [t.id, t]))
      for (const [tid, info] of Object.entries(activeDownloads.value)) {
        const match = byId.get(Number(tid))
        if (!match) {
          // 后端已无此任务记录（清空已完成/手动删除）→ 移出浮窗
          if (!['running', 'pending', 'exporting'].includes(info.status)) {
            delete activeDownloads.value[Number(tid)]
          }
          continue
        }
        activeDownloads.value[Number(tid)] = {
          ...info,
          done: match.doneChapters,
          total: match.totalChapters,
          status: match.status,
          failChapters: match.failChapters,
          errMsg: match.errMsg,
          originName: match.originName || info.originName,
          origin: match.origin || info.origin,
        }
      }
    } catch { /* 忽略 */ }
  }

  /** SSE 事件按书名同步（搜索页等已订阅场景外，这里兜底） */
  function applyEvent(d: { bookName?: string; done?: number; total?: number; status?: string }) {
    if (!d.bookName) return
    for (const [tid, info] of Object.entries(activeDownloads.value)) {
      if (info.name === d.bookName) {
        activeDownloads.value[Number(tid)] = {
          ...info,
          done: d.done ?? info.done,
          total: d.total ?? info.total,
          status: d.status ?? info.status,
        }
      }
    }
  }

  function add(taskId: number, info: DlTaskInfo) {
    activeDownloads.value[taskId] = info
    panelOpen.value = true
    panelMin.value = false
    refresh()
  }

  function remove(taskId: number) {
    delete activeDownloads.value[taskId]
  }

  /** 应用启动时调用：绑定 SSE + 启动轮询 */
  function start() {
    if (!sseBound) {
      sseBound = true
      subscribeEvents({
        onTaskProgress: (d: any) => {
          applyEvent(d)
          refreshServerActive()
        },
        onTaskDone: (d: any) => {
          applyEvent(d)
          refreshServerActive()
        },
      })
    }
    if (timer === null) {
      refreshServerActive()
      timer = window.setInterval(() => {
        refreshServerActive()
        if (Object.keys(activeDownloads.value).length) refresh()
      }, 3000)
    }
  }

  return { activeDownloads, panelOpen, panelMin, entries, panelActiveCount, serverActiveCount, add, remove, refresh, applyEvent, refreshServerActive, start }
})

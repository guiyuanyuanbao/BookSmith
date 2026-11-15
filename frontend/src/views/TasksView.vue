<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, subscribeEvents, type Chapter, type DownloadTask } from '@/api'

const tasks = ref<DownloadTask[]>([])
const filterTab = ref<'all' | 'active' | 'done'>('all')
const loading = ref(false)
let timer: number | null = null
let closeSSE: (() => void) | null = null
const startTimes = new Map<number, number>()

const statusMap: Record<string, { text: string; type: string }> = {
  pending: { text: '等待中', type: 'info' },
  running: { text: '下载中', type: 'primary' },
  paused: { text: '已暂停', type: 'warning' },
  exporting: { text: '导出中', type: 'warning' },
  done: { text: '已完成', type: 'success' },
  failed: { text: '失败', type: 'danger' },
  canceled: { text: '已取消', type: 'info' },
}

const phaseText = (t: DownloadTask) => {
  if (t.status === 'running' || t.status === 'pending') {
    if (t.phase === 'toc') return '获取目录'
    if (t.phase === 'content') return '下载正文'
  }
  if (t.status === 'exporting' || t.phase === 'export') return '导出文件'
  return ''
}

function speedOf(t: DownloadTask): string {
  const start = startTimes.get(t.id)
  if (!start || t.status !== 'running' || t.doneChapters === 0) return ''
  const minutes = (Date.now() - start) / 60000
  if (minutes <= 0.02) return ''
  const spd = t.doneChapters / minutes
  return `${spd.toFixed(1)} 章/分`
}

// 记录任务首次出现 running 的时间
function trackStart(t: DownloadTask) {
  if (t.status === 'running' && !startTimes.has(t.id)) {
    startTimes.set(t.id, Date.now() - (t.updatedAt ? Date.now() - t.updatedAt : 0))
  }
}

async function load() {
  try {
    tasks.value = (await api.listTasks()) ?? []
    tasks.value.forEach(trackStart)
  } catch {
    /* 轮询容错 */
  }
}

async function action(t: DownloadTask, act: 'pause' | 'resume' | 'cancel' | 'retry') {
  try {
    if (act === 'pause') await api.pauseTask(t.id)
    else if (act === 'resume') await api.resumeTask(t.id)
    else if (act === 'cancel') {
      await ElMessageBox.confirm(`取消任务 #${t.id}（《${t.bookName}》）？`, '确认', { type: 'warning' })
      await api.cancelTask(t.id)
    } else await api.retryTask(t.id)
    load()
  } catch (e: any) {
    if (e.message) ElMessage.error(e.message)
  }
}

const hasFinished = computed(() => tasks.value.some((t) => t.status === 'done' || t.status === 'canceled'))

const filteredTasks = computed(() => {
  switch (filterTab.value) {
    case 'active':
      return tasks.value.filter((t) => ['running', 'pending', 'paused', 'exporting'].includes(t.status))
    case 'done':
      return tasks.value.filter((t) => ['done', 'failed', 'canceled'].includes(t.status))
    default:
      return tasks.value
  }
})

async function clearFinished() {
  try {
    await ElMessageBox.confirm('清空全部已完成/已取消的任务记录？（已导出文件不受影响）', '确认', { type: 'info' })
  } catch { return }
  try {
    const r = await api.clearFinishedTasks()
    ElMessage.success(`已清空 ${r.deleted} 条`)
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

function pct(t: DownloadTask) {
  if (!t.totalChapters) return 0
  return Math.min(100, Math.round(((t.doneChapters + t.failChapters) / t.totalChapters) * 100))
}

// 失败章节对话框
const failDlg = ref(false)
const failList = ref<Chapter[]>([])
const failTask = ref<DownloadTask | null>(null)
async function showFailures(t: DownloadTask) {
  failTask.value = t
  failDlg.value = true
  try {
    failList.value = (await api.taskFailures(t.id)) ?? []
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

let progressTimer: number | null = null

onMounted(() => {
  load()
  timer = window.setInterval(load, 2500)
  // progress 事件每章触发一次（高并发下每秒多个），全量 load 会造成渲染风暴；
  // 进度由 2.5s 轮询兜底，这里只对终态事件立即刷新
  closeSSE = subscribeEvents({
    onTaskProgress: () => {
      if (progressTimer !== null) return // 节流：progress 风暴下 2s 最多刷一次
      progressTimer = window.setTimeout(() => {
        progressTimer = null
        load()
      }, 2000)
    },
    onTaskDone: load,
    onTaskError: load,
  })
})

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
  if (progressTimer) window.clearTimeout(progressTimer)
  closeSSE?.()
})
</script>
<template>
  <div class="page">
    <h1 class="page-title">下载任务</h1>
    <p class="page-sub">异步流水线：获取目录 → 并发下载正文（自动重试）→ 多格式导出；支持暂停/恢复/取消</p>

    <div class="toolbar" style="margin-bottom: 0">
      <el-radio-group v-model="filterTab" size="small">
        <el-radio-button value="all">全部</el-radio-button>
        <el-radio-button value="active">进行中</el-radio-button>
        <el-radio-button value="done">已结束</el-radio-button>
      </el-radio-group>
      <div class="spacer" />
      <el-button size="small" @click="clearFinished" :disabled="!hasFinished">清空已完成/已取消</el-button>
    </div>

    <div class="task-list" v-loading="loading">
      <div v-for="row in filteredTasks" :key="row.id" class="task-card">
        <div class="tc-main">
          <div class="tc-title-row">
            <strong class="tc-name">{{ row.bookName }}</strong>
            <span class="tc-author muted">{{ row.author }}</span>
            <el-tag v-for="f in row.formats" :key="f" size="small" class="tc-fmt">{{ f.toUpperCase() }}</el-tag>
            <StatusPill :status="row.status" />
          </div>
          <el-progress
            :percentage="pct(row)"
            :status="row.failChapters > 0 ? 'warning' : undefined"
            :stroke-width="10"
            :show-text="false"
            class="tc-progress"
          />
          <div class="tc-meta muted">
            <span>{{ row.doneChapters }}/{{ row.totalChapters }} 章</span>
            <span v-if="row.failChapters > 0" class="tc-fail">失败 {{ row.failChapters }}</span>
            <span v-if="speedOf(row)">{{ speedOf(row) }}</span>
            <span v-if="phaseText(row)">{{ phaseText(row) }}</span>
            <el-tooltip v-if="row.errMsg" :content="row.errMsg" placement="top" :show-after="300">
              <span class="tc-err">{{ row.errMsg }}</span>
            </el-tooltip>
          </div>
        </div>
        <div class="tc-ops">
          <el-button v-if="row.status === 'running' || row.status === 'pending'" size="small" round @click="action(row, 'pause')">
            暂停
          </el-button>
          <el-button
            v-if="row.status === 'paused' || row.status === 'failed'"
            size="small" type="primary" round
            @click="action(row, 'resume')"
          >
            恢复
          </el-button>
          <el-button v-if="row.failChapters > 0" size="small" type="warning" plain round @click="action(row, 'retry')">
            重跑失败
          </el-button>
          <el-button v-if="row.failChapters > 0" size="small" round @click="showFailures(row)">
            详情
          </el-button>
          <el-button
            v-if="!['done', 'canceled'].includes(row.status)"
            size="small" type="danger" plain round
            @click="action(row, 'cancel')"
          >
            取消
          </el-button>
        </div>
        <div class="tc-id muted">#{{ row.id }}</div>
      </div>
    </div>

    <el-empty v-if="!loading && tasks.length === 0" description="暂无任务 — 从搜索或书架发起下载" />

    <el-dialog v-model="failDlg" :title="`失败章节 · ${failTask?.bookName ?? ''}`" width="560">
      <el-table :data="failList" max-height="420">
        <el-table-column prop="index" label="#" width="70" />
        <el-table-column prop="title" label="章节" min-width="180" show-overflow-tooltip />
        <el-table-column prop="errMsg" label="错误" min-width="220" show-overflow-tooltip />
      </el-table>
      <template #footer>
        <el-button @click="failDlg = false">关闭</el-button>
        <el-button
          type="warning"
          @click="
            failTask && action(failTask, 'retry');
            failDlg = false
          "
        >
          重跑失败章节
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.task-list { display: flex; flex-direction: column; gap: 14px; }
.task-card {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 18px 22px;
  box-shadow: var(--bs-shadow);
  display: flex;
  align-items: center;
  gap: 18px;
  transition: box-shadow 0.2s ease;
}
.task-card:hover { box-shadow: var(--bs-shadow-hover); }
.tc-main { flex: 1; min-width: 0; }
.tc-title-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 8px;
  flex-wrap: wrap;
}
.tc-name { font-size: 15px; }
.tc-author { font-size: 12.5px; }
.tc-fmt { flex-shrink: 0; }
.tc-progress { max-width: 460px; }
.tc-meta { display: flex; gap: 12px; flex-wrap: wrap; font-size: 12.5px; }
.tc-fail { color: var(--bs-danger); }
.tc-err {
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: help;
}
.tc-side { flex-shrink: 0; display: flex; flex-direction: column; align-items: flex-end; gap: 8px; }
.tc-ops { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }

/* 状态胶囊：彩点 + 文字 */
.tc-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  padding: 3px 10px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  flex-shrink: 0;
}
.tc-status .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--bs-text-sub); }
.tc-status.running .dot, .tc-status.exporting .dot { background: var(--bs-primary); animation: pulse 1.4s ease infinite; }
.tc-status.running, .tc-status.exporting { color: var(--bs-primary); }
.tc-status.paused .dot { background: var(--bs-accent); }
.tc-status.paused { color: var(--bs-accent); }
.tc-status.done .dot { background: var(--bs-success); }
.tc-status.done { color: var(--bs-success); }
.tc-status.failed .dot { background: var(--bs-danger); }
.tc-status.failed { color: var(--bs-danger); }
.tc-status.canceled .dot { background: var(--bs-text-sub); }
@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
.tc-id { position: absolute; right: 14px; top: 10px; font-size: 11px; }
.task-card { position: relative; }
</style>

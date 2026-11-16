<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, subscribeEvents, type Chapter, type DownloadTask } from '@/api'
import { fmtColor } from '@/composables/formatters'

const tasks = ref<DownloadTask[]>([])
const filterTab = ref<'all' | 'active' | 'done'>('all')
const loading = ref(false)
let timer: number | null = null
let closeSSE: (() => void) | null = null
const startTimes = new Map<number, number>()

const STATUS_TEXT: Record<string, string> = {
  running: '下载中', pending: '排队中', paused: '已暂停', exporting: '导出中',
  done: '已完成', failed: '失败', canceled: '已取消',
}
// 运行子阶段描述（仅 running/pending 时有意义）
const phaseText = (t: DownloadTask) => {
  if (t.phase === 'toc') return '获取目录'
  if (t.phase === 'content') return '下载正文'
  if (t.phase === 'export') return '导出文件'
  return ''
}
// 状态文字以任务状态为准（phase 只是运行中的子阶段描述；失败任务的 export phase 不该盖过「失败」）
const statusTextOf = (t: DownloadTask) => {
  if ((t.status === 'running' || t.status === 'pending') && phaseText(t)) return phaseText(t)
  return STATUS_TEXT[t.status] ?? t.status
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

// 进度环颜色随状态（与 StatusPill 同一语义色）
const ringColor = (t: DownloadTask) => {
  switch (t.status) {
    case 'running': case 'exporting': return 'var(--bs-primary)'
    case 'paused': return 'var(--bs-accent)'
    case 'failed': return 'var(--bs-danger)'
    case 'done': return 'var(--bs-success)'
    default: return 'var(--bs-text-sub)'
  }
}
// 终态环内显示 ✓ / ✕，进行中显示百分比
const ringDone = (t: DownloadTask) => t.status === 'done' || t.status === 'canceled' || t.status === 'failed'

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
      <div v-for="row in filteredTasks" :key="row.id" class="task-card" :data-status="row.status">
        <!-- 进度环：视觉锚点，颜色随状态 -->
        <div class="tc-ring" :style="{ '--ring': ringColor(row) }">
          <el-progress
            type="dashboard"
            :percentage="pct(row)"
            :width="64"
            :stroke-width="7"
            :show-text="false"
            :color="ringColor(row)"
          />
          <div class="tc-ring-inner">
            <span v-if="ringDone(row)" class="tc-ring-mark" :class="row.status">{{ row.status === 'done' ? '✓' : row.status === 'failed' ? '✕' : '⨯' }}</span>
            <span v-else class="tc-ring-pct">{{ pct(row) }}<small>%</small></span>
          </div>
        </div>

        <!-- 主信息 -->
        <div class="tc-main">
          <div class="tc-title-row">
            <strong class="tc-name">{{ row.bookName }}</strong>
            <span class="tc-author muted">{{ row.author || '未知作者' }}</span>
            <span class="tc-fmt-list">
              <span v-for="f in row.formats" :key="f" class="tc-fmt" :style="{ background: fmtColor(f) }">{{ f.toUpperCase() }}</span>
            </span>
            <span class="tc-status-text" :class="row.status">{{ statusTextOf(row) }}</span>
            <span class="tc-id">#{{ row.id }}</span>
          </div>
          <div class="tc-meta">
            <span>{{ row.doneChapters }}/{{ row.totalChapters }} 章</span>
            <template v-if="row.failChapters > 0">
              <span class="tc-fail" @click="showFailures(row)">失败 {{ row.failChapters }}（点击查看）</span>
            </template>
            <span v-if="speedOf(row)">{{ speedOf(row) }}</span>
            <el-tooltip v-if="row.errMsg" :content="row.errMsg" placement="top" :show-after="300">
              <span class="tc-err">{{ row.errMsg }}</span>
            </el-tooltip>
          </div>
          <!-- 细进度线（环的补充：用于直观比较各任务相对进度） -->
          <div class="tc-bar"><div class="tc-bar-fill" :style="{ width: pct(row) + '%', background: ringColor(row) }" :class="{ striped: row.status === 'running' }" /></div>
        </div>

        <!-- 操作区 -->
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
          <el-button
            v-if="!['done', 'canceled'].includes(row.status)"
            size="small" type="danger" plain round
            @click="action(row, 'cancel')"
          >
            取消
          </el-button>
        </div>
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
.task-list { display: flex; flex-direction: column; gap: 12px; }
.task-card {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 14px 18px;
  box-shadow: var(--bs-shadow);
  display: flex;
  align-items: center;
  gap: 16px;
  transition: box-shadow 0.2s ease, border-color 0.2s ease;
}
.task-card:hover { box-shadow: var(--bs-shadow-hover); }
/* 终态卡片弱化：降低存在感，视觉焦点留给进行中任务 */
.task-card[data-status='done'], .task-card[data-status='canceled'] { opacity: 0.72; }
.task-card[data-status='done']:hover, .task-card[data-status='canceled']:hover { opacity: 1; }

/* ---- 进度环 ---- */
.tc-ring { position: relative; width: 64px; height: 64px; flex-shrink: 0; }
.tc-ring :deep(.el-progress__text) { display: none; }
.tc-ring-inner {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
}
.tc-ring-pct { font-size: 15px; font-weight: 700; color: var(--bs-text); line-height: 1; }
.tc-ring-pct small { font-size: 10px; font-weight: 600; color: var(--bs-text-sub); margin-left: 1px; }
.tc-ring-mark { font-size: 20px; font-weight: 700; line-height: 1; }
.tc-ring-mark.done { color: var(--bs-success); }
.tc-ring-mark.failed { color: var(--bs-danger); }
.tc-ring-mark.canceled { color: var(--bs-text-sub); }

/* ---- 主信息 ---- */
.tc-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 7px; }
.tc-title-row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  min-width: 0;
}
.tc-name {
  font-size: 15px;
  font-family: 'Noto Serif SC', serif;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tc-author { font-size: 12.5px; flex-shrink: 0; }
.tc-fmt-list { display: inline-flex; gap: 4px; flex-shrink: 0; }
.tc-fmt {
  font-size: 10px;
  font-weight: 700;
  color: #fff;
  padding: 1.5px 6px;
  border-radius: 5px;
  letter-spacing: 0.3px;
}
.tc-status-text { font-size: 12.5px; font-weight: 600; margin-left: auto; flex-shrink: 0; }
.tc-status-text.running, .tc-status-text.exporting { color: var(--bs-primary); }
.tc-status-text.paused { color: var(--bs-accent); }
.tc-status-text.done { color: var(--bs-success); }
.tc-status-text.failed { color: var(--bs-danger); }
.tc-id { font-size: 11px; color: var(--bs-text-sub); flex-shrink: 0; font-variant-numeric: tabular-nums; }

.tc-meta {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12.5px;
  color: var(--bs-text-sub);
  min-width: 0;
}
.tc-fail { color: var(--bs-danger); cursor: pointer; }
.tc-fail:hover { text-decoration: underline; }
.tc-err {
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: help;
}
.tc-bar {
  height: 5px;
  border-radius: 999px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.tc-bar-fill {
  height: 100%;
  border-radius: 999px;
  transition: width 0.5s ease;
}
.tc-bar-fill.striped {
  background-image: linear-gradient(
    120deg,
    rgba(255, 255, 255, 0.28) 25%, transparent 25%,
    transparent 50%, rgba(255, 255, 255, 0.28) 50%,
    rgba(255, 255, 255, 0.28) 75%, transparent 75%
  );
  background-size: 14px 14px;
  animation: tc-stripes 0.9s linear infinite;
}
@keyframes tc-stripes {
  to { background-position: 14px 0; }
}

/* ---- 操作区 ---- */
.tc-ops { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; flex-shrink: 0; }
</style>

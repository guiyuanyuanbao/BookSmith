<!-- 下载任务浮窗：挂在 App 级别，任何页面都持续可见。 -->
<!-- 实时进度 / 失败详情 / 一键换源重下（换源跳回搜索页并复用结果）。 -->
<script setup lang="ts">
import { ElMessage } from 'element-plus'
import { api } from '@/api'
import { useTasksStore } from '@/stores/tasks'
import { useRouter } from 'vue-router'

const store = useTasksStore()
const router = useRouter()

function percent(d: { done: number; total: number }) {
  if (!d.total) return 0
  return Math.min(100, Math.round((d.done / d.total) * 100))
}

function statusView(s: string) {
  switch (s) {
    case 'running': return { text: '下载中', type: 'primary' as const }
    case 'exporting': return { text: '导出中', type: 'warning' as const }
    case 'paused': return { text: '已暂停', type: 'info' as const }
    case 'done': return { text: '已完成', type: 'success' as const }
    case 'failed': return { text: '失败', type: 'danger' as const }
    case 'canceled': return { text: '已取消', type: 'info' as const }
    default: return { text: s, type: 'info' as const }
  }
}

async function action(tid: number, act: 'pause' | 'resume' | 'cancel') {
  try {
    await (act === 'pause' ? api.pauseTask(tid) : act === 'resume' ? api.resumeTask(tid) : api.cancelTask(tid))
    await store.refresh()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

/** 换源重下：带上书名与失败源跳回搜索页，由搜索页用已缓存的搜索结果打开选源对话框 */
function switchSource(tid: number) {
  const d = store.activeDownloads[tid]
  if (!d) return
  router.push({
    path: '/search',
    query: { reswitch: String(tid), name: d.name, author: d.author ?? '', exclude: d.origin ?? '' },
  })
}
</script>

<template>
  <div v-if="store.panelOpen && store.entries.length" class="dl-panel" :class="{ min: store.panelMin }">
    <div class="dl-panel-head" @click="store.panelMin = !store.panelMin">
      <span>下载任务（{{ store.entries.length }}）</span>
      <span class="dl-panel-toggle">{{ store.panelMin ? '▲' : '▼' }}</span>
    </div>
    <template v-if="!store.panelMin">
      <div v-for="d in store.entries" :key="d.tid" class="dl-item">
        <div class="dl-item-top">
          <strong class="dl-item-name" :title="d.name">{{ d.name }}</strong>
          <el-tag size="small" :type="statusView(d.status).type">{{ statusView(d.status).text }}</el-tag>
        </div>
        <div class="dl-item-src muted">
          {{ d.originName || d.origin || '—' }}<template v-if="d.author"> · {{ d.author }}</template>
        </div>
        <el-progress
          v-if="d.status !== 'failed' && d.status !== 'canceled'"
          :percentage="percent(d)"
          :stroke-width="8"
          :status="d.status === 'done' ? 'success' : d.status === 'paused' ? 'warning' : undefined"
        />
        <div v-if="d.status === 'failed'" class="dl-fail">
          <div class="dl-fail-msg" :title="d.errMsg">{{ d.errMsg || '下载失败' }}</div>
          <div v-if="d.failChapters" class="muted" style="font-size: 12px">失败章节 {{ d.failChapters }} 章</div>
        </div>
        <div class="dl-item-ops">
          <template v-if="d.status === 'running' || d.status === 'exporting'">
            <el-button size="small" link @click="action(d.tid, 'pause')">暂停</el-button>
          </template>
          <template v-else-if="d.status === 'paused'">
            <el-button size="small" link type="primary" @click="action(d.tid, 'resume')">恢复</el-button>
          </template>
          <template v-if="['running', 'exporting', 'paused'].includes(d.status)">
            <el-button size="small" link type="danger" @click="action(d.tid, 'cancel')">取消</el-button>
          </template>
          <template v-if="['failed', 'canceled'].includes(d.status)">
            <el-button size="small" link type="primary" @click="switchSource(d.tid)">换源重下</el-button>
          </template>
          <el-button
            v-if="['done', 'failed', 'canceled'].includes(d.status)"
            size="small" link @click="store.remove(d.tid)"
          >移除</el-button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.dl-panel {
  position: fixed;
  right: 20px;
  bottom: 20px;
  width: 320px;
  max-height: 60vh;
  overflow-y: auto;
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: 14px;
  box-shadow: 0 8px 32px rgba(40, 36, 28, 0.18);
  z-index: 900;
  padding: 6px 0;
}
.dl-panel.min { width: 220px; overflow: hidden; }
.dl-panel-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 16px;
  font-size: 13px;
  font-weight: 600;
  color: var(--bs-text);
  cursor: pointer;
  user-select: none;
  position: sticky;
  top: 0;
  background: var(--bs-card);
}
.dl-panel-toggle { color: var(--bs-text-sub); font-size: 11px; }
.dl-item {
  padding: 10px 16px 12px;
  border-top: 1px solid var(--bs-border);
}
.dl-item-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 2px;
}
.dl-item-name {
  font-size: 13.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.dl-item-src {
  font-size: 12px;
  margin-bottom: 6px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.dl-fail {
  margin-top: 6px;
  padding: 8px 10px;
  background: rgba(208, 86, 86, 0.08);
  border-radius: 8px;
}
.dl-fail-msg {
  font-size: 12.5px;
  color: var(--bs-danger, #d05656);
  line-height: 1.5;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.dl-item-ops {
  display: flex;
  justify-content: flex-end;
  gap: 4px;
  margin-top: 4px;
  min-height: 22px;
}
</style>

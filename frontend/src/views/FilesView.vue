<!-- 导出文件页：全部导出记录，支持按书过滤、下载、删除 -->
<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api } from '@/api'
import { useTasksStore } from '@/stores/tasks'

interface ExportItem {
  id: number
  bookId: number
  bookName?: string
  name: string
  format: string
  size: number
  createdAt: number
  originName?: string
}

const items = ref<ExportItem[]>([])
const loading = ref(false)
const keyword = ref('')
const fmtFilter = ref('all')

const fmtColors: Record<string, string> = {
  txt: '#67c23a', epub: '#409eff', pdf: '#f56c6c', html: '#e6a23c', md: '#909399',
}

const filtered = computed(() => {
  let out = items.value
  if (keyword.value) {
    const k = keyword.value.toLowerCase()
    out = out.filter((e) => e.name.toLowerCase().includes(k))
  }
  if (fmtFilter.value !== 'all') {
    out = out.filter((e) => e.format === fmtFilter.value)
  }
  return out
})

const totalCount = computed(() => items.value.length)
const totalSize = computed(() => items.value.reduce((s, e) => s + e.size, 0))

function fmtSize(n: number) {
  return n < 1048576 ? `${(n / 1024).toFixed(1)} KB` : `${(n / 1048576).toFixed(2)} MB`
}

function fmtTime(ts: number) {
  const d = new Date(ts)
  const pad = (x: number) => String(x).padStart(2, '0')
  return `${d.getMonth() + 1}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

async function load() {
  loading.value = true
  try {
    items.value = ((await api.listExports(200)) ?? []) as ExportItem[]
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function remove(e: ExportItem) {
  try {
    await ElMessageBox.confirm(`删除 ${e.name}？磁盘文件将一并删除。`, '确认删除', {
      type: 'warning', confirmButtonText: '删除',
    })
  } catch { return }
  try {
    await api.deleteExport(e.id)
    ElMessage.success('已删除')
    load()
  } catch (e2: any) {
    ElMessage.error(e2.message)
  }
}

function downloadAll() {
  filtered.value.forEach((e, i) => {
    setTimeout(() => {
      const a = document.createElement('a')
      a.href = api.fileUrl(e)
      a.download = e.name
      document.body.appendChild(a)
      a.click()
      a.remove()
    }, i * 300)
  })
}

onMounted(load)
const tasksStore = useTasksStore()
</script>

<template>
  <div class="page" style="max-width: 980px">
    <h1 class="page-title">导出文件</h1>
    <p class="page-sub">
      全部已生成的电子书文件
      <template v-if="totalCount"> · 共 {{ totalCount }} 个 · {{ fmtSize(totalSize) }}</template>
    </p>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索文件名" style="width: 220px" clearable />
      <el-select v-model="fmtFilter" style="width: 110px">
        <el-option value="all" label="全部格式" />
        <el-option v-for="(c, f) in fmtColors" :key="f" :value="f" :label="f.toUpperCase()" />
      </el-select>
      <div class="spacer" />
      <el-button :disabled="!filtered.length" @click="downloadAll">全部下载</el-button>
      <el-button @click="load">刷新</el-button>
    </div>

    <div v-if="loading" style="padding: 40px 0; text-align: center" class="muted">加载中…</div>
    <el-empty v-else-if="!filtered.length" description="还没有导出文件，去书架导出或搜索下载新书吧" />

    <div v-else class="file-list">
      <div v-for="e in filtered" :key="e.id" class="file-row">
        <el-tag size="small" :color="fmtColors[e.format] || '#909399'" class="fmt-tag">
          {{ e.format.toUpperCase() }}
        </el-tag>
        <div class="file-main">
          <div class="file-name" :title="e.name">{{ e.name }}</div>
          <div class="muted file-meta">{{ fmtSize(e.size) }} · {{ fmtTime(e.createdAt) }}<template v-if="e.originName"> · 来源 {{ e.originName }}</template></div>
        </div>
        <a :href="api.fileUrl(e)" target="_blank">
          <el-button type="primary" size="small" plain>下载</el-button>
        </a>
        <el-button size="small" type="danger" plain @click="remove(e)">删除</el-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 16px; }
.spacer { flex: 1; }
.file-list {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  box-shadow: var(--bs-shadow);
  overflow: hidden;
}
.file-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--bs-border);
  transition: background 0.12s;
}
.file-row:last-child { border-bottom: none; }
.file-row:hover { background: var(--el-fill-color-light); }
.fmt-tag { color: #fff; border: none; width: 54px; text-align: center; flex-shrink: 0; }
.file-main { flex: 1; min-width: 0; }
.file-name {
  font-size: 13.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.file-meta { font-size: 12px; }
</style>

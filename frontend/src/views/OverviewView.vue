<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, type Overview } from '@/api'

const data = ref<Overview | null>(null)
const loading = ref(false)

// 任务状态英文键 → 中文标签
const taskStatusLabels: Record<string, string> = {
  pending: '等待中', running: '进行中', paused: '已暂停', done: '已完成',
  failed: '失败', canceled: '已取消', exporting: '导出中',
}
const statusLabel = (st: string) => taskStatusLabels[st] ?? st

function fmtSize(n: number) {
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

async function load() {
  loading.value = true
  try {
    data.value = await api.overview()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<script lang="ts">
import { ElMessage } from 'element-plus'
export default {}
</script>

<template>
  <div class="page">
    <h1 class="page-title">概览</h1>
    <p class="page-sub">书源健康、书架缓存、任务与导出的一览统计</p>

    <div v-loading="loading" style="min-height: 300px">
      <template v-if="data">
        <div class="stat-grid">
          <!-- 书源 -->
          <div class="stat-card">
            <div class="stat-head">📖 书源</div>
            <div class="stat-num">{{ data.sources.total }}</div>
            <div class="stat-rows">
              <div class="stat-row"><span>已启用</span><b>{{ data.sources.enabled }}</b></div>
              <div class="stat-row"><span>已禁用</span><b>{{ data.sources.disabled }}</b></div>
              <div class="stat-row"><span style="color: var(--bs-success)">校验可用</span><b style="color: var(--bs-success)">{{ data.sources.verified }}</b></div>
              <div class="stat-row"><span style="color: var(--bs-danger)">校验异常</span><b style="color: var(--bs-danger)">{{ data.sources.abnormal }}</b></div>
            </div>
            <router-link to="/sources"><el-button size="small" style="width:100%">管理书源</el-button></router-link>
          </div>

          <!-- 书架 -->
          <div class="stat-card">
            <div class="stat-head">📚 书架</div>
            <div class="stat-num">{{ data.books.count }}</div>
            <div class="stat-rows">
              <div class="stat-row"><span>章节总数</span><b>{{ data.books.totalChapters }}</b></div>
              <div class="stat-row"><span>已缓存章节</span><b style="color: var(--bs-primary)">{{ data.books.cachedChapters }}</b></div>
            </div>
            <router-link to="/books"><el-button size="small" style="width:100%">打开书架</el-button></router-link>
          </div>

          <!-- 任务 -->
          <div class="stat-card">
            <div class="stat-head">⬇️ 任务</div>
            <div class="stat-num">{{ data.tasks.recent }}</div>
            <div class="stat-rows">
              <div class="stat-row" v-for="(n, st) in data.tasks.byStatus" :key="st">
                <span>{{ statusLabel(String(st)) }}</span><b>{{ n }}</b>
              </div>
            </div>
            <router-link to="/tasks"><el-button size="small" style="width:100%">查看任务</el-button></router-link>
          </div>

          <!-- 导出 -->
          <div class="stat-card">
            <div class="stat-head">📦 导出</div>
            <div class="stat-num">{{ data.exports.count }}</div>
            <div class="stat-rows">
              <div class="stat-row"><span>总大小</span><b>{{ fmtSize(data.exports.totalSize) }}</b></div>
              <div class="stat-row" v-for="(n, f) in data.exports.byFormat" :key="f">
                <span style="text-transform: uppercase">{{ f }}</span><b>{{ n }}</b>
              </div>
            </div>
            <router-link to="/files"><el-button size="small" style="width:100%">查看导出</el-button></router-link>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(230px, 1fr));
  gap: 20px;
}
.stat-card {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 20px;
  box-shadow: var(--bs-shadow);
  display: flex;
  flex-direction: column;
}
.stat-head { font-size: 13px; color: var(--bs-text-sub); margin-bottom: 6px; }
.stat-num { font-size: 36px; font-weight: 700; color: var(--bs-primary); margin-bottom: 10px; font-family: 'Noto Serif SC', serif; }
.stat-rows { flex: 1; margin-bottom: 14px; }
.stat-row {
  display: flex; justify-content: space-between;
  padding: 4px 0; font-size: 13px;
  color: var(--bs-text-sub);
  border-bottom: 1px dashed var(--bs-border);
}
.stat-row:last-child { border-bottom: none; }
.stat-row b { color: var(--bs-text); }
</style>

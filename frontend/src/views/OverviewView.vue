<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type Overview } from '@/api'
import { useRouter } from 'vue-router'

import { fmtSize } from '@/composables/formatters'
const router = useRouter()
const data = ref<Overview | null>(null)
const loading = ref(false)

/** 进行中任务数（running/pending/exporting） */
const activeCount = computed(() => {
  if (!data.value) return 0
  const by = data.value.tasks.byStatus
  return (by.running ?? 0) + (by.pending ?? 0) + (by.exporting ?? 0)
})

/** 书架缓存进度百分比 */
const cachedPct = computed(() => {
  if (!data.value || !data.value.books.totalChapters) return 0
  return Math.round((data.value.books.cachedChapters / data.value.books.totalChapters) * 100)
})

function go(path: string) {
  router.push(path)
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
  <div class="page home">
    <section class="hero" v-loading="loading">
      <div class="hero-text">
        <h1>书籍工坊</h1>
        <p>把网文一键做成精排电子书 —— 书源、下载与导出，一站完成</p>
      </div>
      <div v-if="data" class="hero-stats">
        <button class="hstat" @click="go('/sources')">
          <span class="hstat-num">{{ data.sources.total }}</span>
          <span class="hstat-label">书源</span>
          <span class="hstat-sub">可用 {{ data.sources.verified }}</span>
        </button>
        <button class="hstat" @click="go('/books')">
          <span class="hstat-num">{{ data.books.count }}</span>
          <span class="hstat-label">书架</span>
          <span class="hstat-sub">已缓存 {{ cachedPct }}%</span>
        </button>
        <button class="hstat" @click="go('/tasks')">
          <span class="hstat-num">{{ activeCount }}</span>
          <span class="hstat-label">进行中</span>
          <span class="hstat-sub">共 {{ data.tasks.recent }} 个任务</span>
        </button>
        <button class="hstat" @click="go('/files')">
          <span class="hstat-num">{{ data.exports.count }}</span>
          <span class="hstat-label">成品</span>
          <span class="hstat-sub">{{ fmtSize(data.exports.totalSize) }}</span>
        </button>
      </div>
    </section>

    <section class="launchers">
      <button class="launcher" @click="go('/search')">
        <span class="l-icon">🔍</span>
        <span class="l-body">
          <span class="l-title">搜索新书</span>
          <span class="l-desc">聚合 2600+ 书源，一键比源下载</span>
        </span>
        <span class="l-arrow">→</span>
      </button>
      <button class="launcher" @click="go('/books')">
        <span class="l-icon">📚</span>
        <span class="l-body">
          <span class="l-title">我的书架</span>
          <span class="l-desc">多源缓存与续传，成品导出管理</span>
        </span>
        <span class="l-arrow">→</span>
      </button>
      <button class="launcher" @click="go('/explore')">
        <span class="l-icon">🧭</span>
        <span class="l-body">
          <span class="l-title">发现榜单</span>
          <span class="l-desc">按书源浏览分类榜单，挑书更直观</span>
        </span>
        <span class="l-arrow">→</span>
      </button>
    </section>

    <p class="home-foot muted" v-if="data">
      导出成品共 {{ data.exports.count }} 份（{{ fmtSize(data.exports.totalSize) }}）· 任务记录 {{ data.tasks.recent }} 条
    </p>
  </div>
</template>

<style scoped>
.home { padding-top: 40px; }

/* Hero：墨绿渐变 + 玻璃质感统计 */
.hero {
  background:
    radial-gradient(ellipse 90% 120% at 90% -20%, rgba(126, 190, 164, 0.35), transparent),
    linear-gradient(135deg, #1c362e 0%, #2f6f5e 65%, #3d8a75 100%);
  border-radius: 22px;
  padding: 34px 38px 30px;
  color: #f5f2ea;
  box-shadow: 0 10px 32px rgba(28, 54, 46, 0.28);
  margin-bottom: 22px;
}
.hero-text h1 {
  margin: 0;
  font-size: 28px;
  font-weight: 700;
  letter-spacing: 1px;
}
.hero-text p {
  margin: 8px 0 22px;
  color: rgba(245, 242, 234, 0.82);
  font-size: 14px;
}
.hero-stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.hstat {
  background: rgba(255, 255, 255, 0.1);
  border: 1px solid rgba(255, 255, 255, 0.14);
  border-radius: 14px;
  padding: 14px 18px;
  cursor: pointer;
  text-align: left;
  display: flex;
  flex-direction: column;
  gap: 2px;
  transition: background 0.2s ease, transform 0.2s ease;
  backdrop-filter: blur(4px);
}
.hstat:hover { background: rgba(255, 255, 255, 0.18); transform: translateY(-2px); }
.hstat-num { font-size: 26px; font-weight: 700; color: #fff; line-height: 1.1; }
.hstat-label { font-size: 12.5px; color: rgba(245, 242, 234, 0.9); }
.hstat-sub { font-size: 11px; color: rgba(245, 242, 234, 0.55); }

/* 快捷启动器 */
.launchers {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
}
.launcher {
  display: flex;
  align-items: center;
  gap: 14px;
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 18px 20px;
  cursor: pointer;
  text-align: left;
  box-shadow: var(--bs-shadow);
  transition: transform 0.2s ease, box-shadow 0.2s ease;
}
.launcher:hover {
  transform: translateY(-3px);
  box-shadow: var(--bs-shadow-hover);
}
.launcher:hover .l-arrow { transform: translateX(4px); }
.l-icon { font-size: 26px; }
.l-body { flex: 1; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.l-title { font-size: 15px; font-weight: 600; color: var(--bs-text); }
.l-desc { font-size: 12.5px; color: var(--bs-text-sub); }
.l-arrow { color: var(--bs-text-sub); transition: transform 0.2s ease; }

.home-foot { text-align: center; margin-top: 26px; }

@media (max-width: 760px) {
  .hero-stats { grid-template-columns: repeat(2, 1fr); }
  .launchers { grid-template-columns: 1fr; }
}
</style>

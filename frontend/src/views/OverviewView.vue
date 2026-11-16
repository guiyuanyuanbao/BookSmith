<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, type DownloadTask, type Overview } from '@/api'
import { useRouter } from 'vue-router'
import { fmtSize, fmtColor } from '@/composables/formatters'
import StatusPill from '@/components/StatusPill.vue'

const router = useRouter()
const data = ref<Overview | null>(null)
const recentTasks = ref<DownloadTask[]>([])
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

/** 导出格式分布（按计数降序，只保留有数据的格式） */
const formatBars = computed(() => {
  const by = data.value?.exports.byFormat ?? {}
  const rows = Object.entries(by).map(([format, count]) => ({ format, count, color: fmtColor(format) }))
  rows.sort((a, b) => b.count - a.count)
  const max = Math.max(1, ...rows.map((r) => r.count))
  return rows.map((r) => ({ ...r, pct: Math.round((r.count / max) * 100) }))
})

/** 最近任务（前 5 条，活跃优先） */
const taskPreview = computed(() => {
  const rank = (s: string) => (['running', 'exporting', 'pending'].includes(s) ? 0 : s === 'paused' ? 1 : 2)
  return [...recentTasks.value].sort((a, b) => rank(a.status) - rank(b.status) || b.id - a.id).slice(0, 5)
})

function taskPct(t: DownloadTask) {
  if (!t.totalChapters) return 0
  return Math.min(100, Math.round(((t.doneChapters + t.failChapters) / t.totalChapters) * 100))
}

/** 健康度指标（可点击下钻） */
const health = computed(() => {
  const d = data.value
  if (!d) return []
  return [
    {
      key: 'sources', label: '书源健康', path: '/sources',
      value: d.sources.total ? `${Math.round((d.sources.verified / d.sources.total) * 100)}%` : '—',
      sub: `可用 ${d.sources.verified}${d.sources.abnormal ? ` · 异常 ${d.sources.abnormal}` : ''}`,
      // 校验率低于一半视为需要处理（重新校验/清理失效源），否则绿色
      tone: d.sources.total && d.sources.verified / d.sources.total < 0.5 ? 'warn' : 'ok',
    },
    {
      key: 'cache', label: '书架缓存', path: '/books',
      value: `${cachedPct.value}%`,
      sub: `${d.books.cachedChapters} / ${d.books.totalChapters} 章`,
      tone: cachedPct.value >= 90 ? 'ok' : 'muted',
    },
    {
      key: 'rules', label: '净化规则', path: '/rules',
      value: String(d.rules?.enabled ?? 0),
      sub: `共 ${d.rules?.total ?? 0} 条`,
      tone: 'muted',
    },
    {
      key: 'storage', label: '数据占用', path: '/files',
      value: fmtSize(d.storage?.bytes ?? 0),
      sub: `成品 ${fmtSize(d.exports.totalSize)}`,
      tone: 'muted',
    },
  ]
})

function go(path: string) {
  router.push(path)
}

async function load() {
  // 快速返回（<150ms）时遮罩一闪反而刺眼：短延迟内完成就不显示 loading
  const showTimer = window.setTimeout(() => { loading.value = true }, 150)
  try {
    const [ov, tasks] = await Promise.all([
      api.overview(),
      api.listTasks(20).catch(() => [] as DownloadTask[]),
    ])
    data.value = ov
    recentTasks.value = tasks ?? []
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    window.clearTimeout(showTimer)
    loading.value = false
  }
}

onMounted(load)
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
          <span class="hstat-num" :class="{ pulsing: activeCount > 0 }">{{ activeCount }}</span>
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

    <!-- 双栏：下载动态 + 导出分布 -->
    <section class="panels">
      <div class="panel">
        <div class="panel-head">
          <span class="panel-title">下载动态</span>
          <button class="panel-more" @click="go('/tasks')">全部任务 →</button>
        </div>
        <div v-if="taskPreview.length" class="task-mini-list">
          <button v-for="t in taskPreview" :key="t.id" class="task-mini" @click="go('/tasks')">
            <span class="tm-dot" :class="t.status" />
            <span class="tm-name" :title="t.bookName">{{ t.bookName }}</span>
            <span class="tm-bar"><span class="tm-bar-fill" :style="{ width: taskPct(t) + '%' }" /></span>
            <span class="tm-pct">{{ taskPct(t) }}%</span>
            <StatusPill :status="t.status" />
          </button>
        </div>
        <div v-else class="panel-empty">暂无任务 · 去<button class="link" @click="go('/search')">搜索</button>找一本开始吧</div>
      </div>

      <div class="panel">
        <div class="panel-head">
          <span class="panel-title">导出分布</span>
          <button class="panel-more" @click="go('/files')">全部文件 →</button>
        </div>
        <div v-if="formatBars.length" class="fmt-bars">
          <button v-for="f in formatBars" :key="f.format" class="fmt-bar-row" @click="go('/files')">
            <span class="fb-tag" :style="{ background: f.color }">{{ f.format.toUpperCase() }}</span>
            <span class="fb-track"><span class="fb-fill" :style="{ width: f.pct + '%', background: f.color }" /></span>
            <span class="fb-count">{{ f.count }}</span>
          </button>
          <div class="fmt-foot muted">{{ data?.exports.count ?? 0 }} 个文件 · 共 {{ fmtSize(data?.exports.totalSize ?? 0) }}</div>
        </div>
        <div v-else class="panel-empty">还没有导出成品 · 下载完成会自动生成</div>
      </div>
    </section>

    <section class="launchers">
      <button class="launcher" @click="go('/search')">
        <span class="l-icon">🔍</span>
        <span class="l-body">
          <span class="l-title">搜索新书</span>
          <span class="l-desc">聚合 {{ data?.sources.enabled ?? 0 }} 个启用源，一键比源下载</span>
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

    <!-- 健康度一行 -->
    <section v-if="data" class="health-row">
      <button v-for="h in health" :key="h.key" class="health-item" @click="go(h.path)">
        <span class="h-label">{{ h.label }}</span>
        <span class="h-value" :class="h.tone">{{ h.value }}</span>
        <span class="h-sub">{{ h.sub }}</span>
      </button>
    </section>
  </div>
</template>

<style scoped>
.home { padding-top: 32px; }

/* Hero：墨绿渐变 + 玻璃质感统计 */
.hero {
  background:
    radial-gradient(ellipse 90% 120% at 90% -20%, rgba(126, 190, 164, 0.35), transparent),
    linear-gradient(135deg, #1c362e 0%, #2f6f5e 65%, #3d8a75 100%);
  border-radius: 22px;
  padding: 32px 36px 28px;
  color: #f5f2ea;
  box-shadow: 0 10px 32px rgba(28, 54, 46, 0.28);
  margin-bottom: 20px;
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
  transition: background 0.2s ease, transform 0.2s ease, border-color 0.2s ease;
  backdrop-filter: blur(4px);
}
.hstat:hover {
  background: rgba(255, 255, 255, 0.18);
  border-color: rgba(255, 255, 255, 0.28);
  transform: translateY(-2px);
}
.hstat:active { transform: translateY(0) scale(0.98); }
.hstat-num {
  font-size: 26px;
  font-weight: 700;
  color: #fff;
  line-height: 1.1;
  font-variant-numeric: tabular-nums;
}
/* 有进行中任务时数字呼吸，提示"正在干活" */
.hstat-num.pulsing { animation: hstat-pulse 2s ease-in-out infinite; }
@keyframes hstat-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}
.hstat-label { font-size: 12.5px; color: rgba(245, 242, 234, 0.9); }
.hstat-sub { font-size: 11px; color: rgba(245, 242, 234, 0.55); }

/* ---- 双栏面板 ---- */
.panels {
  display: grid;
  grid-template-columns: 1.15fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
}
.panel {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  box-shadow: var(--bs-shadow);
  padding: 16px 18px;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.panel-title { font-size: 14px; font-weight: 600; color: var(--bs-text); }
.panel-more {
  border: none;
  background: none;
  color: var(--bs-text-sub);
  font-size: 12px;
  cursor: pointer;
  padding: 2px 4px;
  border-radius: 6px;
  transition: color 0.15s ease, background 0.15s ease;
}
.panel-more:hover { color: var(--bs-primary); background: var(--bs-primary-bg); }
.panel-empty {
  color: var(--bs-text-sub);
  font-size: 12.5px;
  padding: 18px 0 14px;
  text-align: center;
}
.panel-empty .link {
  border: none;
  background: none;
  color: var(--bs-primary);
  cursor: pointer;
  font-size: 12.5px;
  padding: 0 2px;
}
.panel-empty .link:hover { text-decoration: underline; }

/* 任务迷你列表 */
.task-mini-list { display: flex; flex-direction: column; gap: 2px; }
.task-mini {
  display: flex;
  align-items: center;
  gap: 9px;
  border: none;
  background: none;
  padding: 7px 8px;
  border-radius: 9px;
  cursor: pointer;
  text-align: left;
  transition: background 0.15s ease;
  min-width: 0;
}
.task-mini:hover { background: var(--el-fill-color-light); }
.tm-dot { width: 7px; height: 7px; border-radius: 50%; flex-shrink: 0; background: var(--bs-text-sub); }
.tm-dot.running { background: var(--bs-primary); animation: sp-pulse 1.4s ease infinite; }
.tm-dot.exporting { background: var(--bs-accent); animation: sp-pulse 1.4s ease infinite; }
.tm-dot.done { background: var(--bs-success); }
.tm-dot.failed { background: var(--bs-danger); }
.tm-dot.paused { background: var(--bs-accent); }
@keyframes sp-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
.tm-name {
  font-size: 13px;
  color: var(--bs-text);
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex-shrink: 0;
}
.tm-bar {
  flex: 1;
  min-width: 40px;
  height: 4px;
  border-radius: 999px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.tm-bar-fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--bs-primary);
  transition: width 0.4s ease;
}
.tm-pct {
  font-size: 11.5px;
  color: var(--bs-text-sub);
  font-variant-numeric: tabular-nums;
  width: 34px;
  text-align: right;
  flex-shrink: 0;
}
.task-mini :deep(.status-pill) { font-size: 11px; padding: 2px 8px; flex-shrink: 0; }

/* 格式分布条 */
.fmt-bars { display: flex; flex-direction: column; gap: 8px; }
.fmt-bar-row {
  display: flex;
  align-items: center;
  gap: 10px;
  border: none;
  background: none;
  padding: 3px 6px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s ease;
}
.fmt-bar-row:hover { background: var(--el-fill-color-light); }
.fb-tag {
  width: 46px;
  flex-shrink: 0;
  text-align: center;
  font-size: 9.5px;
  font-weight: 700;
  color: #fff;
  border-radius: 5px;
  padding: 2px 0;
  letter-spacing: 0.3px;
}
.fb-track {
  flex: 1;
  height: 7px;
  border-radius: 999px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.fb-fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  transition: width 0.4s ease;
}
.fb-count {
  font-size: 12px;
  color: var(--bs-text-sub);
  font-variant-numeric: tabular-nums;
  width: 26px;
  text-align: right;
  flex-shrink: 0;
}
.fmt-foot { font-size: 11.5px; margin-top: 6px; text-align: right; }

/* 快捷启动器 */
.launchers {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
  margin-bottom: 16px;
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
  transition: transform 0.2s ease, box-shadow 0.2s ease, border-color 0.2s ease;
}
.launcher:hover {
  transform: translateY(-3px);
  border-color: var(--bs-primary-light);
  box-shadow: var(--bs-shadow-hover);
}
.launcher:active { transform: translateY(-1px) scale(0.99); }
.launcher:hover .l-arrow { transform: translateX(4px); color: var(--bs-primary); }
/* 图标容器化：emoji 收进主题色浅底圆角块，替代裸放 */
.l-icon {
  width: 44px;
  height: 44px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
  background: var(--bs-primary-bg);
  border-radius: 12px;
}
.l-body { flex: 1; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.l-title { font-size: 15px; font-weight: 600; color: var(--bs-text); }
.l-desc { font-size: 12.5px; color: var(--bs-text-sub); }
.l-arrow { color: var(--bs-text-sub); transition: transform 0.2s ease, color 0.2s ease; }

/* 健康度一行 */
.health-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.health-item {
  display: flex;
  flex-direction: column;
  gap: 3px;
  align-items: flex-start;
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius-sm);
  padding: 12px 16px;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.18s ease, transform 0.18s ease, box-shadow 0.18s ease;
}
.health-item:hover {
  border-color: var(--bs-primary-light);
  transform: translateY(-1px);
  box-shadow: var(--bs-shadow);
}
.h-label { font-size: 11.5px; color: var(--bs-text-sub); }
.h-value {
  font-size: 17px;
  font-weight: 700;
  color: var(--bs-text);
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}
.h-value.ok { color: var(--bs-success); }
.h-value.warn { color: var(--bs-accent); }
.h-sub { font-size: 11px; color: var(--bs-text-sub); }

@media (max-width: 900px) {
  .panels { grid-template-columns: 1fr; }
  .health-row { grid-template-columns: repeat(2, 1fr); }
}
@media (max-width: 760px) {
  .hero-stats { grid-template-columns: repeat(2, 1fr); }
  .launchers { grid-template-columns: 1fr; }
}
</style>

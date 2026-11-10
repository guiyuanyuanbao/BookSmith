<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, VideoPause, CircleCheckFilled } from '@element-plus/icons-vue'
import { api, coverProxy, subscribeEvents, type SearchJobState, type SearchOrigin, type SearchResult, type TocProbeResult } from '@/api'
import { useSettingsStore } from '@/stores/settings'
import { useTasksStore } from '@/stores/tasks'

const settings = useSettingsStore()
const tasksStore = useTasksStore()
const route = useRoute()
const router = useRouter()

const keyword = ref('')
// 从书架"下载"/浮窗"换源"跳转进来时直接进结果态，避免先渲染 hero 再翻转的两阶段视觉
const started = ref(!!(route.query.pick || route.query.reswitch))
const history = ref<string[]>([])
const showErrors = ref(false)
const loading = ref(false)
const job = ref<SearchJobState | null>(null)
let searchId = ''
let pollTimer: number | null = null
let closeSSE: (() => void) | null = null

async function poll() {
  if (!searchId) return
  try {
    const st = await api.searchState(searchId)
    job.value = st
    if (!st.running) {
      stopPolling()
      loading.value = false
      // 换源重下跳转：搜索完成后自动打开换源对话框
      if (pendingReswitch.value) {
        const { name, exclude } = pendingReswitch.value
        pendingReswitch.value = null
        const hit = (st.items ?? []).find((i) => i.name === name)
        if (hit) openPick(hit, exclude || undefined)
        else ElMessage.warning(`未搜到《${name}》，请手动搜索后选源`)
      }
      // 书架"下载"跳转：搜索完成后自动打开选源对话框
      if (pendingPick.value) {
        const name = pendingPick.value
        pendingPick.value = null
        const hit = (st.items ?? []).find((i) => i.name === name)
        if (hit) openPick(hit)
        else ElMessage.warning(`未搜到《${name}》，请换个关键词试试`)
      }
    }
  } catch {
    stopPolling()
    loading.value = false
  }
}

function startPolling() {
  stopPolling()
  pollTimer = window.setInterval(poll, 1000)
}

function stopPolling() {
  if (pollTimer) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

async function loadHistory() {
  try {
    history.value = (await api.getSearchHistory()) ?? []
  } catch { /* 忽略 */ }
}

async function doSearch() {
  const key = keyword.value.trim()
  if (!key) {
    ElMessage.warning('请输入书名或作者')
    return
  }
  started.value = true
  loading.value = true
  job.value = null
  closeSSE = subscribeEvents({
    onSearchProgress: (d) => {
      if (d.searchId === searchId && job.value) {
        job.value.searched = d.searched
        job.value.total = d.total
      }
    },
    onSearchDone: () => {
      if (searchId) poll()
    },
    onTaskProgress: (d) => {
      tasksStore.applyEvent(d)
    },
    onTaskDone: (d) => {
      tasksStore.applyEvent(d)
    },
  })
  try {
    const r = await api.searchStart(key)
    searchId = r.searchId
    api.addSearchHistory(key).then(() => loadHistory()).catch(() => {})
    startPolling()
    await poll()
  } catch (e: any) {
    ElMessage.error(e.message)
    loading.value = false
    closeSSE?.()
    closeSSE = null
  }
}

async function cancelSearch() {
  if (!searchId) return
  try {
    await api.searchCancel(searchId)
    ElMessage.info('已取消搜索')
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    stopPolling()
    loading.value = false
  }
}

const page = ref(1)
const pageSize = 20
const pagedItems = () => {
  const items = job.value?.items ?? []
  return items.slice((page.value - 1) * pageSize, page.value * pageSize)
}

// ---------- 来源对比与选源下载 ----------

const pickDlg = ref(false)
const pickItem = ref<SearchResult | null>(null)
// 换源重下时预排除刚失败的源（点击它给提示）
const excludeOrigin = ref<string | null>(null)
// 换源对话框打开时记住任务名，提交后清理对应 switching 标记由 submitDownload 统一处理

// ---------- 目录探测：比源对话框内按需探测源质量 ----------
const probeMap = ref<Record<string, TocProbeResult>>({})
const probing = ref(false)
const probeTotal = ref(0)
const probeDone = ref(0)

async function onPickDialogOpen() {
  probing.value = false
  probeTotal.value = 0
  probeDone.value = 0
}

// 探测源目录质量：按响应速度分批（每批 12 个，最多 3 批 36 个源）。
// 若前一批全部失败（快源多为死源），自动继续探测下一批，直到探到健康源或达上限。
async function startProbe() {
  if (probing.value || !pickItem.value) return
  const all = [...pickItem.value.origins].sort((a, b) => (a.respondTime || 9999) - (b.respondTime || 9999))
  if (!all.length) return
  probing.value = true
  const BATCH = 6
  const MAX_TOTAL = Math.min(36, all.length)
  let cursor = 0
  try {
    while (cursor < MAX_TOTAL) {
      const batch = all.slice(cursor, cursor + BATCH).map((o) => ({ sourceUrl: o.sourceUrl, bookUrl: o.bookUrl }))
      probeTotal.value = Math.min(cursor + batch.length, MAX_TOTAL)
      // 逐源请求（每源独立返回，失败不影响其他），保证 UI 持续更新
      for (const src of batch) {
        try {
          const r = await api.probeToc(src.sourceUrl, src.bookUrl)
          probeMap.value[src.sourceUrl] = r
        } catch { /* 单源失败不阻塞 */ }
        probeDone.value++
      }
      cursor += batch.length
      const okCount = Object.values(probeMap.value).filter((x) => x.ok).length
      if (okCount >= 3) break
    }
  } catch (e: any) {
    ElMessage.error('探测失败：' + e.message)
  } finally {
    probing.value = false
  }
}

// 状态徽章：完结/连载/疑似缺章（与同书其他已探测源相对比较）
function probeBadge(sourceUrl: string): { label: string; type: 'success' | 'warning' | 'info' } {
  const res = probeMap.value[sourceUrl]
  if (!res || !res.ok) return { label: '', type: 'info' }
  // 与同书其他源对比：章节数 >= 最高已探测源的 90% 且有末章 → 完结倾向；显著偏少 → 疑似缺章
  const totals = Object.values(probeMap.value).filter((r) => r.ok).map((r) => r.total)
  const maxTotal = Math.max(...totals, 1)
  const origin = pickItem.value?.origins.find((o) => o.sourceUrl === sourceUrl)
  const kindText = (origin?.sourceName ? '' : '') + (pickItem.value?.origins.find((o) => o.sourceUrl === sourceUrl)?.latestChapter || '')
  const lastMentioned = res.lastChapters.length > 0
  if (res.total >= maxTotal * 0.9 && lastMentioned) return { label: '· 完整', type: 'success' }
  if (res.total < maxTotal * 0.6) return { label: '· 疑似缺章', type: 'warning' }
  return { label: '· 偏少', type: 'info' }
}
function openPick(item: SearchResult, excludeOriginUrl?: string) {
  pickItem.value = item
  excludeOrigin.value = excludeOriginUrl ?? null
  // 换源场景预选第一个可用源，省一次点击
  pickDlg.value = true
  const first = item.origins.find((o) => o.sourceUrl !== excludeOriginUrl)
  if (first) pickSource(first)
}

// 在比源对话框中选中某来源
const pickedOrigin = ref<SearchOrigin | null>(null)
function pickSource(o: SearchOrigin) {
  pickedOrigin.value = o
  dlSource.value = o
  dlName.value = pickItem.value?.name ?? o.name
  dlAuthor.value = pickItem.value?.author ?? o.author
  dlFormats.value = [...settings.defaultFormats]
  dlConcurrency.value = settings.concurrency
  dlRange.value = [1, 0]
}

function fmtMs(ms?: number) {
  if (!ms) return '—'
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

// 选源下载对话框状态
const dlSource = ref<SearchOrigin | null>(null)
const dlName = ref('')
const dlAuthor = ref('')
const dlFormats = ref<string[]>(['txt', 'epub'])
const dlRange = ref<[number, number]>([1, 0])
const dlConcurrency = ref(6)
const dlSubmitting = ref(false)

async function submitDownload() {
  const o = dlSource.value
  if (!o) return
  if (dlFormats.value.length === 0) {
    ElMessage.warning('请选择至少一种导出格式')
    return
  }
  dlSubmitting.value = true
  try {
    // 自动换源候选：同书其他源按响应速度排序（排除当前选定源），目录失败时后端依次尝试
    const candidates = [...(pickItem.value?.origins ?? [])]
      .sort((a, b) => (a.respondTime || 9999) - (b.respondTime || 9999))
      .filter((x) => x.sourceUrl !== o.sourceUrl)
      .slice(0, 5)
      .map((x) => ({ sourceUrl: x.sourceUrl, sourceName: x.sourceName, bookUrl: x.bookUrl }))
    const r = await api.createTask({
      bookName: dlName.value,
      author: dlAuthor.value,
      bookUrl: o.bookUrl,
      sourceUrl: o.sourceUrl,
      coverUrl: o.coverUrl || '',
      intro: o.intro || '',
      formats: dlFormats.value,
      rangeStart: Math.max(0, dlRange.value[0] - 1),
      rangeEnd: dlRange.value[1] > 0 ? dlRange.value[1] - 1 : -1,
      concurrency: dlConcurrency.value,
      candidateSources: candidates,
    })
    tasksStore.add(r.taskId, {
      name: dlName.value, done: 0, total: 0, status: 'running', author: dlAuthor.value,
      originName: o.sourceName, origin: o.sourceUrl,
    })
    ElMessage.success(`《${dlName.value}》已加入书架，任务 #${r.taskId} 后台下载中`)
    pickDlg.value = false
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    dlSubmitting.value = false
  }
}

// ---------- 跨页面换源重下：从其他页面通过 query 跳回搜索页 ----------

/** 打开换源对话框（预排除失败源并预选下一个可用源）；搜索结果不在缓存时按书名重新搜索 */
function reswitchFromQuery(name: string, author: string, exclude: string, failedTaskId?: number) {
  // 旧失败任务移出浮窗，避免重下后同名任务堆积
  if (failedTaskId) tasksStore.remove(failedTaskId)
  const hit = (job.value?.items ?? []).find((i) => i.name === name)
  if (hit) {
    openPick(hit, exclude || undefined)
    return
  }
  // 本页没有该书结果（刚进入页面）：先搜索，完成后自动打开换源对话框
  keyword.value = name
  pendingReswitch.value = { name, exclude }
  doSearch()
}

const pendingReswitch = ref<{ name: string; exclude: string } | null>(null)
const pendingPick = ref<string | null>(null)

// 结果卡片上的小状态标签
function dlTagType(s: string) {
  return s === 'failed' ? 'danger' : s === 'paused' || s === 'canceled' ? 'info' : s === 'done' ? 'success' : 'primary'
}
function dlTagText(s: string) {
  switch (s) {
    case 'running': return '下载中'
    case 'exporting': return '导出中'
    case 'paused': return '已暂停'
    case 'failed': return '失败'
    case 'canceled': return '已取消'
    default: return s
  }
}

// 多源封面 fallback：item.coverUrl 优先，其次各 origin 的封面
const triedCovers = new Set<string>()
function bestCover(item: SearchResult): string | null {
  if (item.coverUrl) return item.coverUrl
  for (const o of item.origins) {
    if (o.coverUrl && !triedCovers.has(o.coverUrl)) return o.coverUrl
  }
  return null
}
// 封面加载失败：标记已试，换下一个源的封面
function coverFallback(item: SearchResult, ev: Event) {
  const img = ev.target as HTMLImageElement
  const cur = img.src
  triedCovers.add(cur)
  const next = item.origins.map((o) => o.coverUrl).find((u) => u && !triedCovers.has(u))
  if (next) {
    img.src = coverProxy(next)
  } else {
    img.style.display = 'none'
  }
}

// 跨页面换源重下：其他页面浮窗跳转过来（带 reswitch query）。
// 书架"下载"跳转（带 pick query）：自动搜该书并打开选源对话框（预选最快源）。
// 用 watch 而非仅 onMounted：已在搜索页时 router.push 同路径不会重新挂载组件。
// pick/reswitch 只处理一次：URL query 保留（避免二次导航打断过渡），用标记防重复触发
let handledQuery = ''
watch(
  () => route.query.reswitch,
  (v) => {
    if (v && route.query.name) {
      const sig = `r:${v}:${route.query.name}`
      if (handledQuery === sig) return
      handledQuery = sig
      started.value = true
      reswitchFromQuery(
        String(route.query.name),
        String(route.query.author ?? ''),
        String(route.query.exclude ?? ''),
        Number(v),
      )
    }
  },
  { immediate: true },
)

// 书架"下载"进入：搜书 + 选源下载
watch(
  () => route.query.pick,
  (v) => {
    if (v) {
      const sig = `p:${v}`
      if (handledQuery === sig) return
      handledQuery = sig
      started.value = true
      const name = String(v)
      const hit = (job.value?.items ?? []).find((i) => i.name === name)
      if (hit) {
        openPick(hit)
      } else {
        keyword.value = name
        pendingPick.value = name
        doSearch()
      }
    }
  },
  { immediate: true },
)

onMounted(() => {
  loadHistory()
})

onBeforeUnmount(() => {
  stopPolling()
  closeSSE?.()
})
</script>

<template>
  <div :class="['page', !started ? 'page-hero' : '']">
    <template v-if="!started">
      <!-- Hero：居中搜索 -->
      <div class="hero">
        <div class="hero-logo">📚</div>
        <h1 class="hero-title">BookSmith</h1>
        <p class="hero-tagline">跨书源聚合搜索 · 自动去重 · 一键下载制作电子书</p>
        <div class="hero-search">
          <el-input
            v-model="keyword"
            placeholder="输入书名或作者，回车搜索"
            size="large"
            clearable
            @keyup.enter="doSearch"
          >
            <template #append>
              <el-button :icon="Search" :loading="loading" @click="doSearch">搜索</el-button>
            </template>
          </el-input>
        </div>
        <div class="hero-hints">
          <template v-if="history.length">
            <span class="muted">最近：</span>
            <el-link v-for="k in history.slice(0, 5)" :key="k" type="primary" style="margin-right: 12px" @click="keyword = k; doSearch()">{{ k }}</el-link>
            <el-link type="info" @click="api.clearSearchHistory().then(() => loadHistory())">清空</el-link>
          </template>
          <template v-else>
            <span class="muted">热门：</span>
            <el-link v-for="k in ['斗破苍穹','凡人修仙传','诡秘之主']" :key="k" type="primary" style="margin-right: 12px" @click="keyword = k; doSearch()">{{ k }}</el-link>
          </template>
        </div>
      </div>
    </template>
    <template v-else>
    <h1 class="page-title">搜索结果</h1>
    <p class="page-sub">跨书源并发搜索，同名书自动聚合去重；结果实时流式返回，可对比各来源差异后选源下载</p>

    <div class="toolbar">
      <el-input
        v-model="keyword"
        placeholder="输入书名或作者，如：斗破苍穹"
        size="large"
        style="max-width: 480px"
        clearable
        @keyup.enter="doSearch"
      >
        <template #append>
          <el-button :icon="Search" :loading="loading" @click="doSearch">搜索</el-button>
        </template>
      </el-input>
      <el-button v-if="loading" :icon="VideoPause" @click="cancelSearch">取消</el-button>
      <div class="spacer" />
      <template v-if="job">
        <el-progress
          type="dashboard"
          :percentage="job.total ? Math.round((job.searched / job.total) * 100) : 0"
          :width="52"
          :stroke-width="6"
          :show-text="false"
          :status="loading ? undefined : 'success'"
          style="flex-shrink: 0"
        />
        <div class="muted" style="line-height: 1.5">
          {{ job.searched }}/{{ job.total }} 源 · 命中 {{ job.items.length }} 本 ·
          {{ (job.elapsedMs / 1000).toFixed(1) }}s
          <template v-if="job.errors?.length"> · 失败 {{ job.errors.length }}</template>
        </div>
      </template>
    </div>

    <div v-if="job && !loading && job.errors?.length" style="margin-bottom: 12px">
      <el-alert type="info" :closable="false" style="margin-bottom: 6px">
        <template #title>
          <span>{{ job.errors.length }} 个书源未响应（失效/需代理/网络受限属正常现象）</span>
          <el-button link type="primary" size="small" style="margin-left: 10px" @click="showErrors = !showErrors">
            {{ showErrors ? '收起' : '查看详情' }}
          </el-button>
        </template>
      </el-alert>
      <el-table v-if="showErrors" :data="job.errors" size="small" max-height="220" style="border-radius: 8px">
        <el-table-column prop="source" label="书源" min-width="140" show-overflow-tooltip />
        <el-table-column prop="err" label="原因" min-width="300" show-overflow-tooltip />
      </el-table>
    </div>

    <div v-for="item in pagedItems()" :key="item.name + item.author" class="book-card" style="margin-bottom: 12px">
      <div class="cover">
        <img
          v-if="bestCover(item)"
          :src="coverProxy(bestCover(item)!)"
          loading="lazy"
          @error="coverFallback(item, $event)"
        />
        <span v-else>书</span>
      </div>
      <div style="flex: 1; min-width: 0">
        <div style="display: flex; align-items: baseline; gap: 10px">
          <strong style="font-size: 16px">{{ item.name }}</strong>
          <span class="muted">{{ item.author || '未知作者' }}</span>
          <el-tag v-if="item.kind" size="small" type="info">{{ item.kind }}</el-tag>
        </div>
        <p class="intro-text" style="margin: 6px 0">{{ item.intro || '暂无简介' }}</p>
        <div class="muted" style="margin-bottom: 8px">
          最新：{{ item.origins[0]?.latestChapter || item.latestChapter || '—' }}
        </div>
        <div style="display: flex; flex-wrap: wrap; gap: 6px; align-items: center">
          <span class="muted">{{ item.origins.length }} 个来源：</span>
          <el-tag
            v-for="o in item.origins.slice(0, 6)"
            :key="o.sourceUrl"
            size="small"
            class="origin-tag"
            @click="openPick(item)"
          >
            {{ o.sourceName }}
          </el-tag>
          <el-tag v-if="item.origins.length > 6" size="small" type="info">
            +{{ item.origins.length - 6 }}
          </el-tag>
        </div>
      </div>
      <div style="display: flex; flex-direction: column; justify-content: center; gap: 8px">
        <template v-for="(dl, tid) in tasksStore.activeDownloads" :key="tid">
          <el-tag
            v-if="dl.name === item.name && dl.status !== 'done'"
            size="small"
            :type="dlTagType(dl.status)"
            style="cursor: pointer"
            @click="tasksStore.panelOpen = true; tasksStore.panelMin = false"
          >
            {{ dlTagText(dl.status) }} {{ dl.done }}/{{ dl.total || '…' }}
          </el-tag>
          <el-tag
            v-else-if="dl.name === item.name && dl.status === 'done'"
            size="small" type="success" style="cursor: pointer"
            @click="tasksStore.panelOpen = true; tasksStore.panelMin = false"
          >
            ✓ 已完成
          </el-tag>
        </template>
        <el-button v-if="!Object.values(tasksStore.activeDownloads).some((d) => d.name === item.name && !['done', 'failed', 'canceled'].includes(d.status))"
                   type="primary" @click="openPick(item)">下载</el-button>
      </div>
    </div>

    <div v-if="(job?.items.length ?? 0) > pageSize" style="display: flex; justify-content: center; margin-top: 8px">
      <el-pagination
        v-model:current-page="page"
        :page-size="pageSize"
        :total="job!.items.length"
        layout="prev, pager, next"
      />
    </div>

    <!-- 搜索历史：横排 chips，附在分页下方，不遮挡任何内容 -->
    <div v-if="started && history.length" class="history-bar">
      <span class="muted">最近：</span>
      <el-tag
        v-for="k in history.slice(0, 10)"
        :key="k"
        size="small"
        :class="{ 'history-chip': true, active: k === keyword }"
        @click="keyword = k; doSearch()"
      >{{ k }}</el-tag>
      <el-link type="info" :underline="false" style="font-size: 12px; margin-left: 4px" @click="api.clearSearchHistory().then(() => loadHistory())">清空</el-link>
    </div>

    <el-empty
      v-if="job && !loading && job.items.length === 0"
      :description="`全部 ${job.total} 个书源未搜到《${job.key}》相关结果`"
    >
      <div style="display: flex; gap: 10px; justify-content: center">
        <el-button @click="started = false; keyword = ''">重新搜索</el-button>
        <el-button type="primary" plain @click="$router.push('/sources')">检查书源健康</el-button>
      </div>
    </el-empty>
    </template>

    <!-- 比源 + 下载（合并为一个对话框，先选源再设下载） -->
    <el-dialog v-model="pickDlg" :title="`下载 · ${pickItem?.name ?? ''}`" width="860" @open="onPickDialogOpen">
      <div class="pick-section-title">
        ① 选择来源
        <span class="muted">（{{ pickItem?.origins.length }} 个来源，点击选中，默认按响应速度排序）</span>
        <el-button
          size="small" type="primary" plain style="margin-left: 12px"
          :loading="probing" @click="startProbe"
        >
          {{ probing ? '探测中…' : '探测目录质量' }}
        </el-button>
        <span v-if="probing" class="muted" style="margin-left: 8px">{{ probeDone }}/{{ probeTotal }}</span>
      </div>
      <el-table
        :data="pickItem?.origins"
        max-height="300"
        highlight-current-row
        :current-row-key="pickedOrigin?.sourceUrl"
        row-key="sourceUrl"
        :default-sort="{ prop: 'respondTime', order: 'ascending' }"
        @row-click="pickSource"
        style="cursor: pointer"
      >
        <el-table-column width="45">
          <template #default="{ row }">
            <el-icon v-if="pickedOrigin?.sourceUrl === row.sourceUrl" color="var(--bs-primary)" style="font-size: 18px">
              <CircleCheckFilled />
            </el-icon>
          </template>
        </el-table-column>
        <el-table-column prop="sourceName" label="来源" min-width="150" show-overflow-tooltip />
        <el-table-column label="章节数" width="110">
          <template #default="{ row }">
            <template v-if="probeMap[row.sourceUrl]">
              <el-tag v-if="probeMap[row.sourceUrl].ok" size="small" :type="probeBadge(row.sourceUrl).type">
                {{ probeMap[row.sourceUrl].total }} 章 {{ probeBadge(row.sourceUrl).label }}
              </el-tag>
              <el-tag v-else size="small" type="danger">目录不可用</el-tag>
            </template>
            <span v-else class="muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="最新章节" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ row.latestChapter || '—' }}</template>
        </el-table-column>
        <el-table-column label="字数" width="85">
          <template #default="{ row }">{{ row.wordCount || '—' }}</template>
        </el-table-column>
        <el-table-column label="响应" width="80" sortable prop="respondTime">
          <template #default="{ row }">{{ fmtMs(row.respondTime) }}</template>
        </el-table-column>
      </el-table>

      <!-- 目录预览面板（选中已探测的源时显示） -->
      <div v-if="pickedOrigin && probeMap[pickedOrigin.sourceUrl]?.ok" class="toc-preview">
        <div class="toc-preview-title">
          目录预览 · {{ pickedOrigin.sourceName }}
          <span class="muted" style="font-size: 12px; margin-left: 8px">
            {{ probeMap[pickedOrigin.sourceUrl].total }} 章<template v-if="probeMap[pickedOrigin.sourceUrl].volumeCount"> · {{ probeMap[pickedOrigin.sourceUrl].volumeCount }} 卷</template>
          </span>
        </div>
        <div class="toc-preview-body">
          <div v-for="t in probeMap[pickedOrigin.sourceUrl].firstChapters" :key="t" class="toc-line">{{ t }}</div>
          <div v-if="probeMap[pickedOrigin.sourceUrl].lastChapters.length" class="toc-ellipsis">⋯⋯</div>
          <div v-for="t in probeMap[pickedOrigin.sourceUrl].lastChapters" :key="'l' + t" class="toc-line">{{ t }}</div>
        </div>
      </div>

      <div class="pick-section-title" style="margin-top: 18px">
        ② 下载设置
        <span v-if="!pickedOrigin" class="muted">（请先在上方选择一个来源）</span>
        <span v-else class="muted">（已选：{{ pickedOrigin.sourceName }}）</span>
      </div>
      <el-form v-if="pickedOrigin" label-width="100" style="margin-top: 6px">
        <el-form-item label="导出格式">
          <el-checkbox-group v-model="dlFormats">
            <el-checkbox value="txt">TXT</el-checkbox>
            <el-checkbox value="epub">EPUB</el-checkbox>
            <el-checkbox value="pdf">PDF</el-checkbox>
            <el-checkbox value="html">HTML</el-checkbox>
            <el-checkbox value="md">Markdown</el-checkbox>
          </el-checkbox-group>
        </el-form-item>
        <el-form-item label="章节范围">
          <el-input-number v-model="dlRange[0]" :min="1" />
          <span style="margin: 0 8px">至</span>
          <el-input-number v-model="dlRange[1]" :min="0" />
          <span class="muted" style="margin-left: 8px">结束填 0 = 到最后一章（默认全本）</span>
        </el-form-item>
        <el-form-item label="下载并发">
          <el-slider v-model="dlConcurrency" :min="1" :max="16" show-input style="max-width: 420px" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="pickDlg = false">取消</el-button>
        <el-button type="primary" :disabled="!pickedOrigin" :loading="dlSubmitting" @click="submitDownload">
          开始下载
        </el-button>
      </template>
    </el-dialog>

  </div>
</template>

<style scoped>
.origin-tag {
  cursor: pointer;
}
.origin-tag:hover {
  opacity: 0.8;
}
.page-hero {
  display: flex;
  flex-direction: column;
  justify-content: center;
  min-height: calc(100vh - 40px);
  padding-top: 0;
}
.hero {
  text-align: center;
  max-width: 640px;
  margin: 0 auto;
  width: 100%;
}
.hero-logo { font-size: 56px; margin-bottom: 8px; }
.hero-title {
  font-family: 'Noto Serif SC', serif;
  font-size: 40px;
  font-weight: 700;
  color: var(--bs-text);
  margin: 0 0 10px;
  letter-spacing: 2px;
}
.hero-tagline {
  color: var(--bs-text-sub);
  font-size: 15px;
  margin: 0 0 32px;
}
.hero-search { margin-bottom: 16px; }
.pick-section-title { font-size: 14px; font-weight: 600; margin-bottom: 8px; }
/* 固定占位高度：历史异步载入不引起布局跳动 */
.hero-hints {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 32px;
}

/* 目录预览面板 */
.toc-preview {
  margin-top: 12px;
  background: var(--el-fill-color-light, #f4f1ea);
  border-radius: 10px;
  padding: 10px 14px;
}
.toc-preview-title { font-size: 13px; font-weight: 600; color: var(--bs-text); margin-bottom: 6px; }
.toc-preview-body { max-height: 140px; overflow-y: auto; }
.toc-line {
  font-size: 12.5px;
  color: var(--bs-text-sub);
  padding: 2px 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.toc-ellipsis { color: var(--bs-text-sub); font-size: 12px; padding: 2px 0; text-align: center; }

/* 搜索历史 chips：横向排在结果末尾 */
.history-bar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 20px;
  padding-top: 14px;
  border-top: 1px dashed var(--bs-border);
}
.history-chip { cursor: pointer; color: var(--bs-text-sub); }
.history-chip:hover { color: var(--bs-primary); border-color: var(--bs-primary); }
.history-chip.active { color: var(--bs-primary); border-color: var(--bs-primary); font-weight: 600; }
</style>

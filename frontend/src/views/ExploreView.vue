<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Compass } from '@element-plus/icons-vue'
import { api, coverProxy, type ExploreBook, type ExploreSourceInfo } from '@/api'
import CoverThumb from '@/components/CoverThumb.vue'

const router = useRouter()

// 源+分类树
const sources = ref<ExploreSourceInfo[]>([])
const loadingSources = ref(false)
const sourcesErr = ref('')
// 分组筛选（源维度）
const groupFilter = ref<string[]>([])
// 当前选中：源 URL 与分类 URL（一一对应）
const activeSource = ref('')
const activeKind = ref('')
const activeKindTitle = ref('')
// 分类展开的源（只有展开的源显示分类列表；换源自动切展开）
const expandedSource = ref('')

// 书籍网格
const books = ref<ExploreBook[]>([])
const loadingBooks = ref(false)
const exploreErr = ref('')
const page = ref(1)
let exploreId = ''
let pollTimer: number | null = null
let pollFallbackTimer: number | null = null
// 本次加载是否为追加模式（pollOnce 落地结果时按此决定覆盖还是合并）
let pendingAppend = false
// 本轮轮询所属 job id 快照：pollOnce 回来时 exploreId 已变（新一轮 loadBooks 已起）则丢弃结果
let pollingJobId = ''
// pollOnce 互斥：interval 与 3s 兜底并发时只跑一个
let polling = false

const allGroups = computed(() => {
  const set = new Set<string>()
  for (const s of sources.value) {
    for (const g of (s.group || '').split(',').map((x) => x.trim())) {
      if (g) set.add(g)
    }
  }
  return [...set].sort()
})

const visibleSources = computed(() => {
  if (!groupFilter.value.length) return sources.value
  return sources.value.filter((s) =>
    (s.group || '').split(',').some((g) => groupFilter.value.includes(g.trim())),
  )
})

async function loadSources() {
  loadingSources.value = true
  sourcesErr.value = ''
  try {
    const r = await api.exploreSources()
    sources.value = r.sources ?? []
  } catch (e: any) {
    sourcesErr.value = e.message || '加载失败'
  } finally {
    loadingSources.value = false
  }
}

function stopPoll() {
  if (pollTimer !== null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
  if (pollFallbackTimer !== null) {
    clearTimeout(pollFallbackTimer)
    pollFallbackTimer = null
  }
}

function pickSource(sourceUrl: string) {
  // 已选中 → 再次点击 = 收起/展开分类（不重新加载）
  if (activeSource.value === sourceUrl) {
    expandedSource.value = expandedSource.value === sourceUrl ? '' : sourceUrl
    return
  }
  const s = sources.value.find((x) => x.sourceUrl === sourceUrl)
  if (!s || !s.kinds.length) return
  activeSource.value = sourceUrl
  expandedSource.value = sourceUrl // 切源自动展开新源的分类
  // 默认选第一个分类
  activeKind.value = s.kinds[0].url
  activeKindTitle.value = s.kinds[0].title
  books.value = []
  exploreErr.value = ''
  page.value = 1
  pendingAppend = false
  loadBooks(1, false)
}

function pickKind(kindUrl: string, kindTitle: string) {
  if (activeKind.value === kindUrl && books.value.length) return
  activeKind.value = kindUrl
  activeKindTitle.value = kindTitle
  books.value = []
  exploreErr.value = ''
  page.value = 1
  pendingAppend = false
  loadBooks(1, false)
}

async function pollOnce() {
  if (!exploreId || polling) return
  // exploreId 已变（新一轮 loadBooks 已起）→ 本轮结果作废
  if (pollingJobId !== exploreId) return
  polling = true
  const jobSnapshot = exploreId
  try {
    const r = await api.exploreState(jobSnapshot)
    if (r.running) return
    // 结果落地前再校验：期间用户可能已切分类/切源（exploreId 被新 job 覆盖）
    if (pollingJobId !== exploreId) return
    stopPoll()
    loadingBooks.value = false
    if (r.err) {
      // 追加加载失败不清空已有内容，但在按钮旁提示（否则用户无限静默失败）
      if (!pendingAppend || !books.value.length) {
        exploreErr.value = r.err
      } else {
        ElMessage.warning(`${r.err.slice(0, 60)}（追加失败，已有内容保留）`)
      }
      return
    }
    if (pendingAppend) {
      // 追加：按 bookUrl 去重后接在已有书之后（部分源第 2 页会重复末尾条目）
      const seen = new Set(books.value.map((b) => b.bookUrl))
      for (const b of r.books ?? []) {
        if (!seen.has(b.bookUrl)) books.value.push(b)
      }
      if (!books.value.length) {
        exploreErr.value = '该分类没有更多内容了'
      }
    } else {
      books.value = r.books ?? []
      if (r.errors?.length && (r.books ?? []).length === 0) {
        exploreErr.value = r.errors.slice(0, 3).join('; ')
      }
    }
    pendingAppend = false
  } catch {
    stopPoll()
    loadingBooks.value = false
    pendingAppend = false
  } finally {
    polling = false
  }
}

async function loadBooks(p: number, append: boolean) {
  if (!activeSource.value || !activeKind.value) return
  // 同步段先停旧轮询：await exploreStart 期间旧 pollOnce 不再污染新状态
  stopPoll()
  loadingBooks.value = true
  pendingAppend = append
  if (!append) exploreErr.value = ''
  try {
    const r = await api.exploreStart({
      sourceUrls: [activeSource.value],
      kinds: [activeKind.value],
      page: p,
    })
    exploreId = r.exploreId
    pollingJobId = r.exploreId
    page.value = p
    if (!append) books.value = []
    pollTimer = window.setInterval(() => pollOnce(), 1200)
    // 兜底：3s 后还没结果就主动 poll 一次（句柄被 stopPoll 管理，不会与 interval 叠加）
    pollFallbackTimer = window.setTimeout(() => { if (loadingBooks.value) pollOnce() }, 3000)
  } catch (e: any) {
    loadingBooks.value = false
    exploreErr.value = e.message
  }
}

function loadMore() {
  loadBooks(page.value + 1, true)
}

/** 点击书 → 跳搜索页预填书名选源下载 */
function searchBook(b: ExploreBook) {
  router.push({ path: '/search', query: { pick: b.name, author: b.author || '' } })
}

function coverOf(b: ExploreBook): string | null {
  return b.coverUrl ? coverProxy(b.coverUrl) : null
}

function hideCover(ev: Event) {
  ;(ev.target as HTMLImageElement).style.visibility = 'hidden'
}

loadSources()
onBeforeUnmount(stopPoll)
</script>

<template>
  <div class="page">
    <h1 class="page-title">发现</h1>
    <p class="page-sub">按书源浏览分类榜单与推荐页（exploreUrl），聚合多源发现内容；点击书籍跳转搜索下载</p>

    <div style="display: flex; gap: 18px; align-items: flex-start">
      <!-- 左：源 + 分类树 -->
      <div class="explore-side">
        <div style="margin-bottom: 10px">
          <el-select v-model="groupFilter" multiple filterable clearable collapse-tags placeholder="按分组筛选源" style="width: 100%" size="small">
            <el-option v-for="g in allGroups" :key="g" :value="g" :label="g" />
          </el-select>
        </div>
        <div v-if="loadingSources" style="text-align: center; padding: 24px 0">
          <el-icon class="is-loading" :size="22"><Compass /></el-icon>
          <div class="muted" style="font-size: 12px; margin-top: 8px">正在解析各源分类…</div>
        </div>
        <div v-else-if="sourcesErr" style="text-align: center; padding: 16px 0">
          <div style="color: var(--el-color-danger); font-size: 13px; margin-bottom: 8px">{{ sourcesErr }}</div>
          <el-button size="small" @click="loadSources">重试</el-button>
        </div>
        <div v-else-if="!visibleSources.length" class="muted" style="text-align: center; font-size: 13px; padding: 16px 0">
          没有启用了「发现」的书源（需书源配置 exploreUrl + ruleExplore 且 enabledExplore）
        </div>
        <template v-else>
          <div v-for="s in visibleSources" :key="s.sourceUrl" class="explore-source">
            <div
              class="explore-source-name"
              :class="{ active: activeSource === s.sourceUrl }"
              :title="`${s.sourceName}（${s.kinds.length} 个分类，点击选择/收起）`"
              @click="pickSource(s.sourceUrl)"
            >
              <span class="explore-arrow">{{ expandedSource === s.sourceUrl ? '▾' : '▸' }}</span>
              <span class="explore-source-text">{{ s.sourceName }}</span>
              <span class="explore-kind-count">{{ s.kinds.length }}</span>
            </div>
            <div v-if="expandedSource === s.sourceUrl" class="explore-kinds">
              <div
                v-for="k in s.kinds"
                :key="k.url"
                class="explore-kind"
                :class="{ active: activeKind === k.url }"
                @click="pickKind(k.url, k.title)"
              >{{ k.title }}</div>
            </div>
          </div>
        </template>
      </div>

      <!-- 右：分类书籍网格 -->
      <div style="flex: 1; min-width: 0">
        <div v-if="activeSource" style="margin-bottom: 12px; display: flex; align-items: center; gap: 10px">
          <strong>{{ activeKindTitle || '选择分类' }}</strong>
          <span class="muted" style="font-size: 12px">{{ books.length ? `已加载 ${books.length} 本` : '' }}</span>
        </div>
        <div v-if="exploreErr && !books.length" style="color: var(--el-color-danger); font-size: 13px; margin-bottom: 12px">
          {{ exploreErr }}
        </div>
        <div v-if="loadingBooks && !books.length" class="muted" style="text-align: center; padding: 40px 0">
          正在抓取分类列表…
        </div>
        <div v-else-if="!activeSource" class="muted" style="text-align: center; padding: 60px 0">
          ← 从左侧选择书源与分类
        </div>
        <div v-else-if="!books.length && !loadingBooks && !exploreErr" class="muted" style="text-align: center; padding: 40px 0">
          该分类暂无内容
        </div>
        <div class="explore-grid">
          <div v-for="(b, i) in books" :key="b.bookUrl + i" class="explore-card" @click="searchBook(b)">
            <CoverThumb :src="coverOf(b)" :name="b.name" @error="hideCover" />
            <div class="explore-card-name" :title="b.name">{{ b.name }}</div>
            <div class="muted" style="font-size: 12px">{{ b.author || '未知' }}</div>
            <el-tag size="small" type="info" class="explore-card-src">{{ b.sourceName }}</el-tag>
          </div>
        </div>
        <!-- 加载更多：在所有书之后（网格底部），追加失败也不清空已有内容 -->
        <div v-if="books.length" class="explore-more">
          <div v-if="exploreErr" class="muted" style="font-size: 12px; margin-bottom: 8px">{{ exploreErr }}</div>
          <el-button
            size="default"
            :loading="loadingBooks"
            :disabled="!activeKind"
            @click="loadMore"
          >加载更多</el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.explore-side {
  width: 240px;
  flex-shrink: 0;
  background: var(--bs-card, #fff);
  border: 1px solid var(--bs-border, #e5e7eb);
  border-radius: 10px;
  padding: 12px;
  max-height: 72vh;
  overflow-y: auto;
}
.explore-source { margin-bottom: 4px; }
.explore-source-name {
  padding: 7px 10px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 13.5px;
  font-weight: 500;
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}
.explore-arrow { flex-shrink: 0; width: 12px; color: var(--bs-text-sub, #64748b); font-size: 11px; }
.explore-source-text { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.explore-kind-count {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--bs-text-sub, #64748b);
  background: var(--el-fill-color, #f0f2f5);
  border-radius: 8px;
  padding: 0 6px;
  line-height: 16px;
}
.explore-source-name:hover { background: var(--el-fill-color-light, #f5f7fa); }
.explore-source-name.active { background: var(--el-color-primary-light-9, #ecf5ff); color: var(--el-color-primary); }
.explore-kinds { padding: 2px 0 6px 10px; }
.explore-kind {
  padding: 5px 10px;
  font-size: 12.5px;
  color: var(--bs-text-sub, #64748b);
  cursor: pointer;
  border-radius: 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.explore-kind:hover { background: var(--el-fill-color-lighter, #fafafa); }
.explore-kind.active { color: var(--el-color-primary); font-weight: 600; }
.explore-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
  gap: 14px;
}
.explore-more {
  text-align: center;
  padding: 24px 0 40px;
}
.explore-card { cursor: pointer; min-width: 0; }
.explore-card:hover .explore-card-name { color: var(--el-color-primary); }
.cover {
  width: 100%;
  aspect-ratio: 3/4;
  border-radius: 8px;
  overflow: hidden;
  background: var(--el-fill-color, #f0f2f5);
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 6px;
}
.cover img { width: 100%; height: 100%; object-fit: cover; }
.cover-ph { color: #fff; font-size: 22px; }
.explore-card-name {
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.explore-card-src { margin-top: 3px; max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
</style>

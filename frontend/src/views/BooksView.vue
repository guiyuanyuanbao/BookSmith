<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, coverProxy, subscribeEvents, withKey, type BookGroup, type Chapter, type SourceBook } from '@/api'
import { useSettingsStore } from '@/stores/settings'
import { useTasksStore } from '@/stores/tasks'
import { useRouter } from 'vue-router'

const router = useRouter()
const tasksStore = useTasksStore()
const groups = ref<BookGroup[]>([])
const loading = ref(false)
const keyword = ref('')
let timer: number | null = null
let closeSSE: (() => void) | null = null

const settings = useSettingsStore()
const viewMode = ref<'grid' | 'list'>(settings.bookView)

// 切换视图并记住偏好
function switchView(v: 'grid' | 'list') {
  viewMode.value = v
  settings.save({ bookView: v })
}
const sortBy = ref<'recent' | 'name' | 'progress'>('recent')

// 多源面板：单层抽屉，内部切换 来源/导出/目录 视图（消除弹窗套弹窗）
const groupDlg = ref(false)
const groupDlgTarget = ref<BookGroup | null>(null)
const groupView = ref<'sources' | 'exports' | 'toc'>('sources')
const groupActiveSource = ref<SourceBook | null>(null)
const groupExports = ref<{ id: number; name: string; format: string; size: number }[]>([])
const groupExportsLoading = ref(false)
const groupChapters = ref<Chapter[]>([])
const groupTocLoading = ref(false)

function openGroupPanel(g: BookGroup) {
  groupDlgTarget.value = g
  groupView.value = 'sources'
  groupActiveSource.value = null
  groupDlg.value = true
}

/** 在抽屉内查看某源的导出文件 */
async function viewSourceExports(sb: SourceBook) {
  groupActiveSource.value = sb
  groupView.value = 'exports'
  groupExportsLoading.value = true
  try {
    groupExports.value = (await api.listExports(100, sb.id)) ?? []
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    groupExportsLoading.value = false
  }
}

/** 在抽屉内查看某源目录 */
async function viewSourceToc(sb: SourceBook) {
  groupActiveSource.value = sb
  groupView.value = 'toc'
  groupTocLoading.value = true
  try {
    groupChapters.value = (await api.bookToc(sb.id)) ?? []
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    groupTocLoading.value = false
  }
}

function backToSources() {
  groupView.value = 'sources'
  groupActiveSource.value = null
}

// ---------- 手动上传封面 ----------
const coverFileInput = ref<HTMLInputElement | null>(null)
const coverUploading = ref(false)

async function onCoverFile(ev: Event, g: BookGroup) {
  const input = ev.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  if (file.size > 10 * 1024 * 1024) {
    ElMessage.warning('封面文件不能超过 10MB')
    return
  }
  coverUploading.value = true
  try {
    await api.uploadCover(g.primary.id, file)
    ElMessage.success('封面已更新（导出的 EPUB/PDF 将使用新封面）')
    await load(true)
    syncDrawerTarget()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    coverUploading.value = false
  }
}

async function removeCover(g: BookGroup) {
  try {
    await api.removeCover(g.primary.id)
    ElMessage.success('已恢复使用书源远程封面')
    await load(true)
    syncDrawerTarget()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

function downloadAll(list: { id: number; name: string }[]) {
  list.forEach((e, i) => {
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

async function deleteExport(e: { id: number; name: string }) {
  try {
    await ElMessageBox.confirm(`删除 ${e.name}？磁盘文件将一并删除。`, '确认删除', {
      type: 'warning', confirmButtonText: '删除',
    })
  } catch { return }
  try {
    await api.deleteExport(e.id)
    ElMessage.success('已删除')
    if (groupActiveSource.value) await viewSourceExports(groupActiveSource.value)
  } catch (e2: any) {
    ElMessage.error(e2.message)
  }
}

// 批量管理
const batchMode = ref(false)
const selected = ref<number[]>([])

function toggleBatch() {
  batchMode.value = !batchMode.value
  selected.value = []
}

function isSelected(id: number) {
  return selected.value.includes(id)
}

function toggleSelect(id: number) {
  const i = selected.value.indexOf(id)
  if (i >= 0) selected.value.splice(i, 1)
  else selected.value.push(id)
}

function selectAll() {
  selected.value = groups.value.map((g) => g.primary.id)
}

async function batchRemove() {
  if (!selected.value.length) return
  try {
    await ElMessageBox.confirm(`批量移除选中的 ${selected.value.length} 本书（含全部来源的缓存）？`, '批量移除', {
      type: 'warning', confirmButtonText: '移除',
    })
  } catch { return }
  try {
    // 展开为该书全部来源的 bookId
    const ids: number[] = []
    for (const g of groups.value) {
      if (selected.value.includes(g.primary.id)) {
        for (const s of g.sources) ids.push(s.id)
      }
    }
    await api.batchBooks('remove', ids)
    ElMessage.success(`已移除 ${selected.value.length} 本（${ids.length} 个源记录）`)
    selected.value = []
    batchMode.value = false
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

// 批量导出：对选中的每本书的主源执行纯导出，跳过缓存不完整的书
const batchExporting = ref(false)
async function batchExport() {
  if (!selected.value.length || batchExporting.value) return
  const targets = groups.value.filter((g) => selected.value.includes(g.primary.id))
  try {
    await ElMessageBox.confirm(
      `对选中的 ${targets.length} 本书的已完成缓存执行导出（${settings.defaultFormats.join('/').toUpperCase()}）。\n缓存不完整的书将自动跳过。`,
      '批量导出', { type: 'info', confirmButtonText: '开始导出' },
    )
  } catch { return }
  batchExporting.value = true
  let ok = 0, skipped = 0, failed = 0
  try {
    for (const g of targets) {
      // 找缓存最完整的源（主源）
      const sb = g.primary as SourceBook
      try {
        await api.exportNow(sb.id, [...settings.defaultFormats])
        ok++
      } catch (e: any) {
        if (e.message.includes('缓存不完整')) skipped++
        else failed++
      }
    }
    const parts = [`成功 ${ok}`]
    if (skipped) parts.push(`跳过 ${skipped}（缓存不完整）`)
    if (failed) parts.push(`失败 ${failed}`)
    if (ok) parts.push('导出文件见各书"导出"入口')
    ElMessage({ type: ok ? 'success' : 'warning', message: parts.join('，'), duration: 4000 })
    selected.value = []
    batchMode.value = false
  } finally {
    batchExporting.value = false
  }
}

// 目录预览（列表视图直接打开抽屉目录视图）
function openToc(sb: SourceBook) {
  const g = groups.value.find((g) => g.sources.some((s) => s.id === sb.id))
  if (g) {
    if (!groupDlg.value) {
      groupDlgTarget.value = g
      groupDlg.value = true
    }
    viewSourceToc(sb)
  }
}

// 导出面板（按源，供卡片"导出"按钮直接打开抽屉导出视图）
function openExports(sb: SourceBook) {
  // 统一走抽屉导出视图；若抽屉未开则用目标书的分组打开
  const g = groups.value.find((g) => g.sources.some((s) => s.id === sb.id))
  if (g) {
    if (!groupDlg.value) {
      groupDlgTarget.value = g
      groupDlg.value = true
    }
    viewSourceExports(sb)
  }
}

// 下载/续传（按源）：创建/复用任务并加入全局浮窗跟踪
async function continueSource(sb: SourceBook) {
  try {
    const r = await api.bookDownload(sb.id, {
      formats: [...settings.defaultFormats],
      concurrency: settings.concurrency,
    })
    ElMessage.success(r.existing ? `任务 #${r.taskId} 在进行中` : `任务 #${r.taskId} 已创建，进度实时显示`)
    // 拉任务详情进浮窗，跨页面可见
    try {
      const t = await api.getTask(r.taskId)
      tasksStore.add(r.taskId, {
        name: t.bookName || sb.name, author: t.author || sb.author,
        done: t.doneChapters, total: t.totalChapters, status: t.status,
        failChapters: t.failChapters, errMsg: t.errMsg,
        originName: t.originName || sb.originName, origin: t.origin || sb.origin,
      })
    } catch { /* 任务详情获取失败不影响续传 */ }
    await load()
    syncDrawerTarget() // 续传后刷新抽屉里的进度显示
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

// 纯导出（按源）
async function exportSource(sb: SourceBook) {
  try {
    await api.exportNow(sb.id, ['txt', 'epub', 'pdf'])
    ElMessage.success('已从缓存导出')
    await openExports(sb)
    await load()
    syncDrawerTarget()
  } catch (e: any) {
    if (e.message.includes('缓存不完整')) {
      try {
        await ElMessageBox.confirm('章节缓存不完整，需要先续传下载缺失章节。是否现在续传？', '提示', {
          type: 'info', confirmButtonText: '续传', cancelButtonText: '取消',
        })
        await continueSource(sb)
      } catch { /* 取消 */ }
    } else {
      ElMessage.error(e.message)
    }
  }
}

async function removeGroup(g: BookGroup) {
  try {
    await ElMessageBox.confirm(
      `移除《${g.primary.name}》全部 ${g.sources.length} 个来源？\n章节缓存与下载记录将一并删除。`,
      '确认移除', { type: 'warning', confirmButtonText: '移除' },
    )
  } catch { return }
  try {
    await api.batchBooks('remove', g.sources.map((s) => s.id))
    ElMessage.success('已移除')
    await load()
    syncDrawerTarget() // 若抽屉正开着这本书，自动关闭
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function removeSource(sb: SourceBook) {
  try {
    await ElMessageBox.confirm(`移除来源「${sb.originName || sb.origin}」及其缓存？`, '确认', { type: 'warning' })
  } catch { return }
  try {
    await api.batchBooks('remove', [sb.id])
    ElMessage.success('已移除该来源')
    await load()
    // 抽屉读的是 groupDlgTarget 快照：load 后重定位到新分组，来源列表才会更新
    syncDrawerTarget()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

/** load() 后按主源 id 把抽屉目标重新指到新 groups 里的对象（分组被删则关闭抽屉） */
function syncDrawerTarget() {
  if (!groupDlg.value || !groupDlgTarget.value) return
  const pid = groupDlgTarget.value.primary.id
  const fresh = groups.value.find((g) => g.primary.id === pid)
    ?? groups.value.find((g) => g.sources.some((s) => s.id === pid))
  if (!fresh) {
    groupDlg.value = false
    groupDlgTarget.value = null
    groupActiveSource.value = null
    return
  }
  groupDlgTarget.value = fresh
  if (groupActiveSource.value) {
    groupActiveSource.value =
      fresh.sources.find((s) => s.id === groupActiveSource.value!.id) ?? null
    if (!groupActiveSource.value && groupView.value !== 'sources') {
      groupView.value = 'sources' // 活动源被移除，退回来源列表
    }
  }
}

/** 拉取书架分组；silent=true 用于轮询/事件刷新，不触发骨架屏（避免闪烁卡顿） */
async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const r = await api.listBookGroups(keyword.value || undefined)
    groups.value = r.items ?? []
  } catch (e: any) {
    if (!silent) ElMessage.error(e.message)
  } finally {
    if (!silent) loading.value = false
  }
}

const sortedGroups = computed(() => {
  const list = [...groups.value]
  switch (sortBy.value) {
    case 'name':
      return list.sort((a, b) => a.primary.name.localeCompare(b.primary.name, 'zh'))
    case 'progress':
      return list.sort((a, b) => {
        const pa = a.totalChapters ? a.doneChapters / a.totalChapters : 0
        const pb = b.totalChapters ? b.doneChapters / b.totalChapters : 0
        return pb - pa
      })
    default:
      return list.sort((a, b) => (b.primary.updatedAt ?? 0) - (a.primary.updatedAt ?? 0))
  }
})

function goSearch() {
  router.push('/search')
}

/** 未下载的书：跳搜索页自动搜该书并打开选源对话框（与换源重下同一机制），盲选源失败的体验太差 */
function downloadViaSearch(name: string, author: string) {
  router.push({ path: '/search', query: { pick: name, author } })
}

// 无封面柔和底色
const phColors = ['#7fa89a', '#a58f7a', '#8f9a7a', '#9a7a8f', '#7a8f9a']
function phColor(name: string) {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xffff
  return phColors[h % phColors.length]
}

// 封面地址：手动上传（/api/books/{id}/cover）优先于远程 CoverURL（代理）
// t 参数取 updatedAt，更换封面后立即失效缓存
function coverSrc(g: BookGroup): string | null {
  if (g.primary.localCover) return withKey(`/api/books/${g.primary.id}/cover?t=${g.primary.updatedAt ?? 0}`)
  return g.coverUrl ? coverProxy(g.coverUrl) : null
}

function progressOf(sb: SourceBook): number {
  const s = sb.stats
  if (!s || s.totalChapters === 0) return 0
  return Math.min(100, Math.round(((s.doneChapters + s.failedChapters) / s.totalChapters) * 100))
}

function groupProgress(g: BookGroup): number {
  return g.totalChapters ? Math.min(100, Math.round((g.doneChapters / g.totalChapters) * 100)) : 0
}

function statusOfSource(sb: SourceBook): { text: string; type: 'success' | 'warning' | 'danger' | 'info' | 'primary' } {
  const s = sb.stats
  if (sb.sourceMissing) {
    return s && s.doneChapters > 0 ? { text: '源已删除', type: 'warning' } : { text: '源已删除', type: 'info' }
  }
  if (!s || !s.hasTask) {
    return s && s.doneChapters > 0 ? { text: '已缓存', type: 'info' } : { text: '未下载', type: 'info' }
  }
  if (s.activeTask) {
    const st = s.activeTask.status
    if (st === 'running') return { text: s.activeTask.phase === 'toc' ? '获取目录' : s.activeTask.phase === 'export' ? '导出中' : '下载中', type: 'primary' }
    if (st === 'paused') return { text: '已暂停', type: 'warning' }
    if (st === 'pending') return { text: '排队中', type: 'info' }
    if (st === 'exporting') return { text: '导出中', type: 'warning' }
  }
  // 章节数据优先于任务状态：任务"done"但大量章节失败时，"已完成"是误导
  const total = s.totalChapters ?? 0
  if (total > 0 && s.failedChapters > 0) {
    return { text: `部分失败（${s.doneChapters}/${total}）`, type: 'danger' }
  }
  if (total > 0 && s.doneChapters >= total) {
    return { text: '已完成', type: 'success' }
  }
  if (total > 0 && s.doneChapters > 0) {
    return { text: `已缓存 ${s.doneChapters}/${total}`, type: 'warning' }
  }
  const lt = s.lastTask
  if (lt) {
    if (lt.status === 'done') return { text: '已完成', type: 'success' }
    if (lt.status === 'failed') return { text: '下载失败', type: 'danger' }
    if (lt.status === 'paused') return { text: '已暂停', type: 'warning' }
    if (lt.status === 'canceled') return { text: '已取消', type: 'info' }
  }
  return { text: '已缓存', type: 'info' }
}

// 组状态：取主源
function statusOfGroup(g: BookGroup) {
  return statusOfSource(g.sources[0] ?? { ...g.primary, stats: undefined } as SourceBook)
}

function sourceCanContinue(sb: SourceBook): boolean {
  const s = sb.stats
  if (!s) return false
  return s.pendingChapters > 0 || s.failedChapters > 0
}

function hasFailedPrimary(g: BookGroup): boolean {
  return (g.sources[0]?.stats?.failedChapters ?? 0) > 0
}

onMounted(() => {
  load()
  timer = window.setInterval(() => {
    if (groups.value.some((g) => g.sources.some((s) => s.stats?.activeTask))) load(true)
  }, 2500)
  closeSSE = subscribeEvents({ onTaskDone: () => load(true), onTaskError: () => load(true) })
})

onBeforeUnmount(() => {
  if (timer) window.clearInterval(timer)
  closeSSE?.()
})
</script>

<template>
  <div class="page">
    <h1 class="page-title">书架</h1>
    <p class="page-sub">同一本书的多来源聚合展示：每个来源独立缓存与进度，可换源续传或分别导出</p>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索书名/作者" style="width: 200px" clearable @input="load" />
      <el-select v-model="sortBy" style="width: 120px">
        <el-option value="recent" label="最近更新" />
        <el-option value="name" label="书名" />
        <el-option value="progress" label="下载进度" />
      </el-select>
      <div class="spacer" />
      <template v-if="batchMode">
        <el-button :disabled="!selected.length" type="success" plain @click="batchExport">批量导出 ({{ selected.length }})</el-button>
        <el-button :disabled="!selected.length" type="warning" @click="batchRemove">移除选中 ({{ selected.length }})</el-button>
        <el-button @click="selectAll">全选</el-button>
      </template>
      <el-button :type="batchMode ? 'warning' : 'default'" :plain="!batchMode" @click="toggleBatch">
        {{ batchMode ? '退出批量' : '批量管理' }}
      </el-button>
        <el-button-group>
          <el-button :type="viewMode === 'grid' ? 'primary' : 'default'" @click="switchView('grid')" style="padding: 8px 12px">▦</el-button>
          <el-button :type="viewMode === 'list' ? 'primary' : 'default'" @click="switchView('list')" style="padding: 8px 12px">☰</el-button>
        </el-button-group>
      <el-button type="primary" @click="goSearch">去搜索新书</el-button>
    </div>

    <!-- 网格视图 -->
    <div v-if="viewMode === 'grid'" class="book-grid">
      <div
        v-for="g in sortedGroups"
        :key="g.primary.id"
        class="book-tile"
        :class="{ selected: batchMode && isSelected(g.primary.id) }"
        @click="batchMode ? toggleSelect(g.primary.id) : undefined"
        :style="batchMode ? 'cursor:pointer' : ''"
      >
        <div class="tile-cover">
          <img
            v-if="coverSrc(g)"
            :src="coverSrc(g) ?? undefined"
            loading="lazy"
            @error="($event.target as HTMLImageElement).style.display = 'none'"
          />
          <span v-else class="cover-ph" :style="{ background: phColor(g.primary.name) }">书</span>
          <el-checkbox
            v-if="batchMode"
            :model-value="isSelected(g.primary.id)"
            style="position: absolute; top: 6px; right: 6px; z-index: 2"
            @click.stop
            @change="toggleSelect(g.primary.id)"
          />
          <span class="cover-badge" :class="'badge-' + statusOfGroup(g).type">{{ statusOfGroup(g).text }}</span>
          <span v-if="g.sourceCount > 1" class="cover-badge badge-count">{{ g.sourceCount }} 源</span>
        </div>
        <div class="tile-name" :title="g.primary.name">{{ g.primary.name }}</div>
        <div class="tile-author muted">{{ g.primary.author || '未知' }}</div>
        <el-progress v-if="g.totalChapters > 0" :percentage="groupProgress(g)" :stroke-width="5" :show-text="false" style="margin: 4px 0" />
        <div class="muted tile-chapters">{{ g.doneChapters }}/{{ g.totalChapters }} 章</div>
        <div class="tile-actions">
          <!-- 主操作：源已删→仅导出；有进度→续传/重跑；从未下载→下载；缓存完整→导出 -->
          <el-button
            v-if="!g.sources[0]?.sourceMissing && sourceCanContinue(g.sources[0])"
            size="small"
            type="primary"
            @click.stop="continueSource(g.sources[0])"
          >
            {{ hasFailedPrimary(g) ? '重跑' : '续传' }}
          </el-button>
          <el-button
            v-else-if="!g.sources[0]?.sourceMissing && !g.sources[0]?.stats?.totalChapters"
            size="small"
            type="primary"
            plain
            @click.stop="downloadViaSearch(g.primary.name, g.primary.author || '')"
          >下载</el-button>
          <el-button v-else size="small" type="primary" @click.stop="openExports(g.sources[0])">导出</el-button>
          <el-button v-if="g.sourceCount > 1" size="small" @click.stop="openGroupPanel(g)">多源</el-button>
        </div>


      </div>
    </div>

    <!-- 列表视图 -->
    <div v-else>
      <div v-for="g in sortedGroups" :key="g.primary.id" class="book-card" style="margin-bottom: 12px">
        <div class="cover">
          <img
            v-if="coverSrc(g)"
            :src="coverSrc(g) ?? undefined"
            loading="lazy"
            @error="($event.target as HTMLImageElement).style.display = 'none'"
          />
          <span v-else class="cover-ph" :style="{ background: phColor(g.primary.name) }">书</span>
        </div>
        <div style="flex: 1; min-width: 0">
          <div style="display: flex; gap: 10px; align-items: baseline; flex-wrap: wrap">
            <strong style="font-size: 16px">{{ g.primary.name }}</strong>
            <span class="muted">{{ g.primary.author || '未知作者' }}</span>
            <el-tag size="small" :type="statusOfGroup(g).type">{{ statusOfGroup(g).text }}</el-tag>
            <el-tag v-if="g.sourceCount > 1" size="small" type="warning">{{ g.sourceCount }} 个来源</el-tag>
            <el-checkbox
              v-if="batchMode"
              :model-value="isSelected(g.primary.id)"
              @change="toggleSelect(g.primary.id)"
            />
          </div>
          <p class="intro-text" style="margin: 6px 0">{{ g.intro || '暂无简介' }}</p>
          <el-progress
            v-if="g.totalChapters > 0"
            :percentage="groupProgress(g)"
            :stroke-width="12"
            text-inside
            style="margin: 8px 0"
          />
          <div class="muted" style="margin-bottom: 8px">
            章节 {{ g.doneChapters }}/{{ g.totalChapters }}
          </div>

          <!-- 来源列表（列表视图直接平铺） -->
          <div class="source-list">
            <div v-for="sb in g.sources" :key="sb.id" class="source-chip">
              <span class="chip-name" :title="sb.originName || sb.origin">{{ sb.originName || sb.origin }}</span>
              <el-tag size="small" :type="statusOfSource(sb).type">{{ statusOfSource(sb).text }}</el-tag>
              <span class="muted">{{ sb.stats?.doneChapters ?? 0 }}/{{ sb.stats?.totalChapters ?? 0 }}</span>
              <el-button
                v-if="!sb.sourceMissing && (sourceCanContinue(sb) || !sb.stats?.totalChapters)"
                size="small" link type="primary" @click="continueSource(sb)"
              >
                {{ sourceCanContinue(sb) ? '续传' : '下载' }}
              </el-button>
              <el-button v-else size="small" link type="primary" @click="openExports(sb)">导出</el-button>
              <el-button size="small" link @click="openToc(sb)">目录</el-button>
            </div>
          </div>
        </div>
        <div style="display: flex; flex-direction: column; gap: 8px; justify-content: center; min-width: 110px">
          <el-button v-if="sourceCanContinue(g.sources[0])" type="primary" @click="continueSource(g.sources[0])">
            {{ hasFailedPrimary(g) ? '重跑失败' : '继续下载' }}
          </el-button>
          <el-button
            v-else-if="!g.sources[0]?.stats?.totalChapters"
            type="primary" plain
            @click="downloadViaSearch(g.primary.name, g.primary.author || '')"
          >下载</el-button>
          <el-button v-else type="primary" @click="openExports(g.sources[0])">导出文件</el-button>
          <el-button link type="danger" @click="removeGroup(g)">移除</el-button>
        </div>
      </div>
    </div>

    <!-- 首次加载骨架屏 -->
    <div v-if="loading && groups.length === 0" class="book-grid">
      <div v-for="i in 5" :key="i" class="book-tile skeleton">
        <el-skeleton animated>
          <template #template>
            <el-skeleton-item variant="image" style="width: 100%; aspect-ratio: 3/4; border-radius: 8px" />
            <div style="padding: 10px 0 0">
              <el-skeleton-item variant="h3" style="width: 70%" />
              <el-skeleton-item variant="text" style="width: 45%; margin-top: 6px" />
            </div>
          </template>
        </el-skeleton>
      </div>
    </div>

    <el-empty v-else-if="!loading && groups.length === 0" :description="keyword ? `没有找到与「${keyword}」匹配的书` : '书架还是空的'">
      <div style="display: flex; gap: 10px; justify-content: center">
        <el-button v-if="keyword" @click="keyword = ''; load()">清除筛选</el-button>
        <el-button v-if="keyword" type="primary" @click="downloadViaSearch(keyword, '')">去搜索《{{ keyword }}》</el-button>
        <el-button v-if="!keyword" type="primary" @click="goSearch">去搜索第一本书</el-button>
      </div>
    </el-empty>

    <!-- 多源抽屉：单层容器，内部切换 来源/导出/目录 视图 -->
    <el-drawer v-model="groupDlg" size="560" :with-header="false">
      <div v-if="groupDlgTarget" class="drawer-body">
        <div class="drawer-head">
          <el-button v-if="groupView !== 'sources'" link @click="backToSources">← 来源列表</el-button>
          <div class="drawer-title">
            <div class="drawer-book" :title="groupDlgTarget.primary.name">
              {{ groupView === 'sources'
                ? groupDlgTarget.primary.name
                : `${groupDlgTarget.primary.name} · ${groupActiveSource?.originName || ''}` }}
            </div>
            <div class="muted drawer-sub">
              {{ groupView === 'sources'
                ? `${groupDlgTarget.sourceCount} 个来源 · 点击来源查看导出与目录`
                : groupView === 'exports' ? '导出文件' : '章节目录' }}
            </div>
          </div>
        </div>

        <!-- 视图1：来源列表 -->
        <template v-if="groupView === 'sources'">
          <!-- 封面管理：预览 + 上传 + 移除 -->
          <div class="drawer-cover">
            <img v-if="coverSrc(groupDlgTarget)" :src="coverSrc(groupDlgTarget)!" class="drawer-cover-img" @error="($event.target as HTMLImageElement).style.display = 'none'" />
            <span v-else class="drawer-cover-img cover-ph" :style="{ background: phColor(groupDlgTarget.primary.name) }">书</span>
            <div class="drawer-cover-ops">
              <div class="drawer-cover-name">
                {{ groupDlgTarget.primary.localCover ? '已使用手动上传的封面' : '使用书源远程封面' }}
              </div>
              <div style="display: flex; gap: 8px">
                <el-button size="small" type="primary" plain :loading="coverUploading" @click="coverFileInput?.click()">
                  {{ groupDlgTarget.primary.localCover ? '更换封面' : '上传封面' }}
                </el-button>
                <el-button v-if="groupDlgTarget.primary.localCover" size="small" plain @click="removeCover(groupDlgTarget)">恢复远程</el-button>
              </div>
              <input
                ref="coverFileInput"
                type="file"
                accept="image/jpeg,image/png,image/gif,image/webp"
                style="display: none"
                @change="onCoverFile($event, groupDlgTarget)"
              />
            </div>
          </div>

          <div v-for="sb in groupDlgTarget.sources" :key="sb.id" class="ms-row">
            <div class="ms-main">
              <div class="ms-head">
                <strong class="ms-name">{{ sb.originName || sb.origin }}</strong>
                <el-tag size="small" :type="statusOfSource(sb).type">{{ statusOfSource(sb).text }}</el-tag>
              </div>
              <el-progress v-if="sb.stats && sb.stats.totalChapters > 0" :percentage="progressOf(sb)" :stroke-width="5" :show-text="false" style="margin: 4px 0" />
              <div class="muted ms-meta">
                {{ sb.stats?.doneChapters ?? 0 }}/{{ sb.stats?.totalChapters ?? 0 }} 章
                <template v-if="sb.stats && sb.stats.failedChapters > 0">
                  <span style="color: var(--bs-danger)">· 失败 {{ sb.stats.failedChapters }}</span>
                </template>
              </div>
            </div>
            <div class="ms-actions">
              <el-button
                v-if="!sb.sourceMissing && sourceCanContinue(sb)"
                size="small" type="primary" @click="continueSource(sb)"
              >
                {{ sb.stats?.failedChapters ? '重跑失败' : '续传' }}
              </el-button>
              <el-button
                v-else-if="!sb.sourceMissing && !sb.stats?.totalChapters"
                size="small" type="primary" plain
                @click="continueSource(sb)"
              >下载</el-button>
              <el-button v-else size="small" type="primary" plain @click="viewSourceExports(sb)">导出</el-button>
              <el-button size="small" @click="viewSourceToc(sb)">目录</el-button>
              <el-button size="small" link type="danger" @click="removeSource(sb)">移除</el-button>
            </div>
          </div>
        </template>

        <!-- 视图2：导出文件 -->
        <template v-else-if="groupView === 'exports'">
          <div v-if="groupExportsLoading" class="muted" style="text-align: center; padding: 30px 0">加载中…</div>
          <div v-else-if="groupExports.length === 0" class="muted" style="text-align: center; padding: 30px 0">
            该来源还没有导出文件
            <div style="margin-top: 12px">
              <el-button type="primary" size="small" @click="groupActiveSource && exportSource(groupActiveSource)">立即导出</el-button>
            </div>
          </div>
          <template v-else>
            <div v-for="e in groupExports" :key="e.id" class="export-file-row">
              <el-tag size="small" :color="{txt:'#67c23a',epub:'#409eff',pdf:'#f56c6c',html:'#e6a23c',md:'#909399'}[e.format]" class="fmt-tag">
                {{ e.format.toUpperCase() }}
              </el-tag>
              <span style="flex:1; font-size:13px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap">{{ e.name }}</span>
              <span class="muted" style="font-size: 12px">{{ e.size < 1048576 ? (e.size/1024).toFixed(1)+' KB' : (e.size/1048576).toFixed(2)+' MB' }}</span>
              <a :href="api.fileUrl(e)" target="_blank"><el-button type="primary" size="small">下载</el-button></a>
              <el-button size="small" type="danger" plain @click="deleteExport(e)">删除</el-button>
            </div>
            <div style="margin-top: 14px; display: flex; gap: 8px">
              <el-button size="small" type="primary" @click="downloadAll(groupExports)">全部下载</el-button>
              <el-button size="small" type="primary" plain @click="groupActiveSource && exportSource(groupActiveSource)">重新导出</el-button>
            </div>
          </template>
        </template>

        <!-- 视图3：章节目录 -->
        <template v-else>
          <el-table :data="groupChapters" v-loading="groupTocLoading" max-height="560" size="small">
            <el-table-column prop="index" label="#" width="60" />
            <el-table-column label="章节" min-width="240">
              <template #default="{ row }">
                <span :style="row.isVolume ? 'font-weight:600' : ''">{{ row.title }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag v-if="row.status === 2" size="small" type="success">已缓存</el-tag>
                <el-tag v-else-if="row.status === 3" size="small" type="danger">失败</el-tag>
                <span v-else class="muted">—</span>
              </template>
            </el-table-column>
          </el-table>
        </template>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.book-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 18px;
}
.book-tile {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 14px;
  box-shadow: var(--bs-shadow);
  transition: transform 0.2s, box-shadow 0.2s;
  display: flex;
  flex-direction: column;
}
.book-tile:hover { transform: translateY(-3px); box-shadow: var(--bs-shadow-hover); }
.book-tile.selected { outline: 3px solid var(--bs-primary); outline-offset: -1px; }
.tile-cover {
  width: 100%;
  aspect-ratio: 3/4;
  border-radius: 8px;
  background: linear-gradient(160deg, var(--bs-primary-light), var(--bs-primary));
  display: flex; align-items: center; justify-content: center;
  color: #fff; font-size: 32px;
  overflow: hidden; position: relative;
  margin-bottom: 10px;
}
.tile-cover img { width: 100%; height: 100%; object-fit: cover; }
.tile-cover > .cover-ph {
  width: 100%; height: 100%;
  display: flex; align-items: center; justify-content: center;
  color: #fff; font-size: 32px;
}

/* 封面角标：紧凑、半透明底、不遮挡封面主体 */
.cover-badge {
  position: absolute;
  top: 6px;
  left: 6px;
  z-index: 2;
  font-size: 11px;
  line-height: 1;
  padding: 4px 8px;
  border-radius: 6px;
  color: #fff;
  background: rgba(30, 30, 30, 0.55);
  backdrop-filter: blur(4px);
  white-space: nowrap;
}
.cover-badge.badge-success { background: rgba(77, 158, 111, 0.85); }
.cover-badge.badge-warning { background: rgba(217, 155, 66, 0.85); }
.cover-badge.badge-danger { background: rgba(208, 86, 86, 0.85); }
.cover-badge.badge-primary { background: rgba(47, 111, 94, 0.85); }
.cover-badge.badge-count {
  top: auto;
  bottom: 6px;
  background: rgba(30, 30, 30, 0.55);
}
.tile-name {
  font-weight: 600; font-size: 14.5px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  margin-bottom: 2px;
}
.tile-author { font-size: 12px; margin-bottom: 2px; }
.tile-chapters { font-size: 11.5px; margin-bottom: 8px; }
.tile-actions { display: flex; gap: 6px; margin-top: auto; flex-wrap: wrap; }
.tile-actions .el-button { flex: 1; margin: 0; min-width: 62px; }

/* 多源面板 */
.source-panel {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px dashed var(--bs-border);
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.source-row {
  background: #faf8f4;
  border-radius: 8px;
  padding: 8px 10px;
}
.source-head {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
}
.source-name {
  font-size: 12.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 110px;
}
.source-meta { font-size: 11px; margin: 2px 0 4px; }
.source-actions { display: flex; gap: 4px; flex-wrap: wrap; }
.source-actions .el-button { margin: 0; }

/* 列表视图来源条 */
.source-list { display: flex; flex-direction: column; gap: 4px; margin-top: 4px; }
.source-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  background: #faf8f4;
  border-radius: 7px;
  font-size: 12px;
}
.chip-name {
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.source-chip .el-button { margin: 0; padding: 2px 4px; }

.export-file-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px dashed var(--bs-border);
}
.export-file-row:last-of-type { border-bottom: none; }
.fmt-tag { color: #fff; border: none; width: 54px; text-align: center; flex-shrink: 0; }
.ms-row {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--bs-border);
  border-radius: 10px;
  transition: background 0.12s;
}
.ms-row:hover { background: var(--el-fill-color-light); }
.ms-row:last-of-type { border-bottom: none; }
.ms-main { flex: 1; min-width: 0; }
.ms-head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; }
.ms-name { font-size: 13.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ms-meta { font-size: 12px; margin: 3px 0 0; }
.ms-actions { display: flex; flex-direction: column; gap: 5px; min-width: 88px; }
.ms-actions .el-button { margin: 0; }

/* 多源抽屉 */
.drawer-body { padding: 4px; }
.drawer-head { margin-bottom: 14px; }
/* 封面管理区 */
.drawer-cover {
  display: flex;
  gap: 14px;
  align-items: center;
  padding: 12px;
  background: var(--el-fill-color-light, #f4f1ea);
  border-radius: 12px;
  margin-bottom: 14px;
}
.drawer-cover-img {
  width: 72px;
  height: 96px;
  object-fit: cover;
  border-radius: 8px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 24px;
}
.drawer-cover-ops { display: flex; flex-direction: column; gap: 8px; }
.drawer-cover-name { font-size: 12.5px; color: var(--bs-text-sub); }
.drawer-title { margin-top: 4px; }
.drawer-book {
  font-family: 'Noto Serif SC', serif;
  font-size: 18px;
  font-weight: 700;
  color: var(--bs-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.drawer-sub { font-size: 12.5px; margin-top: 2px; }
.book-tile.skeleton {
  pointer-events: none;
  opacity: 0.65;
}
</style>
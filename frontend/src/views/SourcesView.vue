<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Loading } from '@element-plus/icons-vue'
import { api, subscribeEvents, type BookSource, type DebugTrace, type SourceRepo, type RepoProviderResult, type RepoSyncProgress, type ImportResult } from '@/api'

const items = ref<BookSource[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const keyword = ref('')
const enabledFilter = ref(-1)
const loading = ref(false)

const importDlg = ref(false)
const importURL = ref('')
const importText = ref('')
const importing = ref(false)

// 书源广场（动态发现 + 异步同步）
const reposDlg = ref(false)
const providers = ref<RepoProviderResult[]>([])
const providersLoading = ref(false)
const providersErr = ref('')
const activeProvider = ref('aoaostar')
const selectedRepos = ref<SourceRepo[]>([])
const repoCleanup = ref(false)

// 校验
const verifying = ref(false)
const verifyDlg = ref(false)
const verifyResults = ref<{ name: string; ok: boolean; elapsedMs: number; err?: string }[]>([])

// 批量选择
const batchMode = ref(false)
const selected = ref<string[]>([])

function toggleBatch() {
  batchMode.value = !batchMode.value
  selected.value = []
}

function isSelected(url: string) {
  return selected.value.includes(url)
}

function toggleSelect(url: string) {
  const i = selected.value.indexOf(url)
  if (i >= 0) selected.value.splice(i, 1)
  else selected.value.push(url)
}

function selectAllPage() {
  selected.value = items.value.map((s) => s.bookSourceUrl)
}

async function batchAction(action: 'enable' | 'disable' | 'delete') {
  if (!selected.value.length) return
  const label = action === 'enable' ? '启用' : action === 'disable' ? '禁用' : '删除'
  try {
    await ElMessageBox.confirm(`批量${label}选中的 ${selected.value.length} 个书源？`, '批量操作', {
      type: action === 'delete' ? 'warning' : 'info', confirmButtonText: label,
    })
  } catch { return }
  try {
    const r = await api.batchSources(action, selected.value)
    ElMessage.success(`已${label} ${r.affected} 个书源`)
    selected.value = []
    batchMode.value = false
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

// 一键清理失效源
const cleanupDlg = ref(false)
const cleaning = ref(false)
const cleanupMode = ref<'disable' | 'delete'>('disable')
const cleanupScope = ref<'enabled' | 'all'>('enabled')
const cleanupResult = ref<{
  checked: number
  dead: number
  alive: number
  disabled: number
  deleted: number
  elapsedMs: number
  results: { name: string; ok: boolean; elapsedMs: number; err?: string }[]
} | null>(null)
const cleanupProgress = ref<{ checked: number; total: number; dead: number; alive: number; disabled: number; deleted: number } | null>(null)
let cleanupId = ''
let closeCleanupSSE: (() => void) | null = null

async function runCleanup() {
  cleaning.value = true
  cleanupResult.value = null
  cleanupProgress.value = null
  closeCleanupSSE?.()
  closeCleanupSSE = subscribeEvents({
    onCleanupProgress: (d) => {
      if (d.cleanupId === cleanupId) {
        cleanupProgress.value = { checked: d.checked, total: d.total, dead: d.dead, alive: d.alive, disabled: d.disabled ?? 0, deleted: d.deleted ?? 0 }
      }
    },
  })
  try {
    const start = await api.cleanupStart({ scope: cleanupScope.value, mode: cleanupMode.value })
    cleanupId = start.cleanupId
    // 轮询进度直至完成
    for (;;) {
      await new Promise((r) => setTimeout(r, 800))
      const st = await api.cleanupState(cleanupId)
      cleanupProgress.value = { checked: st.checked, total: st.total, dead: st.dead, alive: st.alive, disabled: st.disabled, deleted: st.deleted }
      if (!st.running) {
        cleanupResult.value = st
        break
      }
    }
    load()
    const r = cleanupResult.value!
    ElMessage.success(
      `校验 ${r.checked} 个：可用 ${r.alive}，失效 ${r.dead}` +
        (cleanupMode.value === 'delete'
          ? `，已删除 ${r.deleted}`
          : cleanupMode.value === 'disable'
            ? `，已禁用 ${r.disabled}`
            : ''),
    )
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    cleaning.value = false
    closeCleanupSSE?.()
    closeCleanupSSE = null
  }
}

async function cancelCleanup() {
  if (cleanupId) {
    try {
      await api.cleanupCancel(cleanupId)
    } catch { /* 忽略 */ }
  }
}

function openCleanup() {
  cleanupResult.value = null
  cleanupDlg.value = true
}

// 调试
const debugDlg = ref(false)
const debugSource = ref<BookSource | null>(null)
const debugTrace = ref<DebugTrace | null>(null)
const debugLoading = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await api.listSources({
      keyword: keyword.value || undefined,
      enabled: enabledFilter.value,
      limit: pageSize,
      offset: (page.value - 1) * pageSize,
    })
    items.value = r.items ?? []
    total.value = r.total
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function toggle(bs: BookSource) {
  try {
    await api.setSourceEnabled(bs.bookSourceUrl, !bs.enabled)
    bs.enabled = !bs.enabled
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function remove(bs: BookSource) {
  try {
    await ElMessageBox.confirm(`删除书源「${bs.bookSourceName}」？`, '确认', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.deleteSource(bs.bookSourceUrl)
    ElMessage.success('已删除')
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function doImport() {
  if (!importURL.value.trim() && !importText.value.trim()) {
    ElMessage.warning('请填写书源 URL 或粘贴 JSON')
    return
  }
  importing.value = true
  try {
    const r: ImportResult = await api.importSources({
      url: importURL.value.trim() || undefined,
      text: importText.value.trim() || undefined,
    })
    ElMessage.success(`导入完成：新增 ${r.inserted}，更新 ${r.updated}，共 ${r.total}`)
    importDlg.value = false
    importURL.value = ''
    importText.value = ''
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    importing.value = false
  }
}

async function openRepos() {
  reposDlg.value = true
  await loadRepos(false)
}

async function loadRepos(refresh: boolean) {
  providersLoading.value = true
  providersErr.value = ''
  try {
    const r = await api.repos(refresh)
    providers.value = r.providers
  } catch (e: any) {
    providersErr.value = e.message || '仓库发现失败'
  } finally {
    providersLoading.value = false
  }
}

// ---------- 书源广场：异步同步 ----------
const syncJob = ref<{ id: string; progress: RepoSyncProgress } | null>(null)
let syncPollTimer: number | null = null

function stopSyncPoll() {
  if (syncPollTimer !== null) {
    clearInterval(syncPollTimer)
    syncPollTimer = null
  }
}

async function pollSyncJob(id: string) {
  try {
    const p = await api.syncRepoStatus(id)
    if (syncJob.value) syncJob.value.progress = p
    if (!p.running && p.finished) {
      stopSyncPoll()
      if (p.err) {
        ElMessage.error(`「${p.repoName}」同步失败：${p.err}`)
      } else {
        ElMessage.success(
          `「${p.repoName}」同步完成：新增 ${p.inserted}，更新 ${p.updated}` +
            (p.removed ? `，清理同仓旧源 ${p.removed}` : ''),
        )
      }
      load()
    }
  } catch {
    /* 任务可能已过期，停止轮询 */
    stopSyncPoll()
  }
}

/** 启动一个仓库的异步同步（cleanup=清理同仓旧源） */
async function syncRepo(repo: SourceRepo, cleanup = false) {
  if (syncJob.value?.progress.running) {
    ElMessage.warning('已有同步任务进行中')
    return
  }
  try {
    const r = await api.syncRepoStart({ url: repo.url, name: repo.name, kind: repo.kind, cleanup })
    syncJob.value = { id: r.jobId, progress: { repoName: r.repoName, total: 0, done: 0, inserted: 0, updated: 0, running: true, finished: false } }
    stopSyncPoll()
    syncPollTimer = window.setInterval(() => pollSyncJob(r.jobId), 1000)
  } catch (e: any) {
    ElMessage.error(`${repo.name}: ${e.message}`)
  }
}

/** 批量同步选中的 yiove 合集（顺序逐个） */
async function syncSelectedRepos() {
  const list = selectedRepos.value
  if (!list.length) return
  for (const repo of list) {
    await syncRepo(repo, false)
    // 等当前任务完成再继续（轮询中 progress.finished 置位）
    await new Promise<void>((resolve) => {
      const t = setInterval(() => {
        if (!syncJob.value?.progress.running) {
          clearInterval(t)
          resolve()
        }
      }, 500)
    })
  }
  selectedRepos.value = []
}

async function cancelSync() {
  if (!syncJob.value) return
  try {
    await api.syncRepoCancel(syncJob.value.id)
    ElMessage.info('已请求取消')
  } catch { /* ignore */ }
}

onUnmounted(stopSyncPoll)

function exportAll() {
  window.open(api.exportSources(), '_blank')
}

// 校验当前页全部书源
async function verifyPage() {
  const urls = items.value.map((s) => s.bookSourceUrl)
  if (!urls.length) return
  verifying.value = true
  verifyResults.value = []
  verifyDlg.value = true
  try {
    verifyResults.value = await api.verifySources(urls)
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    verifying.value = false
  }
}

// 调试
async function openDebug(bs: BookSource) {
  debugSource.value = bs
  debugDlg.value = true
  debugLoading.value = true
  debugTrace.value = null
  try {
    debugTrace.value = await api.sourceDebug(bs.bookSourceUrl)
  } catch (e: any) {
    ElMessage.error(e.message)
    debugDlg.value = false
  } finally {
    debugLoading.value = false
  }
}

function fmtMs(ms?: number) {
  if (!ms) return '—'
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h1 class="page-title">书源管理</h1>
    <p class="page-sub">兼容「阅读」APP 全部书源 JSON；内置仓库实时同步；支持校验可用性与逐步骤调试</p>

    <div class="toolbar">
      <el-input v-model="keyword" placeholder="搜索名称/分组" style="width: 220px" clearable @input="page = 1; load()" />
      <el-select v-model="enabledFilter" style="width: 120px" @change="page = 1; load()">
        <el-option :value="-1" label="全部状态" />
        <el-option :value="1" label="已启用" />
        <el-option :value="0" label="已禁用" />
      </el-select>
      <div class="spacer" />
      <el-button type="primary" @click="importDlg = true">导入书源</el-button>
      <el-button @click="openRepos">书源广场</el-button>
      <el-button :loading="verifying" @click="verifyPage">校验本页</el-button>
      <el-button v-if="batchMode" :disabled="!selected.length" type="primary" @click="batchAction('enable')">
        启用 ({{ selected.length }})
      </el-button>
      <el-button v-if="batchMode" :disabled="!selected.length" type="warning" @click="batchAction('disable')">
        禁用 ({{ selected.length }})
      </el-button>
      <el-button v-if="batchMode" :disabled="!selected.length" type="danger" @click="batchAction('delete')">
        删除 ({{ selected.length }})
      </el-button>
      <el-button v-if="batchMode" @click="selectAllPage">全选本页</el-button>
      <el-button :type="batchMode ? 'warning' : 'default'" :plain="!batchMode" @click="toggleBatch">
        {{ batchMode ? '退出批量' : '批量管理' }}
      </el-button>
      <el-button type="warning" @click="openCleanup">一键清理失效源</el-button>
      <el-button @click="exportAll">导出全部</el-button>
    </div>

    <el-table :data="items" v-loading="loading" style="width: 100%">
      <el-table-column v-if="batchMode" width="45">
        <template #default="{ row }">
          <el-checkbox :model-value="isSelected(row.bookSourceUrl)" @change="toggleSelect(row.bookSourceUrl)" />
        </template>
      </el-table-column>
      <el-table-column label="名称" min-width="150">
        <template #default="{ row }">
          <strong>{{ row.bookSourceName }}</strong>
          <el-tag v-if="row.custom" size="small" style="margin-left: 6px">自定义</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="bookSourceGroup" label="分组" min-width="110" show-overflow-tooltip />
      <el-table-column label="健康" width="110">
        <template #default="{ row }">
          <el-tag v-if="row.verified" size="small" type="success">可用 {{ fmtMs(row.respondTime) }}</el-tag>
          <el-tooltip v-else-if="row.verifyErr" :content="row.verifyErr">
            <el-tag size="small" type="danger">异常</el-tag>
          </el-tooltip>
          <span v-else class="muted">未校验</span>
        </template>
      </el-table-column>
      <el-table-column prop="bookSourceUrl" label="源地址" min-width="210" show-overflow-tooltip />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-switch :model-value="row.enabled" @change="toggle(row)" />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="150">
        <template #default="{ row }">
          <el-button link type="primary" @click="openDebug(row)">调试</el-button>
          <el-button link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-pagination
      v-model:current-page="page"
      :page-size="pageSize"
      :total="total"
      layout="prev, pager, next, total"
      style="margin-top: 14px; justify-content: flex-end"
      @current-change="load"
    />

    <el-dialog v-model="importDlg" title="导入书源" width="560">
      <el-form label-width="90">
        <el-form-item label="书源 URL">
          <el-input v-model="importURL" placeholder="https://…/bookSources.json 或 legado://import/bookSource?src=…" />
        </el-form-item>
        <el-form-item label="粘贴 JSON">
          <el-input v-model="importText" type="textarea" :rows="8" placeholder='[{"bookSourceUrl":…,"bookSourceName":…}]' />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importDlg = false">取消</el-button>
        <el-button type="primary" :loading="importing" @click="doImport">导入</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="reposDlg" title="书源广场（实时同步仓库）" width="760" @closed="stopSyncPoll">
      <!-- 发现状态 -->
      <div v-if="providersLoading" style="padding: 30px 0; text-align: center">
        <el-icon class="is-loading" :size="26"><Loading /></el-icon>
        <div class="muted" style="margin-top: 10px">正在从仓库站点动态发现可用书源仓库…</div>
      </div>
      <div v-else-if="providersErr" style="text-align: center; padding: 20px 0">
        <div style="color: var(--el-color-danger); margin-bottom: 12px">{{ providersErr }}</div>
        <el-button size="small" @click="loadRepos(false)">重试</el-button>
      </div>
      <template v-else>
        <el-tabs v-model="activeProvider">
          <el-tab-pane
            v-for="p in providers"
            :key="p.key"
            :name="p.key"
            :label="p.label + (p.err ? '（失败）' : `（${p.repos.length}）`)"
          >
            <div v-if="p.err" style="text-align: center; padding: 20px 0">
              <div style="color: var(--el-color-danger); margin-bottom: 12px">发现失败：{{ p.err }}</div>
              <el-button size="small" @click="loadRepos(true)">重试</el-button>
            </div>
            <template v-else>
              <div style="display: flex; gap: 10px; margin-bottom: 10px; align-items: center">
                <el-checkbox v-if="p.key === 'yiove'" v-model="repoCleanup">同步时清理本仓库旧源（先清后导，保持与站点一致）</el-checkbox>
                <div class="spacer" />
                <span class="muted" style="font-size: 12px">
                  来自 <code>{{ p.root }}</code>（运行时动态发现{{ p.cached ? ' · 缓存' : '' }}{{ p.elapsedMs ? ` · ${fmtMs(p.elapsedMs)}` : '' }}）
                </span>
                <el-button size="small" @click="loadRepos(true)">刷新</el-button>
                <el-button
                  v-if="p.key === 'yiove'"
                  size="small" type="primary" plain
                  :disabled="!selectedRepos.length || !!syncJob?.progress.running"
                  @click="syncSelectedRepos"
                >批量同步选中 ({{ selectedRepos.length }})</el-button>
              </div>
              <el-table
                :data="p.repos"
                max-height="320"
                size="small"
                @selection-change="(rows: SourceRepo[]) => selectedRepos = rows"
              >
                <el-table-column v-if="p.key === 'yiove'" type="selection" width="42" />
                <el-table-column prop="name" label="仓库" min-width="150" show-overflow-tooltip />
                <el-table-column prop="note" label="说明" min-width="180" show-overflow-tooltip />
                <el-table-column label="操作" width="110">
                  <template #default="{ row }">
                    <el-button
                      size="small" type="primary" plain
                      :disabled="!!syncJob?.progress.running"
                      @click="syncRepo(row, repoCleanup && p.key === 'yiove')"
                    >同步</el-button>
                  </template>
                </el-table-column>
              </el-table>
            </template>
          </el-tab-pane>
        </el-tabs>

        <!-- 同步进度面板 -->
        <div v-if="syncJob" class="sync-progress">
          <div style="display: flex; align-items: center; gap: 12px">
            <div style="flex: 1">
              <div style="font-weight: 600; margin-bottom: 6px">
                正在同步「{{ syncJob.progress.repoName }}」
                <span class="muted" style="font-weight: 400">
                  {{ syncJob.progress.done }}/{{ syncJob.progress.total || '?' }}
                  · 新增 {{ syncJob.progress.inserted }} · 更新 {{ syncJob.progress.updated }}
                </span>
              </div>
              <el-progress
                :percentage="syncJob.progress.total ? Math.round((syncJob.progress.done / syncJob.progress.total) * 100) : 10"
                :stroke-width="8"
              />
            </div>
            <el-button v-if="syncJob.progress.running" size="small" @click="cancelSync">取消</el-button>
          </div>
        </div>

        <p class="muted" style="margin-top: 10px">
          仓库列表在打开广场时从各站点实时发现（站点仅以根地址内置，条目动态获取，站点换源无需更新软件）。
          同步按 URL 去重合并；大合集为异步任务，可随时取消。
        </p>
      </template>
    </el-dialog>

    <el-dialog v-model="cleanupDlg" title="一键清理失效书源" width="680">
      <el-form label-width="110" style="margin-bottom: 8px">
        <el-form-item label="校验范围">
          <el-radio-group v-model="cleanupScope">
            <el-radio value="enabled">仅已启用的源</el-radio>
            <el-radio value="all">全部书源</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="失效处理">
          <el-radio-group v-model="cleanupMode">
            <el-radio value="disable">禁用（可恢复）</el-radio>
            <el-radio value="delete">直接删除</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <!-- 校验进行中：实时进度 -->
      <div v-if="cleaning" style="padding: 12px 0 4px">
        <div style="display: flex; align-items: center; gap: 14px; margin-bottom: 10px">
          <el-progress
            type="dashboard"
            :percentage="cleanupProgress?.total ? Math.round((cleanupProgress.checked / cleanupProgress.total) * 100) : 0"
            :width="76"
            :stroke-width="8"
            :show-text="false"
          />
          <div style="flex: 1">
            <div style="font-size: 15px; font-weight: 600; margin-bottom: 4px">
              {{ cleanupProgress?.checked ?? 0 }} / {{ cleanupProgress?.total ?? 0 }} 源
            </div>
            <div class="muted" style="display: flex; gap: 14px">
              <span style="color: var(--bs-success)">✓ 可用 {{ cleanupProgress?.alive ?? 0 }}</span>
              <span style="color: var(--bs-danger)">✗ 失效 {{ cleanupProgress?.dead ?? 0 }}</span>
              <span v-if="cleanupMode === 'disable'">已禁用 {{ cleanupProgress?.disabled ?? 0 }}</span>
              <span v-else-if="cleanupMode === 'delete'">已删除 {{ cleanupProgress?.deleted ?? 0 }}</span>
            </div>
          </div>
          <el-button size="small" @click="cancelCleanup">取消</el-button>
        </div>
        <el-progress
          :percentage="cleanupProgress?.total ? Math.round((cleanupProgress.checked / cleanupProgress.total) * 100) : 0"
          :stroke-width="10"
          :show-text="false"
        />
        <p class="muted" style="margin: 8px 0 0">
          并发 16 逐源试搜索校验，失效源将自动{{ cleanupMode === 'delete' ? '删除' : '禁用' }}…
        </p>
      </div>
      <!-- 校验完成：结果 -->
      <template v-else-if="cleanupResult">
        <el-alert
          :type="cleanupResult.dead > 0 ? 'warning' : 'success'"
          :closable="false"
          style="margin-bottom: 12px"
          :title="`共校验 ${cleanupResult.checked} 个源：可用 ${cleanupResult.alive} · 失效 ${cleanupResult.dead} · 耗时 ${(cleanupResult.elapsedMs / 1000).toFixed(1)}s`"
        />
        <el-table :data="cleanupResult.results.filter((r) => !r.ok)" max-height="320">
          <el-table-column prop="name" label="失效源" min-width="150" />
          <el-table-column prop="err" label="原因" min-width="240" show-overflow-tooltip />
          <el-table-column label="耗时" width="90">
            <template #default="{ row }">{{ fmtMs(row.elapsedMs) }}</template>
          </el-table-column>
        </el-table>
      </template>
      <template #footer>
        <el-button @click="cleanupDlg = false">关闭</el-button>
        <el-button type="warning" :disabled="cleaning" @click="runCleanup">{{ cleaning ? "校验中…" : "开始校验" }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="verifyDlg" title="书源校验结果" width="640">
      <el-table :data="verifyResults" max-height="420">
        <el-table-column prop="name" label="书源" min-width="150" />
        <el-table-column label="结果" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.ok ? 'success' : 'danger'">{{ row.ok ? '可用' : '失败' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ fmtMs(row.elapsedMs) }}</template>
        </el-table-column>
        <el-table-column prop="err" label="错误" min-width="240" show-overflow-tooltip />
      </el-table>
    </el-dialog>

    <el-dialog v-model="debugDlg" :title="`书源调试 · ${debugSource?.bookSourceName ?? ''}`" width="680">
      <div v-loading="debugLoading" style="min-height: 120px">
        <template v-if="debugTrace">
          <el-alert
            v-if="debugTrace.err"
            type="error"
            :title="debugTrace.err"
            :closable="false"
            style="margin-bottom: 12px"
          />
          <div v-for="(st, i) in debugTrace.steps" :key="i" class="debug-step">
            <el-tag size="small" :type="st.ok ? 'success' : 'danger'">{{ st.ok ? '✓' : '✗' }}</el-tag>
            <strong style="margin: 0 8px">{{ st.name }}</strong>
            <span class="muted">{{ fmtMs(st.elapsedMs) }}</span>
            <div class="muted" style="margin: 4px 0 10px 34px; word-break: break-all">{{ st.detail }}</div>
          </div>
          <div v-if="debugTrace.items?.length" class="muted" style="margin-top: 6px">
            命中样例：{{ debugTrace.items.slice(0, 5).map((i) => i.name).join('、') }}
          </div>
        </template>
      </div>
      <p class="muted" style="margin-top: 10px">
        调试会对该源执行一次完整搜索（含目录与正文抽样），用于定位规则失效原因。
      </p>
    </el-dialog>
  </div>
</template>

<style scoped>
.debug-step {
  line-height: 1.6;
}
</style>

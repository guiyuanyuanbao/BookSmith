// 全局设置 store：统一加载/保存/应用设置，所有页面消费同一份配置。
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { api } from '@/api'

export type ThemeKey = 'green' | 'blue' | 'purple' | 'sepia' | 'dark'

export const THEMES: { key: ThemeKey; label: string; primary: string; bg: string; sidebar: [string, string] }[] = [
  { key: 'green', label: '墨绿', primary: '#2f6f5e', bg: '#f7f5f0', sidebar: ['#24443a', '#1c362e'] },
  { key: 'blue', label: '靛蓝', primary: '#3a6ea5', bg: '#f4f6fa', sidebar: ['#24384f', '#1a2a3c'] },
  { key: 'purple', label: '黛紫', primary: '#6b5b95', bg: '#f7f5f9', sidebar: ['#352d4a', '#272138'] },
  { key: 'sepia', label: '书香棕', primary: '#8b6f47', bg: '#faf6ef', sidebar: ['#443624', '#33291a'] },
  { key: 'dark', label: '玄墨', primary: '#8fa3b0', bg: '#2b2f36', sidebar: ['#1e2227', '#171a1e'] },
]

const STORAGE_KEY = 'booksmith_settings'

interface SettingsState {
  theme: ThemeKey
  concurrency: number
  defaultFormats: string[]
  bookView: 'grid' | 'list'
  // 目录探测参数（比源对话框）
  probeBatchSize: number
  probeMaxSources: number
  probeStopHealthy: number
}

const defaults: SettingsState = {
  theme: 'green',
  concurrency: 6,
  defaultFormats: ['txt', 'epub'],
  bookView: 'grid',
  probeBatchSize: 6,
  probeMaxSources: 36,
  probeStopHealthy: 3,
}

export const useSettingsStore = defineStore('settings', () => {
  const state = ref<SettingsState>({ ...defaults })
  const loaded = ref(false)

  const theme = computed(() => state.value.theme)
  const concurrency = computed(() => state.value.concurrency)
  const defaultFormats = computed(() => state.value.defaultFormats)
  const bookView = computed(() => state.value.bookView)
  const probeBatchSize = computed(() => state.value.probeBatchSize)
  const probeMaxSources = computed(() => state.value.probeMaxSources)
  const probeStopHealthy = computed(() => state.value.probeStopHealthy)
  const isDark = computed(() => state.value.theme === 'dark')

  /** 应用主题到 CSS 变量（全局生效，含暗色） */
  function applyTheme(t: ThemeKey) {
    const preset = THEMES.find((x) => x.key === t) ?? THEMES[0]
    const root = document.documentElement
    root.style.setProperty('--bs-primary', preset.primary)
    root.style.setProperty('--el-color-primary', t === 'dark' ? '#6f8b9d' : preset.primary)
    root.style.setProperty('--bs-sidebar-from', preset.sidebar[0])
    root.style.setProperty('--bs-sidebar-to', preset.sidebar[1])
    root.setAttribute('data-theme', t)
    if (t === 'dark') {
      root.style.setProperty('--bs-bg', '#22262c')
      root.style.setProperty('--bs-card', '#2b3037')
      root.style.setProperty('--bs-text', '#e6e3dc')
      root.style.setProperty('--bs-text-sub', '#9a968c')
      root.style.setProperty('--bs-border', '#3a4048')
      root.style.setProperty('--el-bg-color', '#2b3037')
      root.style.setProperty('--el-bg-color-page', '#22262c')
      root.style.setProperty('--el-text-color-primary', '#e6e3dc')
      root.style.setProperty('--el-text-color-regular', '#c9c5bc')
      root.style.setProperty('--el-fill-color-light', '#333941')
      root.style.setProperty('--el-border-color', '#3a4048')
    } else {
      root.style.setProperty('--bs-bg', preset.bg)
      root.style.setProperty('--bs-card', '#ffffff')
      root.style.setProperty('--bs-text', '#2c2a26')
      root.style.setProperty('--bs-text-sub', '#8a8578')
      root.style.setProperty('--bs-border', '#ece8df')
      root.style.setProperty('--el-bg-color', '#ffffff')
      root.style.setProperty('--el-bg-color-page', preset.bg)
      root.style.setProperty('--el-text-color-primary', '#2c2a26')
      root.style.setProperty('--el-text-color-regular', '#5c5850')
      root.style.setProperty('--el-fill-color-light', '#f4f1ea')
      root.style.setProperty('--el-border-color', '#ece8df')
    }
  }

  /** 加载：本地缓存优先（瞬时无闪），随后与后端同步 */
  async function load() {
    if (loaded.value) return
    // 1. 本地缓存先应用（避免刷新闪白）
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (raw) {
        const cached = JSON.parse(raw) as Partial<SettingsState>
        state.value = { ...defaults, ...cached }
        applyTheme(state.value.theme)
      } else {
        applyTheme(state.value.theme)
      }
    } catch {
      applyTheme(state.value.theme)
    }
    // 2. 后端配置覆盖（跨设备一致）
    try {
      const s = await api.getSettings()
      state.value = {
        theme: (s.theme as ThemeKey) || defaults.theme,
        concurrency: parseInt(s.concurrency) || defaults.concurrency,
        defaultFormats: (s.defaultFormats || 'txt,epub').split(',').filter(Boolean),
        bookView: (s.bookView as 'grid' | 'list') || defaults.bookView,
        probeBatchSize: parseInt(s.probeBatchSize) || defaults.probeBatchSize,
        probeMaxSources: parseInt(s.probeMaxSources) || defaults.probeMaxSources,
        probeStopHealthy: parseInt(s.probeStopHealthy) || defaults.probeStopHealthy,
      }
      applyTheme(state.value.theme)
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state.value))
    } catch {
      /* 后端不可用时使用本地配置 */
    }
    loaded.value = true
  }

  /** 保存并应用 */
  async function save(patch: Partial<SettingsState>) {
    state.value = { ...state.value, ...patch }
    applyTheme(state.value.theme)
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state.value))
    await api.putSettings({
      theme: state.value.theme,
      concurrency: String(state.value.concurrency),
      defaultFormats: state.value.defaultFormats.join(','),
      bookView: state.value.bookView,
    })
  }

  return { state, loaded, theme, concurrency, defaultFormats, bookView, isDark, probeBatchSize, probeMaxSources, probeStopHealthy, load, save, applyTheme }
})

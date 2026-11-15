import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, type TocProbeResult } from '@/api'
import { useSettingsStore } from '@/stores/settings'

/**
 * 目录探测状态机（比源对话框专用）：
 * - 首次探测按健康阈值即停（probeStopHealthy），或达 probeMaxSources 上限
 * - 「继续探测」从上次游标接续，不受阈值限制，探完剩余全部
 * - 支持随时停止（立即生效，不等整批结束）
 * - reset() 在换书时清空状态（含已探结果与游标）
 *
 * 抽为 composable：比源对话框与（未来的）换源引导共用同一套状态机。
 */
export function useProbe() {
  const settings = useSettingsStore()
  const probeMap = ref<Record<string, TocProbeResult>>({})
  const probing = ref(false)
  const probeTotal = ref(0)
  const probeDone = ref(0)
  const probeCursor = ref(0)
  let probeAborted = false

  function reset() {
    probing.value = false
    probeAborted = false
    probeTotal.value = 0
    probeDone.value = 0
    probeCursor.value = 0
    probeMap.value = {}
  }

  /** 轻量重置：仅清计数与运行标志，保留已探结果与游标（同一本书重开对话框） */
  function softReset() {
    probing.value = false
    probeAborted = false
    probeTotal.value = 0
    probeDone.value = 0
  }

  async function start(origins: { sourceUrl: string; bookUrl: string; respondTime?: number }[], manual = false) {
    if (probing.value) return
    const all = [...origins].sort((a, b) => (a.respondTime || 9999) - (b.respondTime || 9999))
    if (!all.length) return
    const BATCH = Math.max(1, settings.probeBatchSize)
    const MAX_TOTAL = Math.min(Math.max(1, settings.probeMaxSources), all.length)
    const STOP_HEALTHY = Math.max(1, settings.probeStopHealthy)
    let cursor = manual ? probeCursor.value : 0
    if (cursor >= all.length) {
      ElMessage.info('所有来源都已探测过')
      return
    }
    probing.value = true
    probeAborted = false
    try {
      let processed = 0
      let healthyStop = false
      while (cursor < all.length) {
        const batch = all.slice(cursor, cursor + BATCH).map((o) => ({ sourceUrl: o.sourceUrl, bookUrl: o.bookUrl }))
        probeTotal.value = Math.min(cursor + batch.length, all.length)
        for (const src of batch) {
          try {
            const r = await api.probeToc(src.sourceUrl, src.bookUrl)
            probeMap.value[src.sourceUrl] = r
          } catch { /* 单源失败不阻塞 */ }
          probeDone.value++
          processed++
          cursor++
          if (probeAborted) break
          if (!manual) {
            const okCount = Object.values(probeMap.value).filter((x) => x.ok).length
            if (okCount >= STOP_HEALTHY) { healthyStop = true; break }
          }
        }
        if (healthyStop) break
        if (probeAborted || cursor >= all.length) break
        if (!manual) {
          const okCount = Object.values(probeMap.value).filter((x) => x.ok).length
          if (okCount >= STOP_HEALTHY) { healthyStop = true; break }
          if (cursor >= MAX_TOTAL) break
        }
      }
      probeCursor.value = cursor
      if (probeAborted) {
        ElMessage.info(`已停止探测（已探 ${probeDone.value} 个，可点「继续探测」接着探）`)
      } else if (manual) {
        ElMessage.success(`已继续探测 ${processed} 个来源（共探完 ${cursor}/${all.length}）`)
      }
    } catch (e: any) {
      ElMessage.error('探测失败：' + e.message)
    } finally {
      probing.value = false
      probeAborted = false
    }
  }

  function stop() {
    if (probing.value) probeAborted = true
  }

  /** 质量徽章：与同书其他已探测源相对比较（完整/疑似缺章/偏少） */
  function badge(probeMap: Record<string, TocProbeResult>, sourceUrl: string): { label: string; type: 'success' | 'warning' | 'info' } {
    const res = probeMap[sourceUrl]
    if (!res || !res.ok) return { label: '', type: 'info' }
    const totals = Object.values(probeMap).filter((r) => r.ok).map((r) => r.total)
    const maxTotal = Math.max(...totals, 1)
    const lastMentioned = (res.lastChapters?.length ?? 0) > 0
    if (res.total >= maxTotal * 0.9 && lastMentioned) return { label: '· 完整', type: 'success' }
    if (res.total < maxTotal * 0.6) return { label: '· 疑似缺章', type: 'warning' }
    return { label: '· 偏少', type: 'info' }
  }

  return {
    probeMap, probing, probeTotal, probeDone, probeCursor,
    reset, softReset, start, stop, badge,
  }
}

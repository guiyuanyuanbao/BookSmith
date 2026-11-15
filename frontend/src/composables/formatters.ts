/**
 * 共享格式化与映射工具 —— 各视图曾各自重复定义（fmtSize/fmtMs/状态映射/
 * 封面占位色），此处为唯一权威实现。
 */

/** 字节大小 → 可读文本 */
export function fmtSize(n: number): string {
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

/** 毫秒 → 可读时长（探测/请求耗时） */
export function fmtMs(ms?: number): string {
  if (!ms) return '—'
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`
}

/** 任务状态英文键 → 中文标签 */
export const TASK_STATUS_LABELS: Record<string, string> = {
  pending: '等待中',
  running: '进行中',
  paused: '已暂停',
  done: '已完成',
  failed: '失败',
  canceled: '已取消',
  exporting: '导出中',
}

export function taskStatusLabel(st: string): string {
  return TASK_STATUS_LABELS[st] ?? st
}

/** 任务状态语义（文字 + Element Plus tag 类型），供状态胶囊/标签使用 */
export function taskStatusView(s: string): { text: string; type: 'success' | 'warning' | 'danger' | 'info' | 'primary' } {
  switch (s) {
    case 'running': return { text: '下载中', type: 'primary' }
    case 'exporting': return { text: '导出中', type: 'warning' }
    case 'pending': return { text: '排队中', type: 'info' }
    case 'paused': return { text: '已暂停', type: 'info' }
    case 'done': return { text: '已完成', type: 'success' }
    case 'failed': return { text: '失败', type: 'danger' }
    case 'canceled': return { text: '已取消', type: 'info' }
    default: return { text: s, type: 'info' }
  }
}

/** 导出格式 → 语义色（TXT/EPUB/PDF/HTML/MD 徽章共用） */
export const FORMAT_COLORS: Record<string, string> = {
  txt: '#67c23a',
  epub: '#409eff',
  pdf: '#f56c6c',
  html: '#e6a23c',
  md: '#909399',
}

/** 无封面占位底色（按书名稳定散列） */
export const PH_COLORS = ['#7fa89a', '#a58f7a', '#8f9a7a', '#9a7a8f', '#7a8f9a']

export function phColor(name: string): string {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xffff
  return PH_COLORS[h % PH_COLORS.length]
}

/** 书源分组串 → 分组名数组（半角逗号分隔，去空白） */
export function splitGroups(g: string): string[] {
  return (g || '').split(',').map((s) => s.trim()).filter(Boolean)
}

/** 导出格式 → 语义色（单值查询；未知格式回退灰色） */
export function fmtColor(format: string): string {
  return FORMAT_COLORS[format] || '#909399'
}

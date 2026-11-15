<script setup lang="ts">
import { computed } from 'vue'
import { taskStatusView } from '@/composables/formatters'

/**
 * 任务状态胶囊：彩点 + 文字。运行中/导出中带呼吸动画。
 * 统一任务页与下载浮窗的状态呈现（此前两处各自实现且样式不一致）。
 */
const props = defineProps<{ status: string }>()

const view = computed(() => taskStatusView(props.status))
</script>

<template>
  <span class="status-pill" :class="status">
    <i class="dot" />{{ view.text }}
  </span>
</template>

<style scoped>
.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 600;
  padding: 3px 10px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  white-space: nowrap;
}
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--bs-text-sub); flex-shrink: 0; }
.status-pill.running { color: var(--bs-primary); }
.status-pill.running .dot, .status-pill.exporting .dot { background: var(--bs-primary); animation: sp-pulse 1.4s ease infinite; }
.status-pill.exporting { color: var(--bs-primary); }
.status-pill.paused { color: var(--bs-accent); }
.status-pill.paused .dot { background: var(--bs-accent); }
.status-pill.done { color: var(--bs-success); }
.status-pill.done .dot { background: var(--bs-success); }
.status-pill.failed { color: var(--bs-danger); }
.status-pill.failed .dot { background: var(--bs-danger); }
@keyframes sp-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}
</style>

<script setup lang="ts">
import { computed } from 'vue'
import { phColor } from '@/composables/formatters'

/**
 * 封面缩略图：图片加载失败自动隐藏，露出按书名稳定着色的占位字。
 * 书架网格/搜索结果/发现页网格共用（此前三处各自实现占位与兜底）。
 */
const props = defineProps<{
  /** 封面代理地址；null/undefined 时直接显示占位 */
  src: string | null
  /** 书名（占位字与底色散列） */
  name: string
}>()

const emit = defineEmits<{ (e: 'error', ev: Event): void }>()

const ph = computed(() => phColor(props.name))

function onError(ev: Event) {
  ;(ev.target as HTMLImageElement).style.visibility = 'hidden'
  emit('error', ev)
}
</script>

<template>
  <div class="cover-thumb">
    <img v-if="src" :src="src" loading="lazy" @error="onError" />
    <span v-else class="cover-thumb-ph" :style="{ background: ph }">{{ name.slice(0, 1) }}</span>
  </div>
</template>

<style scoped>
.cover-thumb {
  position: relative;
  width: 100%;
  aspect-ratio: 3 / 4;
  border-radius: 8px;
  overflow: hidden;
  background: var(--el-fill-color);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.cover-thumb img { width: 100%; height: 100%; object-fit: cover; }
.cover-thumb-ph { color: #fff; font-size: 22px; }
</style>

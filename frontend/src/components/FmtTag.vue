<script setup lang="ts">
import { computed } from 'vue'
import { FORMAT_COLORS } from '@/composables/formatters'

/**
 * 导出格式徽章：语义色底 + 白字（TXT/EPUB/PDF/HTML/MD）。
 * 书架与导出页共用（此前两处各自维护颜色映射）。
 */
const props = defineProps<{ format: string }>()

const color = computed(() => FORMAT_COLORS[props.format] || '#909399')
</script>

<template>
  <el-tag size="small" :color="color" class="fmt-tag">{{ format.toUpperCase() }}</el-tag>
</template>

<style scoped>
.fmt-tag { color: #fff; border: none; width: 54px; text-align: center; flex-shrink: 0; }
</style>

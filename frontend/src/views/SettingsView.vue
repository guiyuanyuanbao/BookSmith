<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { THEMES, useSettingsStore, type ThemeKey } from '@/stores/settings'

const store = useSettingsStore()
const saving = ref(false)

const concurrency = ref(store.concurrency)
const theme = ref<ThemeKey>(store.theme)
const defaultFormats = ref<string[]>([...store.defaultFormats])
const probeBatchSize = ref(store.probeBatchSize)
const probeMaxSources = ref(store.probeMaxSources)
const probeStopHealthy = ref(store.probeStopHealthy)
const bookView = ref(store.bookView)
const themes = THEMES

function pickTheme(t: ThemeKey) {
  theme.value = t
  store.applyTheme(t) // 即时预览
}

onMounted(() => {
  store.load().then(() => {
    concurrency.value = store.concurrency
    theme.value = store.theme
    defaultFormats.value = [...store.defaultFormats]
    bookView.value = store.bookView
  })
})

async function save() {
  if (defaultFormats.value.length === 0) {
    ElMessage.warning('请至少选择一种默认导出格式')
    return
  }
  if (probeStopHealthy.value > probeMaxSources.value) {
    ElMessage.warning('健康即停阈值不能大于最大探测源数')
    return
  }
  saving.value = true
  try {
    await store.save({
      theme: theme.value,
      concurrency: concurrency.value,
      defaultFormats: [...defaultFormats.value],
      bookView: bookView.value,
      probeBatchSize: probeBatchSize.value,
      probeMaxSources: probeMaxSources.value,
      probeStopHealthy: probeStopHealthy.value,
    })
    ElMessage.success('设置已保存')
  } catch (e: any) {
    ElMessage.error(e.message || '保存失败')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="page" style="max-width: 720px">
    <h1 class="page-title">设置</h1>
    <p class="page-sub">个性化界面与下载行为，保存后立即生效</p>

    <div class="settings-card">
      <h3 class="section-title">外观</h3>
      <el-form label-width="130">
        <el-form-item label="主题色">
          <div class="theme-row">
            <div
              v-for="t in themes"
              :key="t.key"
              class="theme-swatch"
              :class="{ active: theme === t.key }"
              :style="{ background: t.primary }"
              @click="pickTheme(t.key)"
              :title="t.label"
            >
              <span v-if="theme === t.key" class="check">✓</span>
            </div>
          </div>
        </el-form-item>
        <el-form-item label="书架默认视图">
          <el-radio-group v-model="bookView">
            <el-radio value="grid">卡片网格</el-radio>
            <el-radio value="list">单列列表</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>

      <h3 class="section-title">下载</h3>
      <el-form label-width="130">
        <el-form-item label="默认并发数">
          <el-slider v-model="concurrency" :min="1" :max="16" show-input style="max-width: 360px" />
          <div class="muted">遇到限流源时任务会自动退避等待，无需担心过高并发被封</div>
        </el-form-item>
        <el-form-item label="默认导出格式">
          <el-checkbox-group v-model="defaultFormats">
            <el-checkbox value="txt">TXT</el-checkbox>
            <el-checkbox value="epub">EPUB</el-checkbox>
            <el-checkbox value="pdf">PDF</el-checkbox>
            <el-checkbox value="html">HTML</el-checkbox>
            <el-checkbox value="md">Markdown</el-checkbox>
          </el-checkbox-group>
        </el-form-item>
        <el-form-item label="目录探测">
          <div class="probe-grid">
            <div class="probe-item">
              <span class="probe-label">每批探测源数</span>
              <el-input-number v-model="probeBatchSize" :min="1" :max="20" />
            </div>
            <div class="probe-item">
              <span class="probe-label">最多探测源数</span>
              <el-input-number v-model="probeMaxSources" :min="1" :max="100" />
            </div>
            <div class="probe-item">
              <span class="probe-label">健康即停阈值</span>
              <el-input-number v-model="probeStopHealthy" :min="1" :max="20" />
            </div>
          </div>
          <div class="muted">
            比源对话框点「探测目录质量」时：按响应速度每 {{ probeBatchSize }} 个源一批探测，
            探到 {{ probeStopHealthy }} 个健康源即停，最多探测 {{ probeMaxSources }} 个源
          </div>
        </el-form-item>
      </el-form>

      <div style="margin-top: 16px">
        <el-button type="primary" :loading="saving" @click="save">保存设置</el-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings-card {
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: var(--bs-radius);
  padding: 24px 28px;
  box-shadow: var(--bs-shadow);
}
.probe-grid { display: flex; gap: 20px; flex-wrap: wrap; }
.probe-item { display: flex; flex-direction: column; gap: 6px; }
.probe-label { font-size: 12.5px; color: var(--bs-text-sub); }
.section-title {
  font-size: 15px;
  font-weight: 600;
  color: var(--bs-primary);
  margin: 0 0 14px;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--bs-border);
}
.theme-row { display: flex; gap: 14px; }
.theme-swatch {
  width: 44px; height: 44px;
  border-radius: 12px;
  cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  color: #fff; font-weight: 700;
  border: 3px solid transparent;
  transition: transform 0.15s, border-color 0.15s;
}
.theme-swatch:hover { transform: scale(1.08); }
.theme-swatch.active { border-color: var(--bs-text); }
.check { font-size: 18px; text-shadow: 0 1px 2px rgba(0,0,0,0.4); }
</style>

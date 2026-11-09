<!-- 登录页：公网部署时的密钥验证。仅当后端启用鉴权时可达（未启用会自动跳首页）。 -->
<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { api, setApiKey } from '@/api'

const router = useRouter()
const key = ref('')
const loading = ref(false)
const checking = ref(true)

onMounted(async () => {
  // 后端未启用鉴权 → 直接回首页；已启用且已存 key → 静默校验后回首页
  try {
    const s = await api.authStatus()
    if (!s.required) {
      router.replace('/overview')
      return
    }
    const saved = localStorage.getItem('booksmith_api_key')
    if (saved) {
      setApiKey(saved)
      await api.authLogin(saved)
      router.replace('/overview')
      return
    }
  } catch { /* 校验失败 → 显示登录表单 */ }
  checking.value = false
})

async function login() {
  if (!key.value.trim()) {
    ElMessage.warning('请输入访问密钥')
    return
  }
  loading.value = true
  try {
    setApiKey(key.value.trim()) // 先带上 key 调登录接口
    await api.authLogin(key.value.trim())
    ElMessage.success('验证通过')
    router.replace('/overview')
  } catch (e: any) {
    setApiKey('')
    ElMessage.error(e.message || '密钥错误')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div v-if="checking" class="login-card muted">检查鉴权状态…</div>
    <div v-else class="login-card">
      <div class="login-logo">📚</div>
      <h1 class="login-title">BookSmith</h1>
      <p class="login-sub">本站已开启访问保护，请输入访问密钥</p>
      <el-input
        v-model="key"
        type="password"
        placeholder="访问密钥"
        size="large"
        show-password
        @keyup.enter="login"
      />
      <el-button type="primary" size="large" :loading="loading" style="width: 100%; margin-top: 14px" @click="login">
        进入
      </el-button>
      <p class="login-hint muted">密钥由部署者设置（环境变量 BOOKSMITH_API_KEY）</p>
    </div>
  </div>
</template>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bs-bg);
}
.login-card {
  width: 360px;
  background: var(--bs-card);
  border: 1px solid var(--bs-border);
  border-radius: 16px;
  padding: 36px 34px 28px;
  box-shadow: var(--bs-shadow);
  text-align: center;
}
.login-logo { font-size: 44px; }
.login-title {
  font-family: 'Noto Serif SC', serif;
  font-size: 26px;
  margin: 8px 0 4px;
  color: var(--bs-text);
}
.login-sub { color: var(--bs-text-sub); font-size: 13px; margin: 0 0 22px; }
.login-hint { font-size: 12px; margin: 14px 0 0; }
</style>

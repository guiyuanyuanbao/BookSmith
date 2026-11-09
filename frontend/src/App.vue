<script setup lang="ts">
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import { useRoute } from 'vue-router'
import { Reading, Search, Collection, Download, FolderOpened, Filter, DataBoard, Setting } from '@element-plus/icons-vue'
import DownloadPanel from '@/components/DownloadPanel.vue'
import { useTasksStore } from '@/stores/tasks'

const route = useRoute()
const tasksStore = useTasksStore()
tasksStore.start()

// 窄屏自动折叠侧边栏（图标模式），拖宽自动展开
const isNarrow = ref(false)
function checkWidth() {
  isNarrow.value = window.innerWidth < 900
}
onMounted(() => {
  checkWidth()
  window.addEventListener('resize', checkWidth)
})
onBeforeUnmount(() => window.removeEventListener('resize', checkWidth))
const collapsed = computed(() => isNarrow.value)

// JS 驱动的过渡：CSS transition 在懒加载 chunk/长任务下可能不触发 transitionend 导致 out-in 卡死
function onFadeLeave(el: Element, done: () => void) {
  (el as HTMLElement).style.opacity = '0'
  setTimeout(done, 120)
}
function onFadeEnter(el: Element, done: () => void) {
  const h = el as HTMLElement
  h.style.opacity = '0'
  requestAnimationFrame(() => {
    h.style.transition = 'opacity 0.14s ease'
    h.style.opacity = '1'
    setTimeout(done, 140)
  })
}

const navs = computed(() => [
  { path: '/overview', title: '概览', desc: '统计', icon: DataBoard },
  { path: '/search', title: '搜索', desc: '找书', icon: Search },
  { path: '/books', title: '书架', desc: '我的书', icon: Collection },
  { path: '/tasks', title: '任务', desc: tasksStore.serverActiveCount > 0 ? `${tasksStore.serverActiveCount} 个进行中` : '空闲', icon: Download },
  { path: '/files', title: '导出', desc: '电子书', icon: FolderOpened },
  { path: '/sources', title: '书源', desc: '管理', icon: Reading },
  { path: '/rules', title: '净化', desc: '规则', icon: Filter },
  { path: '/settings', title: '设置', desc: '偏好', icon: Setting },
])
</script>

<template>
  <el-container style="height: 100%">
    <el-aside :width="collapsed ? '64px' : '216px'" class="sidebar">
      <div class="logo" :class="{ mini: collapsed }">
        <span class="logo-icon">📚</span>
        <div v-if="!collapsed" class="logo-text">
          <div class="logo-name">BookSmith</div>
          <div class="logo-slogan">书籍工坊</div>
        </div>
      </div>
      <el-menu :default-active="route.path" router class="side-menu" :collapse="collapsed" :collapse-transition="false">
        <el-menu-item v-for="n in navs" :key="n.path" :index="n.path">
          <el-icon><component :is="n.icon" /></el-icon>
          <template #title>
            <span class="nav-title">{{ n.title }}</span>
            <span v-if="!collapsed" class="nav-desc">{{ n.desc }}</span>
          </template>
        </el-menu-item>
      </el-menu>
      <div v-if="!collapsed" class="side-footer">
        <div class="footer-line">基于「开源阅读」规则引擎</div>
        <div class="footer-line">Go 原生实现 · 无桥接</div>
      </div>
    </el-aside>
    <el-main class="main-area">
      <router-view v-slot="{ Component }">
        <transition name="page-fade" mode="out-in" :css="false" @enter="onFadeEnter" @leave="onFadeLeave">
          <component :is="Component" />
        </transition>
      </router-view>
    </el-main>
  </el-container>
  <!-- 全局下载任务浮窗：跨页面持续可见 -->
  <DownloadPanel />
</template>

<style scoped>
.sidebar {
  background: linear-gradient(180deg, var(--bs-sidebar-from, #24443a) 0%, var(--bs-sidebar-to, #1c362e) 100%);
  display: flex;
  flex-direction: column;
  box-shadow: 2px 0 12px rgba(28, 54, 46, 0.15);
}
.logo {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 26px 20px 20px;
}
.logo.mini { justify-content: center; padding: 26px 0 20px; }
.logo-icon { font-size: 30px; filter: drop-shadow(0 2px 4px rgba(0,0,0,0.3)); }
.logo-name {
  color: #f5f2ea;
  font-size: 19px;
  font-weight: 700;
  letter-spacing: 0.5px;
  font-family: 'Noto Serif SC', serif;
}
.logo-slogan { color: #7fa89a; font-size: 11px; margin-top: 2px; }

.side-menu {
  border-right: none;
  background: transparent;
  flex: 1;
  padding: 4px 10px;
}
/* 折叠模式：菜单项居中，隐藏文字 */
.side-menu.el-menu--collapse { padding: 4px 6px; }
.side-menu.el-menu--collapse :deep(.el-menu-item) { padding: 0 !important; justify-content: center; }
.side-menu :deep(.el-menu-item) {
  color: #a3bfb4;
  border-radius: 10px;
  margin-bottom: 4px;
  height: 46px;
}
.side-menu :deep(.el-menu-item.is-active) {
  color: #fff;
  background: rgba(255, 255, 255, 0.1);
  box-shadow: inset 3px 0 0 var(--bs-accent);
}
.side-menu :deep(.el-menu-item:hover) {
  background: rgba(255, 255, 255, 0.06);
}
.nav-title { font-size: 14.5px; font-weight: 500; }
.nav-desc { font-size: 11px; color: #6d9184; margin-left: 8px; }

.side-footer {
  padding: 16px 20px;
  color: #5c7a6e;
  font-size: 11px;
  line-height: 1.8;
}
.footer-line { white-space: nowrap; }

.main-area {
  padding: 0;
  background:
    radial-gradient(ellipse 80% 50% at 50% -10%, rgba(47, 111, 94, 0.05), transparent),
    var(--bs-bg);
  overflow-y: auto;
}

/* 页面切换过渡：out-in 顺序执行（避免两页叠放高度畸变）；enter 只淡入不位移（避免重排感） */
/* 页面切换由 JS 钩子驱动（onFadeEnter/onFadeLeave），CSS 规则仅作兜底 */
</style>

import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/overview', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: '概览' } },
    { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { title: '设置' } },
    { path: '/search', name: 'search', component: () => import('@/views/SearchView.vue'), meta: { title: '聚合搜索' } },
    { path: '/sources', name: 'sources', component: () => import('@/views/SourcesView.vue'), meta: { title: '书源管理' } },
    { path: '/rules', name: 'rules', component: () => import('@/views/RulesView.vue'), meta: { title: '净化规则' } },
    { path: '/books', name: 'books', component: () => import('@/views/BooksView.vue'), meta: { title: '书架' } },
    { path: '/tasks', name: 'tasks', component: () => import('@/views/TasksView.vue'), meta: { title: '下载任务' } },
    { path: '/files', name: 'files', component: () => import('@/views/FilesView.vue'), meta: { title: '导出文件' } },
    { path: '/login', name: 'login', component: () => import('@/views/LoginView.vue'), meta: { title: '登录' } },
  ],
})

router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · BookSmith` : 'BookSmith'
})

export default router

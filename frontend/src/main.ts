import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import App from './App.vue'
import router from './router'
import './styles.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(ElementPlus, { locale: zhCn })

// 启动即加载设置（主题/并发/默认格式），避免首屏主题闪变
import { useSettingsStore } from '@/stores/settings'
useSettingsStore().load()

app.mount('#app')

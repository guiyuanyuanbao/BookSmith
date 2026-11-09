<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, type ReplaceRule } from '@/api'

const rules = ref<ReplaceRule[]>([])
const loading = ref(false)
const importDlg = ref(false)
const importURL = ref('')
const importText = ref('')
const importing = ref(false)
const editDlg = ref(false)
const editing = ref<Partial<ReplaceRule>>({})

// 规则测试
const testDlg = ref(false)
const testForm = ref({ pattern: '', replacement: '', isRegex: true, text: '' })
const testResult = ref('')
const testLoading = ref(false)

function openTest(rule?: Partial<ReplaceRule>) {
  testForm.value = {
    pattern: rule?.pattern ?? '',
    replacement: rule?.replacement ?? '',
    isRegex: rule?.isRegex ?? true,
    text: '这是测试文本，包含广告内容与多余空格  。',
  }
  testResult.value = ''
  testDlg.value = true
}

async function runTest() {
  if (!testForm.value.pattern || !testForm.value.text) {
    ElMessage.warning('匹配内容与测试文本不能为空')
    return
  }
  testLoading.value = true
  try {
    const r = await api.testReplace(testForm.value)
    testResult.value = r.result
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    testLoading.value = false
  }
}

async function load() {
  loading.value = true
  try {
    rules.value = (await api.listReplace(false)) ?? []
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}

async function doImport() {
  importing.value = true
  try {
    const r = await api.importReplace({
      url: importURL.value.trim() || undefined,
      text: importText.value.trim() || undefined,
    })
    ElMessage.success(`导入完成：新增 ${r.inserted}`)
    importDlg.value = false
    importURL.value = ''
    importText.value = ''
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  } finally {
    importing.value = false
  }
}

function openEdit(rule?: ReplaceRule) {
  editing.value = rule
    ? { ...rule }
    : {
        name: '',
        pattern: '',
        replacement: '',
        isRegex: true,
        isEnabled: true,
        order: (rules.value?.length ?? 0) + 1,
        scopeContent: true,
        scopeTitle: false,
      }
  editDlg.value = true
}

async function save() {
  const r = editing.value
  if (!r.name || !r.pattern) {
    ElMessage.warning('名称与匹配内容必填')
    return
  }
  // 正则合法性预检：用后端测试端点验证（非法正则在下载时才会爆炸，提前拦截）
  if (r.isRegex) {
    try {
      await api.testReplace({ pattern: r.pattern, replacement: r.replacement || '', isRegex: true, text: '预检文本' })
    } catch (e: any) {
      ElMessage.error(`正则表达式无效：${e.message}`)
      return
    }
  }
  try {
    await api.saveReplace(r)
    ElMessage.success('已保存')
    editDlg.value = false
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

async function remove(rule: ReplaceRule) {
  try {
    await ElMessageBox.confirm(`删除净化规则「${rule.name}」？`, '确认', { type: 'warning' })
  } catch {
    return
  }
  try {
    await api.deleteReplace(rule.id)
    ElMessage.success('已删除')
    load()
  } catch (e: any) {
    ElMessage.error(e.message)
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h1 class="page-title">净化规则</h1>
    <p class="page-sub">兼容「阅读」APP 替换净化规则（正则 / 纯文本 / JS 三模式）；可导入乌云净化等社区规则库</p>

    <div class="toolbar">
      <div class="spacer" />
      <el-button type="primary" @click="openEdit()">新建规则</el-button>
      <el-button @click="openTest()">测试规则</el-button>
      <el-button @click="importDlg = true">导入规则</el-button>
    </div>

    <el-table :data="rules" v-loading="loading">
      <el-table-column prop="order" label="顺序" width="70" />
      <el-table-column prop="name" label="名称" min-width="140" show-overflow-tooltip />
      <el-table-column prop="pattern" label="匹配" min-width="200" show-overflow-tooltip />
      <el-table-column prop="replacement" label="替换为" min-width="140" show-overflow-tooltip>
        <template #default="{ row }">
          <span v-if="String(row.replacement || '').toLowerCase().startsWith('@js:')" class="muted">JS 替换</span>
          <span v-else>{{ row.replacement || '(删除)' }}</span>
        </template>
      </el-table-column>
      <el-table-column label="类型" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.isRegex ? 'warning' : 'info'">{{ row.isRegex ? '正则' : '文本' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="作用" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.scopeTitle ? 'success' : 'primary'">
            {{ row.scopeTitle ? '标题' : '正文' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="130">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="importDlg" title="导入净化规则" width="560">
      <el-form label-width="90">
        <el-form-item label="规则 URL">
          <el-input v-model="importURL" placeholder="https://…/replaceRules.json" />
        </el-form-item>
        <el-form-item label="粘贴 JSON">
          <el-input v-model="importText" type="textarea" :rows="8" placeholder='[{"name":…,"pattern":…,"replacement":…}]' />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importDlg = false">取消</el-button>
        <el-button type="primary" :loading="importing" @click="doImport">导入</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="testDlg" title="测试净化规则" width="560">
      <el-form label-width="90">
        <el-form-item label="匹配内容">
          <el-input v-model="testForm.pattern" />
        </el-form-item>
        <el-form-item label="替换为">
          <el-input v-model="testForm.replacement" placeholder="留空为删除" />
        </el-form-item>
        <el-form-item label="正则模式">
          <el-switch v-model="testForm.isRegex" />
        </el-form-item>
        <el-form-item label="测试文本">
          <el-input v-model="testForm.text" type="textarea" :rows="3" />
        </el-form-item>
        <el-form-item label="结果">
          <el-input v-model="testResult" type="textarea" :rows="3" readonly placeholder="点击「执行测试」查看替换结果" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="testDlg = false">关闭</el-button>
        <el-button type="primary" :loading="testLoading" @click="runTest">执行测试</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editDlg" :title="editing.id ? '编辑规则' : '新建规则'" width="620">
      <el-form label-width="100">
        <el-form-item label="名称">
          <el-input v-model="editing.name" />
        </el-form-item>
        <el-form-item label="匹配内容">
          <el-input v-model="editing.pattern" placeholder="正则表达式或纯文本" />
        </el-form-item>
        <el-form-item label="替换为">
          <el-input v-model="editing.replacement" placeholder="留空为删除；支持 $1 引用；@js: 开头为 JS" />
        </el-form-item>
        <el-form-item label="正则模式">
          <el-switch v-model="editing.isRegex" />
        </el-form-item>
        <el-form-item label="作用于正文">
          <el-switch v-model="editing.scopeContent" />
        </el-form-item>
        <el-form-item label="作用于标题">
          <el-switch v-model="editing.scopeTitle" />
        </el-form-item>
        <el-form-item label="作用范围">
          <el-input v-model="editing.scope" placeholder="书名或书源 URL 子串，分号分隔；留空全局生效" />
        </el-form-item>
        <el-form-item label="排除范围">
          <el-input v-model="editing.excludeScope" placeholder="命中的书不应用此规则" />
        </el-form-item>
        <el-form-item label="顺序">
          <el-input-number v-model="editing.order" :min="0" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="openTest(editing)">测试</el-button>
        <el-button @click="editDlg = false">取消</el-button>
        <el-button type="primary" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

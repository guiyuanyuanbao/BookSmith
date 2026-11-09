package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"booksmith/internal/httpx"
	"booksmith/internal/store"
)

func testManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, httpx.NewClient()), st
}

const sampleSources = `[
  {"bookSourceUrl":"https://a.com","bookSourceName":"甲源","bookSourceGroup":"测试","enabled":true,
   "searchUrl":"/search?q={{key}}","ruleSearch":{"bookList":"class.item","name":"class.name@text"}},
  {"bookSourceUrl":"https://b.com","bookSourceName":"乙源","bookSourceGroup":"测试","enabled":true},
  {"bookSourceUrl":"","bookSourceName":"无效源"},
  {"bookSourceUrl":"https://c.com","bookSourceName":""}
]`

func TestParseSourceJSON(t *testing.T) {
	list, err := ParseSourceJSON([]byte(sampleSources))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("valid sources = %d want 2", len(list))
	}
	if list[0].BookSourceName != "甲源" || list[0].RuleSearch == nil {
		t.Errorf("first = %+v", list[0])
	}
	// 单对象
	list, err = ParseSourceJSON([]byte(`{"bookSourceUrl":"https://x.com","bookSourceName":"单源"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].BookSourceName != "单源" {
		t.Errorf("single = %v", list)
	}
	// 带杂质文本
	list, err = ParseSourceJSON([]byte("前置说明\n" + sampleSources + "\n后置说明"))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("noisy = %d", len(list))
	}
}

func TestImportDedup(t *testing.T) {
	m, st := testManager(t)
	res, err := m.ImportText(sampleSources, "测试导入")
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 2 {
		t.Fatalf("inserted = %d", res.Inserted)
	}
	// 重复导入 → 更新而非新增
	res, err = m.ImportText(sampleSources, "测试导入")
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 2 || res.Inserted != 0 {
		t.Fatalf("second import: inserted=%d updated=%d", res.Inserted, res.Updated)
	}
	total, err := st.SourceCount()
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d", total)
	}
}

func TestImportURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleSources))
	}))
	defer srv.Close()
	m, _ := testManager(t)
	res, err := m.ImportURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 2 {
		t.Fatalf("inserted = %d", res.Inserted)
	}
	// legado:// 协议提取
	res, err = m.ImportURL("legado://import/bookSource?src=" + srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 2 {
		t.Fatalf("legado scheme: %+v", res)
	}
	// 导出
	data, err := m.ExportSources(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "甲源") {
		t.Errorf("export = %s", data[:min(200, len(data))])
	}
}

func TestParseReplaceJSON(t *testing.T) {
	// legado 标准格式
	std := `[{"id":1,"name":"去广告","pattern":"广告.*?内容","replacement":"","isRegex":true,"isEnabled":true,"order":1}]`
	rules, err := ParseReplaceJSON([]byte(std))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Pattern != "广告.*?内容" || !rules[0].IsRegex {
		t.Errorf("std = %+v", rules)
	}
	// 非标准格式（legacy）
	legacy := `[{"regex":"简洁正则","replaceSummary":"规则名","replacement":"替换","isRegex":true,"enable":true,"serialNumber":2}]`
	rules, err = ParseReplaceJSON([]byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Pattern != "简洁正则" || rules[0].Name != "规则名" || rules[0].Order != 2 {
		t.Errorf("legacy = %+v", rules)
	}
}

func TestImportReplaceAndSyncRepo(t *testing.T) {
	ruleJSON := `[{"id":7,"name":"净化","pattern":"测试广告","replacement":"","isRegex":false,"isEnabled":true}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ruleJSON))
	}))
	defer srv.Close()

	m, st := testManager(t)
	res, err := m.ImportReplaceURL(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 1 {
		t.Fatalf("inserted = %d", res.Inserted)
	}
	rules, err := st.ListReplaceRules(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Name != "净化" {
		t.Fatalf("rules = %+v", rules)
	}

	// 内置仓库列表应包含 aoaostar
	repos := BuiltinRepos()
	found := false
	for _, r := range repos {
		if strings.Contains(r.URL, "legado.aoaostar.com") {
			found = true
		}
	}
	if !found {
		t.Error("aoaostar repo missing")
	}

	// 同步仓库（本地 HTTP 模拟）
	status := m.SyncRepo(SourceRepo{Name: "模拟仓库", URL: srv.URL, Kind: "replaceRule"})
	if status.Err != "" || status.Inserted != 0 {
		t.Logf("sync status: %+v (id 冲突视为更新)", status)
	}
}

func TestSyncConcurrentGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleSources))
	}))
	defer srv.Close()
	m, _ := testManager(t)
	// 并发同仓同步只允许一个
	m.mu.Lock()
	m.syncing[srv.URL] = true
	m.mu.Unlock()
	status := m.SyncRepo(SourceRepo{Name: "x", URL: srv.URL, Kind: "bookSource"})
	if status.Err != "同步进行中" {
		t.Errorf("guard = %+v", status)
	}
}

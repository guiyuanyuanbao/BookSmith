// Package source 实现书源与净化规则的管理：
// 导入（URL/文件/文本）、去重、校验、导出、内置仓库（aoaostar 等）实时同步。
package source

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"booksmith/internal/httpx"
	"booksmith/internal/model"
	"booksmith/internal/store"
)

// Manager 书源管理器。
type Manager struct {
	st      *store.Store
	client  *httpx.Client
	mu      sync.Mutex
	syncing map[string]bool
}

// New 创建管理器。
func New(st *store.Store, client *httpx.Client) *Manager {
	return &Manager{st: st, client: client, syncing: map[string]bool{}}
}

// ParseSourceJSON 解析书源 JSON：支持单对象、数组、以及外层再包裹的字符串形式。
func ParseSourceJSON(data []byte) ([]*model.BookSource, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("书源内容为空")
	}
	var arr []*model.BookSource
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		return filterValidSources(arr), nil
	}
	var single model.BookSource
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil && single.BookSourceURL != "" {
		return filterValidSources([]*model.BookSource{&single}), nil
	}
	// legado 导出可能带 BOM 或前后缀文字，尝试截取首个 [ ... ]
	start := strings.Index(trimmed, "[")
	end := strings.LastIndex(trimmed, "]")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &arr); err == nil {
			return filterValidSources(arr), nil
		}
	}
	return nil, fmt.Errorf("无法解析书源 JSON")
}

// filterValidSources 过滤无效书源（必须有 URL 与名称）。
func filterValidSources(list []*model.BookSource) []*model.BookSource {
	out := make([]*model.BookSource, 0, len(list))
	for _, bs := range list {
		if bs == nil {
			continue
		}
		if strings.TrimSpace(bs.BookSourceURL) == "" || strings.TrimSpace(bs.BookSourceName) == "" {
			continue
		}
		out = append(out, bs)
	}
	return out
}

// ParseReplaceJSON 解析净化规则 JSON（兼容 legado ReplaceRule 与非标准字段名）。
func ParseReplaceJSON(data []byte) ([]*model.ReplaceRule, error) {
	trimmed := strings.TrimSpace(string(data))
	var arr []*model.ReplaceRule
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil && len(arr) > 0 {
		// 标准格式：至少一条有 pattern 才直接返回（否则可能是 legacy 字段名）
		meaningful := false
		for _, r := range arr {
			if r != nil && r.Pattern != "" {
				meaningful = true
				break
			}
		}
		if meaningful {
			for _, r := range arr {
				if r.ID == 0 {
					r.ID = time.Now().UnixNano()
				}
			}
			return arr, nil
		}
	}
	var single model.ReplaceRule
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil && single.Pattern != "" {
		if single.ID == 0 {
			single.ID = time.Now().UnixNano()
		}
		return []*model.ReplaceRule{&single}, nil
	}
	// 非标准格式：regex/replacement/isRegex/enable/serialNumber
	var legacy []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &legacy); err == nil {
		legacyOut := make([]*model.ReplaceRule, 0, len(legacy))
		for _, m := range legacy {
			r := &model.ReplaceRule{IsRegex: true, Enabled: true, ID: time.Now().UnixNano()}
			if v, ok := m["regex"].(string); ok {
				r.Pattern = v
			}
			if v, ok := m["pattern"].(string); ok {
				r.Pattern = v
			}
			if v, ok := m["replacement"].(string); ok {
				r.Replacement = v
			}
			if v, ok := m["name"].(string); ok {
				r.Name = v
			} else if v, ok := m["replaceSummary"].(string); ok {
				r.Name = v
			}
			if v, ok := m["isRegex"].(bool); ok {
				r.IsRegex = v
			}
			if v, ok := m["enable"].(bool); ok {
				r.Enabled = v
			} else if v, ok := m["isEnabled"].(bool); ok {
				r.Enabled = v
			}
			if v, ok := m["serialNumber"].(float64); ok {
				r.Order = int(v)
			} else if v, ok := m["order"].(float64); ok {
				r.Order = int(v)
			}
			if v, ok := m["scope"].(string); ok {
				r.Scope = v
			}
			if r.Pattern != "" {
				legacyOut = append(legacyOut, r)
			}
		}
		if len(legacyOut) > 0 {
			return legacyOut, nil
		}
	}
	return nil, fmt.Errorf("无法解析净化规则 JSON")
}

// ImportResult 导入结果。
type ImportResult struct {
	Total    int      `json:"total"`
	Inserted int      `json:"inserted"`
	Updated  int      `json:"updated"`
	Errors   []string `json:"errors,omitempty"`
}

// ImportText 从文本导入书源（粘贴 JSON）。
func (m *Manager) ImportText(text string, origin string) (*ImportResult, error) {
	sources, err := ParseSourceJSON([]byte(text))
	if err != nil {
		return nil, err
	}
	return m.saveSources(sources, origin, true), nil
}

// ImportURL 从 URL 实时拉取书源（支持 legado://import/bookSource?src= 格式的提取）。
func (m *Manager) ImportURL(url string) (*ImportResult, error) {
	url = strings.TrimSpace(url)
	// legado://import/bookSource?src=https://... → 提取 src
	if i := strings.Index(url, "src="); i >= 0 && strings.HasPrefix(url, "legado://") {
		url = url[i+4:]
	}
	resp, err := m.client.Do(nil, &httpx.Request{URL: url, Timeout: 60 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("拉取书源失败: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("拉取书源失败: HTTP %d", resp.StatusCode)
	}
	text := resp.BodyText()
	sources, err := ParseSourceJSON([]byte(text))
	if err != nil {
		// 可能是 gzip 已解压但仍是压缩字节 — 尝试原始字节解析
		return nil, err
	}
	return m.saveSources(sources, url, false), nil
}

// saveSources 保存书源（以 bookSourceUrl 为主键去重）。
func (m *Manager) saveSources(sources []*model.BookSource, origin string, custom bool) *ImportResult {
	res := &ImportResult{Total: len(sources)}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UnixMilli()
	for _, bs := range sources {
		if origin != "" {
			bs.Origin = origin
		}
		bs.ImportedAt = now
		// 去重判定：已存在则更新（URL 主键天然去重）
		_, err := m.st.GetSource(bs.BookSourceURL)
		if err == store.ErrNotFound {
			res.Inserted++
		} else if err == nil {
			res.Updated++
		}
		if err := m.st.UpsertSource(bs, custom); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", bs.BookSourceName, err))
		}
	}
	return res
}

// ImportReplaceText 导入净化规则。
func (m *Manager) ImportReplaceText(text string) (*ImportResult, error) {
	rules, err := ParseReplaceJSON([]byte(text))
	if err != nil {
		return nil, err
	}
	res := &ImportResult{Total: len(rules)}
	for _, r := range rules {
		if err := m.st.UpsertReplaceRule(r); err != nil {
			res.Errors = append(res.Errors, err.Error())
		} else {
			res.Inserted++
		}
	}
	return res, nil
}

// ImportReplaceURL 从 URL 拉取净化规则。
func (m *Manager) ImportReplaceURL(url string) (*ImportResult, error) {
	resp, err := m.client.Do(nil, &httpx.Request{URL: url, Timeout: 60 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("拉取净化规则失败: %w", err)
	}
	return m.ImportReplaceText(resp.BodyText())
}

// ExportSources 导出书源 JSON（enabledOnly 时仅导出启用源）。
func (m *Manager) ExportSources(enabledOnly bool) ([]byte, error) {
	sources, err := m.st.AllSources(enabledOnly)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(sources, "", "  ")
}

// SourceRepo 内置书源仓库（复用 model 定义）。
type SourceRepo = model.SourceRepo

// BuiltinRepos 内置仓库清单（「阅读」APP 生态实时源，主项为 aoaostar）。
func BuiltinRepos() []SourceRepo {
	return []SourceRepo{
		{Name: "全量书源（aoaostar）", URL: "https://legado.aoaostar.com/sources/b778fe6b.json", Kind: "bookSource", Note: "聚合 3900+ 书源"},
		{Name: "XIU2精品书源", URL: "https://legado.aoaostar.com/sources/71e56d4f.json", Kind: "bookSource", Note: "精选 20+ 稳定源"},
		{Name: "破冰书源", URL: "https://legado.aoaostar.com/sources/4dc410d1.json", Kind: "bookSource"},
		{Name: "关耳女频", URL: "https://legado.aoaostar.com/sources/e3e5d620.json", Kind: "bookSource"},
		{Name: "三舞313书源", URL: "https://legado.aoaostar.com/sources/2a1f129b.json", Kind: "bookSource"},
		{Name: "开源阅读软件书源", URL: "https://legado.aoaostar.com/sources/3bb7b751.json", Kind: "bookSource"},
		{Name: "乌云净化（净化规则）", URL: "https://legado.aoaostar.com/sources/3f067eb2.json", Kind: "replaceRule", Note: "通用广告净化"},
	}
}

// SyncStatus 同步状态。
type SyncStatus struct {
	Repo      string `json:"repo"`
	Total     int    `json:"total"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Err       string `json:"err,omitempty"`
	ElapsedMS int64  `json:"elapsedMs"`
}

// BuiltinRepos 内置仓库列表。
func (m *Manager) BuiltinRepos() []SourceRepo {
	return BuiltinRepos()
}

// SyncRepo 实时同步一个内置仓库。
func (m *Manager) SyncRepo(repo SourceRepo) *SyncStatus {
	m.mu.Lock()
	if m.syncing[repo.URL] {
		m.mu.Unlock()
		return &SyncStatus{Repo: repo.Name, Err: "同步进行中"}
	}
	m.syncing[repo.URL] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.syncing, repo.URL)
		m.mu.Unlock()
	}()

	st := &SyncStatus{Repo: repo.Name}
	start := time.Now()
	defer func() { st.ElapsedMS = time.Since(start).Milliseconds() }()

	resp, err := m.client.Do(nil, &httpx.Request{URL: repo.URL, Timeout: 120 * time.Second})
	if err != nil {
		st.Err = err.Error()
		return st
	}
	if resp.StatusCode != 200 {
		st.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return st
	}
	switch repo.Kind {
	case "replaceRule":
		res, err := m.ImportReplaceText(resp.BodyText())
		if err != nil {
			st.Err = err.Error()
			return st
		}
		st.Total, st.Inserted, st.Updated = res.Total, res.Inserted, res.Updated
	default:
		sources, err := ParseSourceJSON([]byte(resp.BodyText()))
		if err != nil {
			st.Err = err.Error()
			return st
		}
		res := m.saveSources(sources, repo.URL, false)
		st.Total, st.Inserted, st.Updated = res.Total, res.Inserted, res.Updated
	}
	return st
}

// SortSources 按 customOrder/weight 排序（列表展示用）。
func SortSources(list []*model.BookSource) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].CustomOrder != list[j].CustomOrder {
			return list[i].CustomOrder < list[j].CustomOrder
		}
		if list[i].Weight != list[j].Weight {
			return list[i].Weight > list[j].Weight
		}
		return list[i].BookSourceName < list[j].BookSourceName
	})
}

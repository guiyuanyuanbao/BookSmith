// Package source 实现书源与净化规则的管理：
// 导入（URL/文件/文本）、去重、校验、导出、内置仓库（aoaostar 等）实时同步。
package source

import (
	"encoding/json"
	"fmt"
	"regexp"
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

// numericStringFields legado JSON 里语义为数字但部分站点导出为字符串的字段
// （yiove 实测 lastUpdateTime 为字符串），解析前统一归一化为数字。
var numericStringFields = []string{
	"lastUpdateTime", "respondTime", "weight", "customOrder", "bookSourceType",
}

// stringNumberFields 语义为字符串但部分源写成数字的字段（concurrentRate="3/1000"，
// 个别源直接写 1000），解析前统一归一化为字符串。
var stringNumberFields = []string{
	"concurrentRate", "header",
}

// ParseSourceJSON 解析书源 JSON：支持单对象、数组、以及外层再包裹的字符串形式。
// 宽容处理：数字/字符串字段类型混用时（yiove 站点源实测）归一化后重试。
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
	// 字段类型宽容重试（双向归一化）后再次尝试
	normalized := normalizeJSONFields(trimmed)
	if err := json.Unmarshal([]byte(normalized), &arr); err == nil {
		return filterValidSources(arr), nil
	}
	// 兜底：宽松两阶段解析——先 map 全收，再映射（规则字段可能被写成
	// "字符串化 JSON"（双层编码），逐字段二次解码）
	if list, ok := laxUnmarshalSourceList(normalized); ok && len(list) > 0 {
		return filterValidSources(list), nil
	}
	// legado 导出可能带 BOM 或前后缀文字，尝试截取首个 [ ... ]
	start := strings.Index(trimmed, "[")
	end := strings.LastIndex(trimmed, "]")
	if start >= 0 && end > start {
		seg := trimmed[start : end+1]
		if err := json.Unmarshal([]byte(seg), &arr); err == nil {
			return filterValidSources(arr), nil
		}
		if err := json.Unmarshal([]byte(normalizeJSONFields(seg)), &arr); err == nil {
			return filterValidSources(arr), nil
		}
	}
	return nil, fmt.Errorf("无法解析书源 JSON")
}

// numStrRe 匹配 "field":"digits" → 数字字段字符串归一化。
var numStrRe = regexp.MustCompile(`"(` + strings.Join(numericStringFields, "|") + `)"\s*:\s*"(-?\d+)(?:\.0+)?"`)

// strNumRe 匹配 "field":digits（非字符串值）→ 字符串字段数字归一化。
// RE2 无前瞻：捕获后续分隔符，替换时原样写回。
var strNumRe = regexp.MustCompile(`"(` + strings.Join(stringNumberFields, "|") + `)"\s*:\s*(-?\d+(?:\.\d+)?)\s*([,}\]])`)

// sourceTypeStrRe bookSourceType 字符串枚举（yiove 个别源写 "TEXT"）→ 数字。
// TEXT=0 AUDIO=1 IMAGE=2 FILE=3 VIDEO=4（legado 语义）。
var sourceTypeStrRe = regexp.MustCompile(`"bookSourceType"\s*:\s*"(TEXT|AUDIO|IMAGE|FILE|VIDEO)"`)

var sourceTypeEnum = map[string]int{"TEXT": 0, "AUDIO": 1, "IMAGE": 2, "FILE": 3, "VIDEO": 4}

// laxUnmarshalSourceList 宽松两阶段解析：先解为通用 map 列表，再逐字段映射到
// BookSource。规则字段（ruleSearch/ruleBookInfo/ruleContent/ruleExplore/ruleReview）
// 若被站点写成"字符串化 JSON"（双层编码），此处二次解码为结构体——严格 typed 解析
// 会因这类混用整批失败（yiove 单源实测）。二次解码失败的条目丢弃该字段（缺失优于整批失败）。
func laxUnmarshalSourceList(data string) ([]*model.BookSource, bool) {
	var raw []map[string]any
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, false
	}
	ruleKeys := []string{"ruleSearch", "ruleBookInfo", "ruleContent", "ruleExplore", "ruleReview", "ruleToc"}
	for _, item := range raw {
		for _, k := range ruleKeys {
			s, ok := item[k].(string)
			if !ok {
				continue
			}
			trimmed := strings.TrimSpace(s)
			if strings.HasPrefix(trimmed, "{") {
				var obj map[string]any
				if json.Unmarshal([]byte(trimmed), &obj) == nil {
					item[k] = obj
					continue
				}
			}
			// 不是对象也不像 JSON：删除（typed 解析只接受对象）
			delete(item, k)
		}
		// bookSourceType 空串/未知枚举 → 默认文本源 0（typed 解析仅接受数字）
		if v, ok := item["bookSourceType"]; ok {
			switch t := v.(type) {
			case string:
				if n, found := sourceTypeEnum[t]; found {
					item["bookSourceType"] = n
				} else {
					item["bookSourceType"] = 0
				}
			case float64:
				// 已是数字，保留
			default:
				item["bookSourceType"] = 0
			}
		}
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var out []*model.BookSource
	if err := json.Unmarshal(normalized, &out); err != nil {
		return nil, false
	}
	return out, true
}

// normalizeJSONFields 双向归一化数字/字符串字段类型。
func normalizeJSONFields(s string) string {
	s = numStrRe.ReplaceAllString(s, `"$1":$2`)
	s = strNumRe.ReplaceAllString(s, `"$1":"$2"$3`)
	s = sourceTypeStrRe.ReplaceAllStringFunc(s, func(m string) string {
		for k, v := range sourceTypeEnum {
			if strings.Contains(m, `"`+k+`"`) {
				return `"bookSourceType":` + fmt.Sprintf("%d", v)
			}
		}
		return m
	})
	return s
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

// ClearDiscoveryCache 清空仓库发现缓存（前端「刷新」按钮）。
func (m *Manager) ClearDiscoveryCache() {
	discoveryCache.mu.Lock()
	discoveryCache.items = map[string]cachedDiscovery{}
	discoveryCache.mu.Unlock()
}

// SyncStatus 同步状态。
type SyncStatus struct {
	Repo      string `json:"repo"`
	Total     int    `json:"total"`
	Inserted  int    `json:"inserted"`
	Updated   int    `json:"updated"`
	Removed   int    `json:"removed,omitempty"` // 同仓清理移除的失效旧源数
	Err       string `json:"err,omitempty"`
	ElapsedMS int64  `json:"elapsedMs"`
}

// SyncRepo 实时同步一个仓库直链（拉取 → 解析 → 去重入库）。
// cleanup=true 时同步成功后清理该仓库 origin 下已消失的旧源（custom=0，不影响手动导入源）。
func (m *Manager) SyncRepo(repo SourceRepo, cleanup bool) *SyncStatus {
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
	// 同仓清理：上次导入但本次仓库已不再提供的源（仅限本仓库 origin，且非手动导入）
	// 语义为"同步前清理"——先清后导，避免误删刚导入的新源。
	if cleanup && st.Err == "" && repo.Kind != "replaceRule" {
		if n, cerr := m.st.DeleteSourcesByOrigin(repo.URL); cerr == nil && n > 0 {
			st.Removed = int(n)
		}
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

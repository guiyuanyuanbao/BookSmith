package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// settings.go 用户设置持久化（kv 表）。

var defaultSettings = map[string]string{
	"theme":             "green", // green / blue / purple / sepia / dark
	"concurrency":       "6",     // 全局默认下载并发
	"defaultFormats":    "txt,epub",
	"bookView":          "grid", // grid / list
	"sourceConcurrency": "48",
	// 目录探测参数（比源对话框"探测目录质量"）
	"probeBatchSize":   "12", // 每批探测源数
	"probeMaxSources":  "36", // 单次探测最多源数
	"probeStopHealthy": "3",  // 探到几个健康源即停
}

// handleGetSettings 读取全部设置。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for k, def := range defaultSettings {
		if v, ok := s.app.Store.KVGet("setting_" + k); ok && v != "" {
			out[k] = v
		} else {
			out[k] = def
		}
	}
	writeOK(w, out)
}

// handlePutSettings 保存设置（部分键部分更新）。
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var req map[string]string
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	saved := map[string]string{}
	for k, v := range req {
		if _, ok := defaultSettings[k]; !ok {
			continue // 只接受已知键
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !validSetting(k, v) {
			continue // 非法值拒绝写入，保持旧值生效
		}
		if err := s.app.Store.KVSet("setting_"+k, v); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		saved[k] = v
	}
	writeOK(w, saved)
}

var _ = json.Marshal

// handleGetSearchHistory 最近搜索词（最多 10 条）。
func (s *Server) handleGetSearchHistory(w http.ResponseWriter, r *http.Request) {
	v, ok := s.app.Store.KVGet("search_history")
	if !ok || v == "" {
		writeOK(w, []string{})
		return
	}
	var hist []string
	if err := json.Unmarshal([]byte(v), &hist); err != nil {
		writeOK(w, []string{})
		return
	}
	writeOK(w, hist)
}

// handleAddSearchHistory 追加搜索词（去重置顶，截断 10 条）。
func (s *Server) handleAddSearchHistory(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeErr(w, 400, "key 不能为空")
		return
	}
	hist := []string{}
	if v, ok := s.app.Store.KVGet("search_history"); ok && v != "" {
		_ = json.Unmarshal([]byte(v), &hist)
	}
	// 去重置顶
	out := []string{key}
	for _, h := range hist {
		if h != key {
			out = append(out, h)
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	b, _ := json.Marshal(out)
	_ = s.app.Store.KVSet("search_history", string(b))
	writeOK(w, out)
}

// handleClearSearchHistory 清空搜索历史。
func (s *Server) handleClearSearchHistory(w http.ResponseWriter, r *http.Request) {
	_ = s.app.Store.KVSet("search_history", "[]")
	writeOK(w, nil)
}

// validSetting 设置值合法性校验（防止非法值经 API 写入后污染前端运行时）。
func validSetting(k, v string) bool {
	switch k {
	case "theme":
		switch v {
		case "green", "blue", "purple", "sepia", "dark":
			return true
		}
		return false
	case "bookView":
		return v == "grid" || v == "list"
	case "concurrency", "sourceConcurrency":
		if v == "" {
			return false
		}
		for _, c := range v {
			if c < '0' || c > '9' {
				return false
			}
		}
		n := 0
		for _, c := range v {
			n = n*10 + int(c-'0')
		}
		return n >= 1 && n <= 64
	case "defaultFormats":
		for _, f := range strings.Split(v, ",") {
			switch strings.TrimSpace(f) {
			case "txt", "epub", "pdf", "html", "md":
			default:
				return false
			}
		}
		return v != ""
	case "probeBatchSize", "probeMaxSources", "probeStopHealthy":
		if v == "" {
			return false
		}
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return false
			}
			n = n*10 + int(c-'0')
		}
		switch k {
		case "probeBatchSize", "probeStopHealthy":
			return n >= 1 && n <= 20
		default: // probeMaxSources
			return n >= 1 && n <= 100
		}
	}
	return true
}

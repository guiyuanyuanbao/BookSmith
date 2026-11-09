// Package server 提供 REST API 与静态资源服务（net/http 原生路由）。
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Server HTTP 服务。
type Server struct {
	app *App
}

// New 创建服务。
func New(app *App) *Server { return &Server{app: app} }

// writeJSON 统一 JSON 响应（legado ReturnData 风格 + HTTP 语义）。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	writeJSONBody(w, v)
}

// writeJSONBody 仅写 JSON body（headers/status 由调用方控制）。
func writeJSONBody(w http.ResponseWriter, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeErr 统一错误响应。
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"isSuccess": false, "errorMsg": msg})
}

// writeOK 统一成功响应。
func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, map[string]any{"isSuccess": true, "data": v})
}

// readJSON 解析请求体。
func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

// queryInt 查询参数转整数。
func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// queryInt64 查询参数转 int64。
func queryInt64(r *http.Request, key string, def int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

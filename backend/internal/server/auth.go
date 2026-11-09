// auth.go 简单密钥鉴权：公网部署时用 API Key 保护全部 API 与页面。
// 通过环境变量 BOOKSMITH_API_KEY 或 flag --api-key 设置；为空则完全关闭鉴权（本地使用）。
package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Auth 鉴权器。APIKey 为空时所有请求放行。
type Auth struct {
	APIKey string
}

// NewAuth 创建鉴权器。
func NewAuth(apiKey string) *Auth { return &Auth{APIKey: apiKey} }

// Enabled 是否启用鉴权。
func (a *Auth) Enabled() bool { return a != nil && a.APIKey != "" }

// check 校验请求携带的密钥：优先 X-API-Key 头；其次 ?key= 查询参数（SSE/封面等无法设头的场景）；
// 最后 Bearer token。
func (a *Auth) check(r *http.Request) bool {
	if !a.Enabled() {
		return true
	}
	got := r.Header.Get("X-API-Key")
	if got == "" {
		if k := r.URL.Query().Get("key"); k != "" {
			got = k
		}
	}
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.APIKey)) == 1
}

// Middleware 包裹 handler：未通过时 API 返回 401 JSON，页面请求重定向到 /login。
// /api/auth/* 与静态资源（/assets、favicon）豁免——登录页本身需要加载。
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Enabled() || a.isPublic(r) || a.check(r) {
			next.ServeHTTP(w, r)
			return
		}
		// 已提交过 key 的浏览器（localStorage 有 token）→ 前端登录页会带 key 重试
		if wantsHTML(r) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		writeJSONBody(w, map[string]any{"isSuccess": false, "errorMsg": "未授权（密钥缺失或错误）", "needAuth": true})
	})
}

// isPublic 无需鉴权的路径。
func (a *Auth) isPublic(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/api/auth/") ||
		strings.HasPrefix(p, "/assets/") ||
		p == "/login" ||
		p == "/favicon.ico" ||
		p == "/favicon.svg" ||
		!strings.Contains(p, "/") || p == "/" ||
		!strings.Contains(r.URL.Path[1:], "/") // SPA 路由（/books、/search 等单段路径）→ 交给前端
}

// wantsHTML 判断是否浏览器页面请求（Accept 含 text/html 且非静态资源路径）。
func wantsHTML(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/assets/") || strings.Contains(p, ".") {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// LoginHandler 提供登录校验端点：POST {key}，正确返回 success。
func (a *Auth) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if !a.Enabled() {
		writeOK(w, map[string]any{"required": false})
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil || req.Key == "" {
		writeErr(w, 400, "需要 key")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Key), []byte(a.APIKey)) != 1 {
		writeErr(w, 401, "密钥错误")
		return
	}
	writeOK(w, map[string]any{"required": true, "ok": true})
}

// StatusHandler 查询是否需要鉴权：前端首屏据此决定是否跳登录页。
func (a *Auth) StatusHandler(w http.ResponseWriter, r *http.Request) {
	writeOK(w, map[string]any{"required": a.Enabled()})
}

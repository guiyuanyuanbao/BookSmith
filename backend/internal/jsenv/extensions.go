package jsenv

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/longbridgeapp/opencc"

	"booksmith/internal/httpx"
	"booksmith/internal/varstore"
)

// Extensions 是 JS 里 java 对象的实现（legado JsExtensions 子集）。
// goja 将导出方法名首字母小写暴露：Ajax → java.ajax。
type Extensions struct {
	env    *Env
	source SourceRef
	vars   varstore.VariableProvider

	// GetStringHook 供规则上下文注入 java.getString（AnalyzeRule 提供）
	GetStringHook func(rule string) string
	// logFn 调试日志输出
	logFn func(string)
	// baseURL 默认基准（cookie/cache 键）
	baseURL string
}

// SourceRef JS 环境所需书源只读视图（*model.BookSource 满足）。
type SourceRef interface {
	GetKey() string
	GetName() string
	GetHeader() string
	GetConcurrentRate() string
	GetJSLib() string
}

// NewExtensions 创建 java 对象。
func (e *Env) NewExtensions(source SourceRef, vars varstore.VariableProvider, baseURL string) *Extensions {
	return &Extensions{env: e, source: source, vars: vars, baseURL: baseURL, logFn: func(string) {}}
}

// SetLogFn 设置日志函数。
func (x *Extensions) SetLogFn(fn func(string)) { x.logFn = fn }

// SetBaseURL 设置基准 URL。
func (x *Extensions) SetBaseURL(u string) { x.baseURL = u }

// SetSource 设置书源。
func (x *Extensions) SetSource(s SourceRef) { x.source = s }

// GetStringHookCall 规则上下文的 java.getString（由 AnalyzeRule 注入）。
func (x *Extensions) GetStringHookCall(rule string) string {
	if x.GetStringHook != nil {
		return x.GetStringHook(rule)
	}
	return ""
}

// Log 调试日志（java.log）。
func (x *Extensions) Log(msg string) string {
	if x.logFn != nil {
		x.logFn(msg)
	}
	return msg
}

// ---------------- 变量 ----------------

// Put 保存变量。
func (x *Extensions) Put(key, value string) string {
	if x.vars != nil {
		x.vars.Put(key, value)
	}
	return value
}

// Get 读取变量或发起 HTTP GET（多态：1 参变量读取，2+ 参 HTTP GET）。
func (x *Extensions) Get(args ...any) any {
	if len(args) == 1 {
		if x.vars != nil {
			return x.vars.Get(toStringArg(args[0]))
		}
		return ""
	}
	// HTTP GET
	var headers string
	timeout := 0
	if len(args) > 1 && args[1] != nil {
		headers = headersArg(args[1])
	}
	if len(args) > 2 {
		timeout = intArg(args[2])
	}
	resp := x.httpGetString(toStringArg(args[0]), headers, timeout)
	return resp
}

// PutConcurrent 动态更新源并发率。
func (x *Extensions) PutConcurrent(rate string) {
	if x.source != nil {
		x.env.ApplySourceRateLimit(x.source.GetKey(), rate)
	}
}

// ---------------- 网络 ----------------

// StrResponse 文本响应（JS 侧 res.body / res.url / res.headers().get(...)）。
type StrResponse struct {
	URL     string
	Body    string
	Headers *ResponseHeaders
}

// ResponseHeaders 响应头（JS 侧 headers().get("Content-Type")）。
type ResponseHeaders struct {
	m map[string]string
}

// Get 取头值。
func (h *ResponseHeaders) Get(name string) string {
	if h == nil || h.m == nil {
		return ""
	}
	if v, ok := h.m[name]; ok {
		return v
	}
	// 大小写不敏感兜底
	for k, v := range h.m {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// Ajax 抓取 URL 文本（失败返回错误串，与 legado 行为一致）。
func (x *Extensions) Ajax(args ...any) string {
	urlStr := toStringArg(args[0])
	// legado: url 为数组取第一个
	if s := strings.TrimSpace(urlStr); strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
		urlStr = strings.Trim(urlStr, "[]")
	}
	timeout := 0
	if len(args) > 1 {
		timeout = intArg(args[1])
	}
	resp, err := x.env.Client.Do(newCtx(), &httpx.Request{
		URL:     urlStr,
		Headers: httpx.ParseHeaders(x.sourceHeaders()),
		Timeout: seconds(timeout),
	})
	if err != nil {
		return fmt.Sprintf("ajax错误: %v", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("ajax错误: HTTP %d %s", resp.StatusCode, urlStr)
	}
	return resp.BodyText()
}

// AjaxAll 并发抓取多个 URL。
func (x *Extensions) AjaxAll(urls any) []StrResponse {
	var list []string
	switch u := urls.(type) {
	case []any:
		for _, item := range u {
			list = append(list, toStringArg(item))
		}
	case []string:
		list = append(list, u...)
	case string:
		list = append(list, strings.Split(strings.Trim(u, "[]"), ",")...)
	default:
		list = append(list, toStringArg(urls))
	}
	out := make([]StrResponse, 0, len(list))
	for _, u := range list {
		out = append(out, x.Connect(u))
	}
	return out
}

// Connect 抓取并返回 StrResponse。
func (x *Extensions) Connect(args ...any) StrResponse {
	if len(args) == 0 {
		return StrResponse{}
	}
	urlStr := toStringArg(args[0])
	headers := ""
	if len(args) > 1 && args[1] != nil {
		headers = headersArg(args[1])
	}
	timeout := 0
	if len(args) > 2 {
		timeout = intArg(args[2])
	}
	hdr := httpx.ParseHeaders(x.sourceHeaders())
	for k, v := range httpx.ParseHeaders(headers) {
		hdr[k] = v
	}
	resp, err := x.env.Client.Do(newCtx(), &httpx.Request{
		URL:     urlStr,
		Headers: hdr,
		Timeout: seconds(timeout),
	})
	if err != nil {
		return StrResponse{URL: urlStr, Body: fmt.Sprintf("connect错误: %v", err), Headers: &ResponseHeaders{m: map[string]string{}}}
	}
	return StrResponse{
		URL:     resp.URL,
		Body:    resp.BodyText(),
		Headers: respHeaderMap(resp.Header),
	}
}

func respHeaderMap(h map[string][]string) *ResponseHeaders {
	m := map[string]string{}
	for k, v := range h {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return &ResponseHeaders{m: m}
}

// GetWithHeaders HTTP GET（java.get 双参形式也可走这里）。
func (x *Extensions) GetWithHeaders(urlStr, headers string, timeout int) StrResponse {
	hdr := httpx.ParseHeaders(x.sourceHeaders())
	for k, v := range httpx.ParseHeaders(headers) {
		hdr[k] = v
	}
	resp, err := x.env.Client.Do(newCtx(), &httpx.Request{
		URL:     urlStr,
		Headers: hdr,
		Timeout: seconds(timeout),
	})
	if err != nil {
		return StrResponse{URL: urlStr, Body: fmt.Sprintf("get错误: %v", err), Headers: &ResponseHeaders{m: map[string]string{}}}
	}
	return StrResponse{URL: resp.URL, Body: resp.BodyText(), Headers: respHeaderMap(resp.Header)}
}

// Post HTTP POST。
func (x *Extensions) Post(args ...any) StrResponse {
	if len(args) < 2 {
		return StrResponse{}
	}
	urlStr := toStringArg(args[0])
	body := toStringArg(args[1])
	headers := ""
	if len(args) > 2 && args[2] != nil {
		headers = headersArg(args[2])
	}
	timeout := 0
	if len(args) > 3 {
		timeout = intArg(args[3])
	}
	hdr := httpx.ParseHeaders(x.sourceHeaders())
	for k, v := range httpx.ParseHeaders(headers) {
		hdr[k] = v
	}
	if hdr["Content-Type"] == "" && strings.HasPrefix(strings.TrimSpace(body), "{") {
		hdr["Content-Type"] = "application/json"
	}
	resp, err := x.env.Client.Do(newCtx(), &httpx.Request{
		Method:  "POST",
		URL:     urlStr,
		Body:    body,
		Headers: hdr,
		Timeout: seconds(timeout),
	})
	if err != nil {
		return StrResponse{URL: urlStr, Body: fmt.Sprintf("post错误: %v", err), Headers: &ResponseHeaders{m: map[string]string{}}}
	}
	return StrResponse{URL: resp.URL, Body: resp.BodyText(), Headers: respHeaderMap(resp.Header)}
}

// Head HTTP HEAD。
func (x *Extensions) Head(args ...any) StrResponse {
	if len(args) == 0 {
		return StrResponse{}
	}
	urlStr := toStringArg(args[0])
	headers := ""
	if len(args) > 1 && args[1] != nil {
		headers = headersArg(args[1])
	}
	timeout := 0
	if len(args) > 2 {
		timeout = intArg(args[2])
	}
	hdr := httpx.ParseHeaders(x.sourceHeaders())
	for k, v := range httpx.ParseHeaders(headers) {
		hdr[k] = v
	}
	resp, err := x.env.Client.Do(newCtx(), &httpx.Request{
		Method:  "HEAD",
		URL:     urlStr,
		Headers: hdr,
		Timeout: seconds(timeout),
	})
	if err != nil {
		return StrResponse{URL: urlStr, Body: "", Headers: &ResponseHeaders{m: map[string]string{}}}
	}
	return StrResponse{URL: resp.URL, Body: "", Headers: respHeaderMap(resp.Header)}
}

// sourceHeaders 书源默认请求头。
func (x *Extensions) sourceHeaders() string {
	if x.source != nil {
		return x.source.GetHeader()
	}
	return ""
}

// httpGetString 简单 GET 文本。
func (x *Extensions) httpGetString(urlStr, headers string, timeout int) string {
	r := x.GetWithHeaders(urlStr, headers, timeout)
	return r.Body
}

// ---------------- Cookie ----------------

// GetCookie 读 Cookie（java.getCookie(url[, key])）。
func (x *Extensions) GetCookie(args ...any) string {
	if len(args) == 0 {
		return ""
	}
	u := toStringArg(args[0])
	if len(args) > 1 {
		key := toStringArg(args[1])
		if key != "" {
			return x.env.Cookies().GetKey(u, key)
		}
	}
	return x.env.Cookies().GetCookie(u)
}

// SetCookie 写 Cookie。
func (x *Extensions) SetCookie(urlStr, cookie string) string {
	x.env.Cookies().SetCookie(urlStr, cookie)
	return cookie
}

// RemoveCookie 删 Cookie。
func (x *Extensions) RemoveCookie(urlStr, key string) string {
	x.env.Cookies().RemoveCookie(urlStr, key)
	return key
}

// ReplaceCookie 替换 Cookie。
func (x *Extensions) ReplaceCookie(urlStr, key, value string) string {
	x.env.Cookies().ReplaceCookie(urlStr, key, value)
	return value
}

// GetCookieKey 读单键（cookie.getKey 别名场景）。
func (x *Extensions) GetCookieKey(urlStr, key string) string {
	return x.env.Cookies().GetKey(urlStr, key)
}

// ---------------- Cache ----------------

// CachePut 写缓存（java.cache.put / cachePut 兼容）。
func (x *Extensions) CachePut(key, value string, saveTime int64) string {
	x.env.Cache().Put(key, value, saveTime)
	return value
}

// CacheGet 读缓存。
func (x *Extensions) CacheGet(key string) string {
	return x.env.Cache().Get(key)
}

// CacheDelete 删缓存。
func (x *Extensions) CacheDelete(key string) string {
	x.env.Cache().Delete(key)
	return key
}

// CachePutMemory 写内存缓存。
func (x *Extensions) CachePutMemory(key, value string) string {
	x.env.Cache().PutMemory(key, value)
	return value
}

// CacheGetFromMemory 读内存缓存。
func (x *Extensions) CacheGetFromMemory(key string) string {
	return x.env.Cache().GetFromMemory(key)
}

// CachePutFile 写文件缓存。
func (x *Extensions) CachePutFile(key, value string) string {
	x.env.Cache().PutFile(key, value)
	return value
}

// CacheGetFile 读文件缓存。
func (x *Extensions) CacheGetFile(key string) string {
	return x.env.Cache().GetFile(key)
}

// CacheFile 下载文本文件并缓存，返回内容。
func (x *Extensions) CacheFile(urlStr string, saveTime int64) string {
	key := "file_" + x.Md5Encode16(urlStr)
	if v := x.env.Cache().Get(key); v != "" {
		return v
	}
	body := x.Ajax(urlStr)
	if !strings.HasPrefix(body, "ajax错误") {
		x.env.Cache().Put(key, body, saveTime)
	}
	return body
}

// ---------------- 编解码与摘要 ----------------

// Base64Encode Base64 编码（默认去换行）。
func (x *Extensions) Base64Encode(args ...any) string {
	s := toStringArg(args[0])
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// Base64Decode Base64 解码（容忍 URL safe 与缺省 padding）。
func (x *Extensions) Base64Decode(args ...any) string {
	s := strings.TrimSpace(toStringArg(args[0]))
	// 清洗 URL-safe 与空白
	clean := strings.NewReplacer("-", "+", "_", "/", "\n", "", "\r", "").Replace(s)
	// 补齐 padding
	if m := len(clean) % 4; m != 0 {
		clean += strings.Repeat("=", 4-m)
	}
	b, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return ""
	}
	return string(b)
}

// Base64DecodeToByteArray Base64 解码为字节数组（JS 数组）。
func (x *Extensions) Base64DecodeToByteArray(args ...any) []any {
	b := []byte(x.Base64Decode(args...))
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// Md5Encode MD5（32 位小写）。
func (x *Extensions) Md5Encode(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Md5Encode16 MD5（16 位中段）。
func (x *Extensions) Md5Encode16(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[8:24]
}

// DigestHex 任意摘要（"SHA-256" 等算法名）。
func (x *Extensions) DigestHex(data, algorithm string) string {
	var h hash.Hash
	switch strings.ToUpper(strings.ReplaceAll(algorithm, "-", "")) {
	case "MD5":
		h = md5.New()
	case "SHA1", "SHA":
		h = sha1.New()
	case "SHA256":
		h = sha256.New()
	case "SHA512":
		h = sha512.New()
	default:
		h = md5.New()
	}
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// HMacHex HMAC 十六进制。
func (x *Extensions) HMacHex(data, algorithm, key string) string {
	var h func() hash.Hash
	switch strings.ToUpper(strings.ReplaceAll(algorithm, "-", "")) {
	case "SHA1", "SHA":
		h = sha1.New
	case "SHA256":
		h = sha256.New
	case "SHA512":
		h = sha512.New
	default:
		h = md5.New
	}
	mac := hmac.New(h, []byte(key))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}

// HexEncodeToString 十六进制编码。
func (x *Extensions) HexEncodeToString(args ...any) string {
	s := toStringArg(args[0])
	if len(args) > 0 {
		if b, ok := args[0].([]any); ok {
			raw := make([]byte, len(b))
			for i, v := range b {
				raw[i] = byte(intArg(v))
			}
			return hex.EncodeToString(raw)
		}
	}
	return hex.EncodeToString([]byte(s))
}

// HexDecodeToString 十六进制解码。
func (x *Extensions) HexDecodeToString(s string) string {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return string(b)
}

// SymmetricCrypto 对称加密对象（AES 等）。
type SymmetricCrypto struct {
	algo string
	key  []byte
	iv   []byte
	ctr  bool
	gcm  bool
}

// CreateSymmetricCrypto 创建对称加密（java.createSymmetricCrypto("AES/CBC/PKCS5Padding", key[, iv])）。
func (x *Extensions) CreateSymmetricCrypto(args ...any) *SymmetricCrypto {
	transformation := toStringArg(args[0])
	key := []byte(toStringArg(args[1]))
	var iv []byte
	if len(args) > 2 && args[2] != nil {
		iv = []byte(toStringArg(args[2]))
	}
	up := strings.ToUpper(transformation)
	sc := &SymmetricCrypto{algo: up, key: key, iv: iv}
	switch {
	case strings.Contains(up, "GCM"):
		sc.gcm = true
	case strings.Contains(up, "CTR"):
		sc.ctr = true
	}
	return sc
}

// Encrypt 加密并返回字节数组。
func (sc *SymmetricCrypto) Encrypt(args ...any) []any {
	data := argBytes(args)
	b, err := sc.encryptBytes(data)
	if err != nil {
		return nil
	}
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// EncryptHex 加密返回十六进制。
func (sc *SymmetricCrypto) EncryptHex(args ...any) string {
	data := argBytes(args)
	b, err := sc.encryptBytes(data)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// EncryptBase64 加密返回 Base64。
func (sc *SymmetricCrypto) EncryptBase64(args ...any) string {
	data := argBytes(args)
	b, err := sc.encryptBytes(data)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

// Decrypt 解密字节数组。
func (sc *SymmetricCrypto) Decrypt(args ...any) []any {
	b, err := sc.decryptBytes(argBytes(args))
	if err != nil {
		return nil
	}
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// DecryptStr 解密为字符串。
func (sc *SymmetricCrypto) DecryptStr(args ...any) string {
	b, err := sc.decryptBytes(argBytes(args))
	if err != nil {
		return ""
	}
	return string(b)
}

func argBytes(args []any) []byte {
	if len(args) == 0 {
		return nil
	}
	switch v := args[0].(type) {
	case string:
		return []byte(v)
	case []any:
		b := make([]byte, len(v))
		for i, item := range v {
			b[i] = byte(intArg(item))
		}
		return b
	default:
		return []byte(fmt.Sprintf("%v", v))
	}
}

func (sc *SymmetricCrypto) newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(sc.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (sc *SymmetricCrypto) encryptBytes(data []byte) ([]byte, error) {
	if sc.gcm {
		aead, err := sc.newGCM()
		if err != nil {
			return nil, err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		return aead.Seal(nonce, nonce, data, nil), nil
	}
	block, err := aes.NewCipher(sc.key)
	if err != nil {
		return nil, err
	}
	if sc.ctr {
		iv := sc.ivOrFixed(block.BlockSize())
		stream := cipher.NewCTR(block, iv)
		out := make([]byte, len(data))
		stream.XORKeyStream(out, data)
		return out, nil
	}
	// CBC + PKCS5/7 填充
	bs := block.BlockSize()
	data = pkcs5Pad(data, bs)
	iv := sc.ivOrFixed(bs)
	out := make([]byte, len(data))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

func (sc *SymmetricCrypto) decryptBytes(data []byte) ([]byte, error) {
	if sc.gcm {
		aead, err := sc.newGCM()
		if err != nil {
			return nil, err
		}
		if len(data) < aead.NonceSize() {
			return nil, fmt.Errorf("gcm 数据过短")
		}
		return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], nil)
	}
	block, err := aes.NewCipher(sc.key)
	if err != nil {
		return nil, err
	}
	if sc.ctr {
		iv := sc.ivOrFixed(block.BlockSize())
		stream := cipher.NewCTR(block, iv)
		out := make([]byte, len(data))
		stream.XORKeyStream(out, data)
		return out, nil
	}
	bs := block.BlockSize()
	if len(data) == 0 || len(data)%bs != 0 {
		return nil, fmt.Errorf("CBC 数据长度非法")
	}
	iv := sc.ivOrFixed(bs)
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return pkcs5Unpad(out), nil
}

func (sc *SymmetricCrypto) ivOrFixed(size int) []byte {
	if len(sc.iv) >= size {
		return sc.iv[:size]
	}
	iv := make([]byte, size)
	copy(iv, sc.iv)
	return iv
}

func pkcs5Pad(data []byte, bs int) []byte {
	n := bs - len(data)%bs
	pad := make([]byte, n)
	for i := range pad {
		pad[i] = byte(n)
	}
	return append(data, pad...)
}

func pkcs5Unpad(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	n := int(data[len(data)-1])
	if n <= 0 || n > len(data) {
		return data
	}
	return data[:len(data)-n]
}

// EncodeURI URI 编码。
func (x *Extensions) EncodeURI(args ...any) string {
	s := toStringArg(args[0])
	return url.QueryEscape(s)
}

// StrToBytes 字符串转字节数组。
func (x *Extensions) StrToBytes(s string) []any {
	b := []byte(s)
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// BytesToStr 字节数组转字符串。
func (x *Extensions) BytesToStr(args ...any) string {
	if len(args) == 0 {
		return ""
	}
	if b, ok := args[0].([]any); ok {
		raw := make([]byte, len(b))
		for i, v := range b {
			raw[i] = byte(intArg(v))
		}
		return string(raw)
	}
	return toStringArg(args[0])
}

// Utf8ToGbk UTF-8 → GBK（兼容垫片）。
func (x *Extensions) Utf8ToGbk(s string) string {
	enc := httpx.EncodingByName("gbk")
	if enc == nil {
		return s
	}
	b, err := enc.NewEncoder().String(s)
	if err != nil {
		return s
	}
	// 保留为 latin1 可视字节（JS 侧通常当字节串用）
	return string([]byte(b))
}

// GbkToUtf8 GBK → UTF-8（兼容垫片）。
func (x *Extensions) GbkToUtf8(s string) string {
	enc := httpx.EncodingByName("gbk")
	if enc == nil {
		return s
	}
	b, err := enc.NewDecoder().String(s)
	if err != nil {
		return s
	}
	return b
}

// TimeFormat 时间格式化（毫秒时间戳 → 字符串）。
func (x *Extensions) TimeFormat(args ...any) string {
	var t int64
	if len(args) > 0 {
		t = int64(floatArg(args[0]))
	}
	format := "yyyy-MM-dd HH:mm:ss"
	if len(args) > 1 && toStringArg(args[1]) != "" {
		format = toStringArg(args[1])
	}
	return formatTime(t, format)
}

// TimeFormatUTC UTC 时间格式化（时区偏移 sh 小时）。
func (x *Extensions) TimeFormatUTC(args ...any) string {
	var t int64
	if len(args) > 0 {
		t = int64(floatArg(args[0]))
	}
	format := "yyyy-MM-dd HH:mm:ss"
	if len(args) > 1 && toStringArg(args[1]) != "" {
		format = toStringArg(args[1])
	}
	sh := 0
	if len(args) > 2 {
		sh = int(floatArg(args[2]))
	}
	return formatTime(t+int64(sh)*3600000, format)
}

// TimeFormatAPI 暴露给 JS 的时间戳（java.timeFormatAPI）。
func (x *Extensions) TimeFormatAPI() int64 { return time.Now().UnixMilli() }

// ---------------- 简繁转换 ----------------

var (
	convT2S   *opencc.OpenCC
	convS2T   *opencc.OpenCC
	convOnce  sync.Once
	convError error
)

func initConverters() {
	convOnce.Do(func() {
		convT2S, convError = opencc.New("t2s")
		if convError != nil {
			return
		}
		convS2T, convError = opencc.New("s2t")
	})
}

// T2S 繁体 → 简体。
func (x *Extensions) T2S(s string) string {
	initConverters()
	if convError != nil || convT2S == nil {
		return s
	}
	out, err := convT2S.Convert(s)
	if err != nil {
		return s
	}
	return out
}

// S2T 简体 → 繁体。
func (x *Extensions) S2T(s string) string {
	initConverters()
	if convError != nil || convS2T == nil {
		return s
	}
	out, err := convS2T.Convert(s)
	if err != nil {
		return s
	}
	return out
}

// ---------------- 其他 ----------------

// HtmlFormat 正文 HTML 格式化（由 content 包注入实现，避免循环依赖）。
var HtmlFormatImpl func(s, redirectURL string) string

// HtmlFormat 调用正文格式化。
func (x *Extensions) HtmlFormat(args ...any) string {
	s := toStringArg(args[0])
	redirect := ""
	if len(args) > 1 {
		redirect = toStringArg(args[1])
	}
	if HtmlFormatImpl != nil {
		return HtmlFormatImpl(s, redirect)
	}
	return s
}

var chNumRe = regexp.MustCompile(`(第)\s*([零一二三四五六七八九十百千万亿两]+|\d+)\s*([章节回卷集篇部])`)

// ToNumChapter 中文数字章节转阿拉伯数字（保留章节单位与后缀）。
func (x *Extensions) ToNumChapter(s string) string {
	return chNumRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := chNumRe.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + ChineseNumToArabic(sub[2]) + sub[3]
	})
}

// ChineseNumToArabic 中文数字转阿拉伯数字。
func ChineseNumToArabic(s string) string {
	digits := map[rune]int64{'零': 0, '一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '两': 2}
	units := map[rune]int64{'十': 10, '百': 100, '千': 1000, '万': 10000, '亿': 100000000}
	// 纯数字直接返回
	if allDigits(s) {
		return strings.TrimLeft(s, "0")
	}
	var total, section int64
	var current int64
	hasDigit := false
	for _, r := range s {
		if d, ok := digits[r]; ok {
			current = current*10 + d
			hasDigit = true
		} else if u, ok := units[r]; ok {
			hasDigit = true
			switch {
			case current == 0:
				current = 1
			}
			if u == 10000 || u == 100000000 {
				section = (section + current) * u
				total += section
				section = 0
			} else {
				section += current * u
			}
			current = 0
		}
	}
	if !hasDigit {
		return s
	}
	total += section + current
	return fmt.Sprintf("%d", total)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RandomUUID 生成随机 UUID v4。
func (x *Extensions) RandomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Toast 提示（服务端忽略）。
func (x *Extensions) Toast(msg string) string { return msg }

// ---------------- 参数辅助 ----------------

func toStringArg(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []any:
		if len(x) > 0 {
			return toStringArg(x[0])
		}
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

func intArg(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		var n int
		fmt.Sscanf(x, "%d", &n)
		return n
	default:
		return 0
	}
}

func floatArg(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		var f float64
		fmt.Sscanf(x, "%g", &f)
		return f
	default:
		return 0
	}
}

// headersArg 头参数（JSON 串或对象）。
func headersArg(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case map[string]any:
		b, err := jsonMarshalOrdered(x)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func seconds(sec int) time.Duration {
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func newCtx() context.Context { return context.Background() }

// formatTime 毫秒时间戳按 java 风格格式输出。
func formatTime(ms int64, format string) string {
	if ms <= 0 {
		return ""
	}
	t := time.UnixMilli(ms)
	r := strings.NewReplacer(
		"yyyy", pad(t.Year(), 4),
		"MM", pad(int(t.Month()), 2),
		"dd", pad(t.Day(), 2),
		"HH", pad(t.Hour(), 2),
		"mm", pad(t.Minute(), 2),
		"ss", pad(t.Second(), 2),
		"SSS", pad(t.Nanosecond()/1e6, 3),
	)
	return r.Replace(format)
}

func pad(n int, width int) string {
	s := fmt.Sprintf("%d", n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func jsonMarshalOrdered(v any) ([]byte, error) {
	return json.Marshal(v)
}

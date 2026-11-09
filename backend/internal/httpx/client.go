// Package httpx 提供面向书源抓取场景的 HTTP 客户端：
// 自动编码检测、UA 池、Cookie 管理、每站点并发/频率限流、超时重试。
package httpx

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// Request 描述一次抓取请求。
type Request struct {
	Method  string            // GET / POST …
	URL     string            // 目标 URL
	Headers map[string]string // 请求头
	Body    string            // 请求体（POST 等）
	Charset string            // 指定响应编码（如 gbk），留空自动检测
	Timeout time.Duration     // 单次请求超时，0 用客户端默认
}

// Response 是解码后的响应。
type Response struct {
	StatusCode int
	Header     http.Header
	URL        string // 最终 URL（跟随重定向后）
	Body       []byte // 原始字节
	RawBody    io.ReadCloser
	Cost       time.Duration
	req        *Request
}

// BodyText 将响应体按编码解码为字符串。
func (r *Response) BodyText() string {
	if r.req != nil && r.req.Charset != "" {
		if enc := EncodingByName(r.req.Charset); enc != nil {
			if s, err := enc.NewDecoder().Bytes(r.Body); err == nil {
				return string(s)
			}
		}
	}
	s, _ := DecodeBody(r.Body, r.Header.Get("Content-Type"))
	return s
}

// EncodingByName 通过别名查找编码器（gbk/gb2312/gb18030/big5/utf-8/utf-16/shift-jis 等）。
func EncodingByName(name string) encoding.Encoding {
	if name == "" {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "charset=")
	name = strings.Trim(name, `"'`)
	switch name {
	case "", "utf8", "utf-8", "utf_8":
		return unicode.UTF8
	case "gbk", "gb2312", "gb_2312", "cp936", "gb18030", "gb-18030":
		return simplifiedchinese.GBK
	case "big5", "big-5", "big5hkscs", "big5-hkscs", "cp950":
		return traditionalchinese.Big5
	case "utf-16", "utf16", "utf_16":
		return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM)
	case "utf-16le":
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	case "utf-16be":
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
	}
	if enc, _ := charset.Lookup(name); enc != nil {
		return enc
	}
	return nil
}

// DecodeBody 依据 Content-Type、BOM 与 HTML meta 标签解码响应体。
// 返回解码后的文本；无法判定时按 UTF-8 处理（无效字节以替换符呈现）。
func DecodeBody(data []byte, contentType string) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	enc, _, _ := DetermineEncoding(data, contentType)
	if enc == nil {
		return string(data), nil
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return string(data), err
	}
	return string(decoded), nil
}

// DetermineEncoding 判定响应体编码：BOM > Content-Type > HTML meta > UTF-8。
// 返回编码器与编码名（可能为空）。
func DetermineEncoding(data []byte, contentType string) (encoding.Encoding, string, bool) {
	if len(data) >= 2 {
		switch {
		case data[0] == 0xFE && data[1] == 0xFF:
			return unicode.UTF16(unicode.BigEndian, unicode.UseBOM), "utf-16be", true
		case data[0] == 0xFF && data[1] == 0xFE:
			return unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), "utf-16le", true
		case data[0] == 0xEF && data[1] == 0xBB:
			if len(data) >= 3 && data[2] == 0xBF {
				return unicode.UTF8, "utf-8", true
			}
		}
	}
	if ct := strings.ToLower(contentType); ct != "" {
		if _, params, err := mime.ParseMediaType(ct); err == nil {
			if cs := strings.TrimSpace(params["charset"]); cs != "" {
				if enc := EncodingByName(cs); enc != nil {
					return enc, cs, false
				}
			}
		}
	}
	// 嗅探 HTML meta（仅检查前 4KB，够覆盖 head 区域）
	peek := data
	if len(peek) > 4096 {
		peek = peek[:4096]
	}
	if enc, name, ok := sniffMetaCharset(peek); ok {
		return enc, name, false
	}
	// x/net/html/charset 的最终兜底（基于字节统计）
	if enc, name, _ := charset.DetermineEncoding(peek, contentType); enc != nil {
		return enc, name, false
	}
	return unicode.UTF8, "utf-8", false
}

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([\w-]+)`)

// sniffMetaCharset 从 HTML 片段中提取 <meta charset> 声明。
func sniffMetaCharset(peek []byte) (encoding.Encoding, string, bool) {
	m := metaCharsetRe.FindSubmatch(peek)
	if m == nil {
		return nil, "", false
	}
	name := strings.TrimSpace(string(m[1]))
	enc := EncodingByName(name)
	if enc == nil {
		return nil, "", false
	}
	return enc, name, true
}

// UserAgent 池：覆盖桌面与移动端，降低反爬概率。
var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36 Edg/125.0.0.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 13; M2102J20SG) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
}

// RandomUA 返回随机 User-Agent。
func RandomUA() string {
	return userAgents[rand.Intn(len(userAgents))]
}

// ClientOption 客户端选项。
type ClientOption func(*Client)

// WithTimeout 设置整体默认超时。
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.defaultTimeout = d }
}

// WithCookieJar 启用共享 Cookie（书源需要登录态时使用）。
func WithCookieJar(jar http.CookieJar) ClientOption {
	return func(c *Client) { c.hc.Jar = jar }
}

// WithInsecureTLS 跳过 TLS 校验（部分老站点证书有问题）。
func WithInsecureTLS() ClientOption {
	return func(c *Client) {
		if tr, ok := c.hc.Transport.(*http.Transport); ok {
			tr.TLSClientConfig.InsecureSkipVerify = true
		}
	}
}

// WithProxy 设置 HTTP/SOCKS 代理。
func WithProxy(addr string) ClientOption {
	return func(c *Client) {
		if tr, ok := c.hc.Transport.(*http.Transport); ok {
			if addr == "" {
				tr.Proxy = nil
			} else if pu, err := url.Parse(addr); err == nil {
				tr.Proxy = http.ProxyURL(pu)
			}
		}
	}
}

// Client 是具备限流能力的 HTTP 客户端。
type Client struct {
	hc             *http.Client
	defaultTimeout time.Duration
	limiter        *hostLimiter
	maxRetries     int
	retryDelay     time.Duration
}

// NewClient 创建客户端。
func NewClient(opts ...ClientOption) *Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   6 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   6 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{},
		DisableCompression:    false,
	}
	c := &Client{
		hc: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return errors.New("stopped after 8 redirects")
				}
				return nil
			},
		},
		defaultTimeout: 30 * time.Second,
		limiter:        newHostLimiter(),
		maxRetries:     2,
		retryDelay:     500 * time.Millisecond,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SetRateLimit 设置某主机的并发与最小间隔限流（legado concurrentRate 语义：
// "次数/毫秒" 表示该毫秒窗口内最多 N 次；"0" 不限）。
func (c *Client) SetRateLimit(host string, count int, window time.Duration) {
	c.limiter.setRule(host, count, window)
}

// SetMaxConcurrent 设置某主机最大并发数（0/负数不限）。
func (c *Client) SetMaxConcurrent(host string, n int) {
	c.limiter.setMaxConcurrent(host, n)
}

// NormalizeURL 规范化书源 JS 拼出的畸形 URL：
// 折叠重复协议头（https:https://x → https://x）。legado 的 OkHttp 对这类
// URL 宽容解析，此处对齐其行为，否则 java.ajax 等场景直接报 no host。
func NormalizeURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return u
	}
	// 剥离重复协议头（"https:https:https://x" → "https://x"，可叠加多层）
	for i := 0; i < 4; i++ {
		idx := strings.Index(u, ":")
		if idx <= 0 {
			break
		}
		head := strings.ToLower(u[:idx])
		rest := u[idx+1:]
		if head != "https" && head != "http" {
			break
		}
		lowRest := strings.ToLower(rest)
		if strings.HasPrefix(lowRest, "http:") || strings.HasPrefix(lowRest, "https:") {
			u = rest // rest 仍是带协议头的完整 URL：继续剥离
			continue
		}
		break
	}
	// 协议名统一小写
	if idx := strings.Index(u, "://"); idx > 0 {
		u = strings.ToLower(u[:idx]) + u[idx:]
	}
	return u
}

// Do 执行请求：限流排队 → 发送 → 重试。ctx 为 nil 时使用 Background。
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req.URL = NormalizeURL(req.URL)
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.retryDelay * time.Duration(attempt)):
			}
		}
		resp, err := c.doOnce(ctx, req)
		if err != nil {
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue
		}
		// 5xx 重试；429 重试并尊重 Retry-After 头（源站限流）
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("http %d: %s", resp.StatusCode, req.URL)
			if resp.StatusCode == http.StatusTooManyRequests {
				// 自动学习限流：429 后对该主机施加递增的最小间隔（1s→2s→4s，上限 8s）
				if host := hostOf(req.URL); host != "" {
					c.limiter.learnBackoff(host)
				}
				// Retry-After 秒数优先；否则指数退避（500ms/1s/2s…上限 10s）
				if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
					lastErr = fmt.Errorf("http 429 (限流，等待 %v 后重试): %s", ra, req.URL)
					drainClose(resp)
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(ra):
					}
					continue
				}
				backoff := c.retryDelay * time.Duration(1<<attempt)
				if backoff > 10*time.Second {
					backoff = 10 * time.Second
				}
				drainClose(resp)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff):
				}
				continue
			}
			drainClose(resp)
			continue
		}
		// 请求成功：清除该主机的 429 冷却
		if resp.StatusCode < 400 {
			c.limiter.clearBlock(hostOf(req.URL))
		}
		return resp, nil
	}
	return nil, lastErr
}

// parseRetryAfter 解析 Retry-After 头（秒数形式），非法/缺失返回 0。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 120 {
		return time.Duration(n) * time.Second
	}
	return 0
}

func drainClose(r *Response) {
	if r.RawBody != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.RawBody, 1<<16))
		_ = r.RawBody.Close()
	}
}

func (c *Client) doOnce(ctx context.Context, req *Request) (*Response, error) {
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if req.Body != "" && method != http.MethodGet && method != http.MethodHead {
		bodyReader = strings.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	applyDefaultHeaders(hr, req)
	for k, v := range req.Headers {
		if strings.EqualFold(k, "cookie") {
			// Cookie 头直接附加
			existing := hr.Header.Get("Cookie")
			if existing == "" {
				hr.Header.Set("Cookie", v)
			} else {
				hr.Header.Set("Cookie", existing+"; "+v)
			}
			continue
		}
		hr.Header.Set(k, v)
	}

	timeout := c.defaultTimeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	release, err := c.limiter.acquire(rctx, hostOf(req.URL))
	if err != nil {
		return nil, err
	}
	defer release()
	hresp, err := c.hc.Do(hr)
	if err != nil {
		return nil, err
	}
	body, err := readAll(hresp)
	if err != nil {
		_ = hresp.Body.Close()
		return nil, fmt.Errorf("read body: %w", err)
	}
	_ = hresp.Body.Close()
	return &Response{
		StatusCode: hresp.StatusCode,
		Header:     hresp.Header,
		URL:        hresp.Request.URL.String(),
		Body:       body,
		Cost:       time.Since(start),
		req:        req,
	}, nil
}

// applyDefaultHeaders 设置默认请求头（书源未指定时）。
func applyDefaultHeaders(hr *http.Request, req *Request) {
	if req.Headers == nil || req.Headers["User-Agent"] == "" {
		hr.Header.Set("User-Agent", RandomUA())
	}
	if req.Headers == nil || req.Headers["Accept"] == "" {
		hr.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	if req.Headers == nil || req.Headers["Accept-Language"] == "" {
		hr.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	}
	if req.Method == http.MethodPost && req.Headers["Content-Type"] == "" {
		hr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
}

// readAll 读取并按需解压响应体。
func readAll(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	switch strings.ToLower(resp.Header.Get("Content-Encoding")) {
	case "gzip":
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return io.ReadAll(reader)
		}
		defer gz.Close()
		reader = gz
	case "deflate":
		// 服务端可能用 zlib 包装，做一层兼容
		br := newPeekReader(reader)
		if br.peek3() == 0x1f && br.byteAt(1) == 0x8b {
			gz, err := gzip.NewReader(br)
			if err != nil {
				return io.ReadAll(br)
			}
			defer gz.Close()
			reader = gz
		} else {
			fr := flate.NewReader(br)
			defer fr.Close()
			reader = fr
		}
	case "br", "zstd":
		// 未启用时不会出现（不主动声明这些 Accept-Encoding）
	}
	return io.ReadAll(io.LimitReader(reader, 64<<20))
}

type peekReader struct {
	r    io.Reader
	buf  [3]byte
	n    int
	read bool
}

func newPeekReader(r io.Reader) *peekReader { return &peekReader{r: r} }

func (p *peekReader) fill() {
	if p.read {
		return
	}
	p.n, _ = io.ReadFull(p.r, p.buf[:])
	p.read = true
}

func (p *peekReader) peek3() byte {
	p.fill()
	if p.n > 0 {
		return p.buf[0]
	}
	return 0
}
func (p *peekReader) byteAt(i int) byte {
	p.fill()
	if p.n > i {
		return p.buf[i]
	}
	return 0
}

func (p *peekReader) Read(b []byte) (int, error) {
	p.fill()
	if p.n > 0 {
		copied := copy(b, p.buf[:p.n])
		p.n = 0
		if copied < len(b) {
			n2, err := p.r.Read(b[copied:])
			return copied + n2, err
		}
		return copied, nil
	}
	return p.r.Read(b)
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

// hostLimiter 按 (主机, 规则) 限流：窗口计数 + 最大并发 + 最小间隔。
type hostLimiter struct {
	mu         sync.Mutex
	windows    map[string]*windowRule
	semaphores map[string]chan struct{}
	minLast    map[string]time.Time
	// learnedInterval 429 自适应学习的最小间隔
	learnedInterval map[string]time.Duration
	// blockedUntil 429 后的冷却截止时间（到点前所有请求排队）
	blockedUntil map[string]time.Time
}

type windowRule struct {
	count  int
	window time.Duration
}

func newHostLimiter() *hostLimiter {
	return &hostLimiter{
		windows:         map[string]*windowRule{},
		semaphores:      map[string]chan struct{}{},
		minLast:         map[string]time.Time{},
		learnedInterval: map[string]time.Duration{},
		blockedUntil:    map[string]time.Time{},
	}
}

func (h *hostLimiter) setRule(host string, count int, window time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.windows[host] = &windowRule{count: count, window: window}
}

func (h *hostLimiter) setMaxConcurrent(host string, n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n <= 0 {
		delete(h.semaphores, host)
	} else {
		h.semaphores[host] = make(chan struct{}, n)
	}
}

// clearBlock 清除主机 429 冷却。
func (h *hostLimiter) clearBlock(host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.blockedUntil, host)
	delete(h.learnedInterval, host)
}

// learnBackoff 收到 429 后自动对该主机施加最小间隔（1s→2s→4s→8s 封顶）。
// 源未配置 concurrentRate 时的自适应限流。
func (h *hostLimiter) learnBackoff(host string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cur := h.windows[host]
	if cur != nil && cur.count > 0 {
		return // 已有显式限流规则，不覆盖
	}
	if cur2 := h.learnedInterval[host]; cur2 > 0 {
		// 已在学习中：递增（倍增至上限 8s）并延长冷却
		next := cur2 * 2
		if next > 8*time.Second {
			next = 8 * time.Second
		}
		h.learnedInterval[host] = next
	} else {
		h.learnedInterval[host] = 1 * time.Second
	}
	// 冷却期：所有请求等到冷却结束（连续 429 时指数延长）
	h.blockedUntil[host] = time.Now().Add(h.learnedInterval[host])
}

// learnedWait 返回自适应学习到的最小间隔（无则 0）。
func (h *hostLimiter) learnedWait(host string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.learnedInterval[host]
}

// acquire 等待限流放行，返回释放函数（无并发限制时返回 no-op）。
func (h *hostLimiter) acquire(ctx context.Context, host string) (func(), error) {
	h.mu.Lock()
	rule := h.windows[host]
	sem := h.semaphores[host]
	var wait time.Duration
	if rule != nil && rule.count > 0 && rule.window > 0 {
		now := time.Now()
		if last, ok := h.minLast[host]; ok {
			elapsed := now.Sub(last)
			if elapsed < rule.window {
				wait = rule.window - elapsed
			}
		}
		h.minLast[host] = now.Add(wait)
	}
	// 429 冷却期：所有该主机请求等到冷却截止（而非逐请求间隔，避免超时叠加）
	if bu, ok := h.blockedUntil[host]; ok && bu.After(time.Now()) {
		if d := bu.Sub(time.Now()); d > wait {
			wait = d
		}
	}
	h.mu.Unlock()

	if wait > 0 {
		select {
		case <-ctx.Done():
			return func() {}, ctx.Err()
		case <-time.After(wait):
		}
	}
	if sem != nil {
		select {
		case sem <- struct{}{}:
			return func() { <-sem }, nil
		default:
			// 并发槽满则排队等待
			select {
			case sem <- struct{}{}:
				return func() { <-sem }, nil
			case <-ctx.Done():
				return func() {}, ctx.Err()
			}
		}
	}
	return func() {}, nil
}

// ParseConcurrentRate 解析 legado concurrentRate 字符串。
// "10/60000" 表示 60 秒内最多 10 次；"0" 或空表示不限。
func ParseConcurrentRate(s string) (count int, window time.Duration) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, 0
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, 0
	}
	ms, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil || ms <= 0 {
		return 0, 0
	}
	return n, time.Duration(ms) * time.Millisecond
}

// ParseHeaders 解析 legado 风格的 header 字段：JSON 对象字符串或 key:value 换行分隔。
func ParseHeaders(s string) map[string]string {
	out := map[string]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out
	}
	if strings.HasPrefix(s, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(s), &m); err == nil {
			for k, v := range m {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			return out
		}
		// 值可能不是字符串，尝试通用 map
		var raw map[string]any
		if err := json.Unmarshal([]byte(s), &raw); err == nil {
			for k, v := range raw {
				out[strings.TrimSpace(k)] = fmt.Sprintf("%v", v)
			}
			return out
		}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// AbsoluteURL 将相对链接解析为绝对链接。
func AbsoluteURL(base, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	bu, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ref
	}
	ru, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	// 处理协议相对 //host/path
	if ru.Host != "" && ru.Scheme == "" {
		if bu.Scheme != "" {
			return bu.Scheme + ":" + ref
		}
	}
	// 处理协议相对（双斜杠开头已被 Parse 识别）
	abs := bu.ResolveReference(ru)
	return abs.String()
}

// Package jsenv 提供基于 goja（纯 Go JS 引擎，零桥接）的书源脚本运行时，
// 实现 legado JsExtensions 的常用子集，供规则引擎执行 <js>/<js>: / {{}} 脚本。
package jsenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"booksmith/internal/httpx"
)

// Env JS 运行环境：HTTP 客户端、Cookie、缓存与脚本编译缓存。
type Env struct {
	Client  *httpx.Client
	DataDir string

	cookies *CookieStore
	cache   *CacheManager

	progMu   sync.Mutex
	programs map[string]*goja.Program
	progLRU  []string // 简单 LRU 键序

	// 全局 JS 库（书源 jsLib）预编译缓存
	jsLibMu    sync.Mutex
	jsLibProgs map[string]*goja.Program
}

// New 创建环境。
func New(client *httpx.Client, dataDir string) *Env {
	if dataDir != "" {
		_ = os.MkdirAll(filepath.Join(dataDir, "cache"), 0o755)
	}
	return &Env{
		Client:     client,
		DataDir:    dataDir,
		cookies:    NewCookieStore(dataDir),
		cache:      NewCacheManager(dataDir),
		programs:   map[string]*goja.Program{},
		jsLibProgs: map[string]*goja.Program{},
	}
}

// Cookies Cookie 存储。
func (e *Env) Cookies() *CookieStore { return e.cookies }

// Cache 缓存管理器。
func (e *Env) Cache() *CacheManager { return e.cache }

const maxProgramCache = 64

// compileProgram 编译脚本（带 LRU 缓存）。
func (e *Env) compileProgram(key, script string) (*goja.Program, error) {
	e.progMu.Lock()
	if p, ok := e.programs[key]; ok {
		e.progMu.Unlock()
		return p, nil
	}
	e.progMu.Unlock()
	p, err := goja.Compile(key, script, false)
	if err != nil {
		return nil, err
	}
	e.progMu.Lock()
	e.programs[key] = p
	e.progLRU = append(e.progLRU, key)
	if len(e.progLRU) > maxProgramCache {
		old := e.progLRU[0]
		e.progLRU = e.progLRU[1:]
		delete(e.programs, old)
	}
	e.progMu.Unlock()
	return p, nil
}

// Engine 实现 analyzer.JSEngine 接口（结构性匹配，无导入依赖）。
type Engine struct {
	env *Env
}

// Engine 获取引擎（供 analyzer.JSEngine 使用）。
func (e *Env) Engine() *Engine { return &Engine{env: e} }

// Eval 执行脚本：先预载源 JS 库，再注入绑定运行。
func (en *Engine) Eval(script string, bindings map[string]any) (any, error) {
	return en.EvalWithTimeout(script, bindings, 0)
}

// EvalWithTimeout 带看门狗超时执行（timeout<=0 不限）。
func (en *Engine) EvalWithTimeout(script string, bindings map[string]any, timeout time.Duration) (any, error) {
	vm := goja.New()
	e := en.env

	if timeout > 0 {
		stop := make(chan struct{})
		go func() {
			select {
			case <-stop:
			case <-time.After(timeout):
				vm.Interrupt(fmt.Errorf("JS执行超时(%v)", timeout))
			}
		}()
		defer close(stop)
	}

	// 预载 jsLib（按源缓存编译结果）
	var jsLibKey, jsLib string
	if src := bindings["_jsLibKey"]; src != nil {
		jsLibKey, _ = src.(string)
	}
	if src := bindings["_jsLib"]; src != nil {
		jsLib, _ = src.(string)
	}
	if jsLib != "" {
		e.jsLibMu.Lock()
		prog, ok := e.jsLibProgs[jsLibKey]
		e.jsLibMu.Unlock()
		if !ok {
			p, err := goja.Compile("__jsLib__", jsLib, false)
			if err == nil {
				prog = p
				e.jsLibMu.Lock()
				e.jsLibProgs[jsLibKey] = p
				e.jsLibMu.Unlock()
			}
		}
		if prog != nil {
			func() {
				defer func() { _ = recover() }() // jsLib 报错不中断主脚本
				_, _ = vm.RunProgram(prog)
			}()
		}
	}

	var javaObj, srcObj, bookObj, chObj any
	for k, v := range bindings {
		switch k {
		case "java":
			javaObj = v
			continue
		case "source":
			srcObj = v
			continue
		case "book":
			bookObj = v
			continue
		case "chapter":
			chObj = v
			continue
		}
		if strings.HasPrefix(k, "_") {
			continue // 内部键不注入
		}
		_ = vm.Set(k, v)
	}
	en.InjectBindings(vm, javaObj, srcObj, bookObj, chObj)

	// goja regex 兼容预处理：
	// 1. \} 在标准 JS 正则中合法但 goja 不支持，替换为 }
	// 2. [\s\S] 在 goja 中可能存在回溯限制，替换为 [\d\D]（语义等价：匹配任意字符）
	script = strings.ReplaceAll(script, `\}`, `}`)
	script = strings.ReplaceAll(script, `[\s\S]`, `[\d\D]`)
	// 全角空格/零宽字符在 goja 解析中可能触发 ILLEGAL
	script = strings.Map(func(r rune) rune {
		switch r {
		case 0x3000, 0x200B, 0xFEFF:
			return ' '
		}
		return r
	}, script)

	// 覆写 eval：书源规则常用 eval 解析 JSON 数据，服务端返回可能含真实换行。
	// 标准 JS 不允许字符串字面量内有裸换行，此处兼容处理（先原样执行，失败则转义换行重试）。
	_ = vm.Set("eval", func(call goja.FunctionCall) goja.Value {
		code := call.Argument(0).String()
		val, err := vm.RunString(code)
		if err == nil {
			return val
		}
		// SyntaxError 时转义换行重试（书源 JSON 数据常见）
		fixed := strings.ReplaceAll(code, "\n", "\\n")
		fixed = strings.ReplaceAll(fixed, "\r", "\\r")
		val, err = vm.RunString(fixed)
		if err != nil {
			panic(vm.ToValue("eval: " + err.Error()))
		}
		return val
	})

	prog, cerr := goja.Compile("__eval__", script, false)
	if cerr != nil {
		return nil, fmt.Errorf("JS编译出错: %w", cerr)
	}
	var result any
	var runErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("JS执行出错: %v", r)
			}
		}()
		val, err := vm.RunProgram(prog)
		if err != nil {
			runErr = fmt.Errorf("JS执行出错: %w", err)
			return
		}
		result = ExportValue(vm, val)
	}()
	return result, runErr
}

// ExportValue 将 goja 值导出为 Go 值（保持 Go 原生对象引用不变）。
func ExportValue(vm *goja.Runtime, v goja.Value) any {
	if v == nil || goja.IsUndefined(v) || v == goja.Null() {
		return ""
	}
	switch v.Export() {
	case nil:
		return ""
	}
	// ExportType 保底：直接 Export
	return v.Export()
}

package analyzer

import (
	"fmt"
	"strings"
)

// ---------------- 执行引擎 ----------------

// applyJSONMode 内容为 JSON 时强制 JSON 模式（legado isJSON 语义）。
func (ar *AnalyzeRule) applyJSONMode(sr *SourceRule) {
	if ar.isJSON && sr.mode != ModeJs && sr.mode != ModeRegex {
		sr.mode = ModeJson
	}
}

// putRule 执行 @put 变量赋值（值为规则串，递归求值）。
func (ar *AnalyzeRule) putRule(putMap map[string]string) {
	for k, v := range putMap {
		ar.Put(k, ar.GetString(v))
	}
}

// GetString 单值提取（unescape=true）。
func (ar *AnalyzeRule) GetString(ruleStr string) string {
	if ruleStr == "" {
		return ""
	}
	return ar.getStringWithOpt(ruleStr, nil, false, true)
}

// GetStringUnescape 单值提取（可控制实体解码；正文规则用 false）。
func (ar *AnalyzeRule) GetStringUnescape(ruleStr string, unescape bool) string {
	if ruleStr == "" {
		return ""
	}
	return ar.getStringWithOpt(ruleStr, nil, false, unescape)
}

// GetStringContent 对指定内容提取单值。
func (ar *AnalyzeRule) GetStringContent(ruleStr string, mContent any) string {
	if ruleStr == "" {
		return ""
	}
	return ar.getStringWithOpt(ruleStr, mContent, false, true)
}

// GetStringURL 单值提取并绝对化为 URL。
func (ar *AnalyzeRule) GetStringURL(ruleStr string, mContent any) string {
	if ruleStr == "" {
		return ""
	}
	return ar.getStringWithOpt(ruleStr, mContent, true, true)
}

func (ar *AnalyzeRule) getStringWithOpt(ruleStr string, mContent any, isURL, unescape bool) string {
	ruleList := SplitSourceRule(ruleStr, false, false)
	return ar.getStringByRules(ruleList, mContent, isURL, unescape)
}

// getStringByRules 执行规则链（legado getString 主体）。
func (ar *AnalyzeRule) getStringByRules(ruleList []*SourceRule, mContent any, isURL, unescape bool) string {
	var result any
	content := mContent
	if content == nil {
		content = ar.content
	}
	if content != nil && len(ruleList) > 0 {
		result = content
		if mp, ok := asJSObject(result); ok {
			// JS 返回对象：键值直接访问
			sr := ruleList[0]
			ar.putRule(sr.putMap)
			sr.makeUpRule(nil, ar.evalJSAny, ar.Get, ar.GetString)
			switch {
			case sr.mode == ModeJs:
				result = ar.evalJSAny(sr.rule, mp)
			case sr.mode == ModeJson:
				if a, err := ar.getAnalyzeByJSON(normalizeJSONRoot(mp)); err == nil {
					result, _ = a.GetString(sr.rule)
				} else {
					result = ""
				}
			case sr.GetParamSize() > 1:
				result = sr.rule
			default:
				result = objectKeyString(mp, sr.rule)
			}
			if result != nil && sr.replaceRegex != "" {
				result = replaceRegex(fmt.Sprintf("%v", result), sr.replaceRegex, sr.replacement, sr.replaceFirst)
			}
		} else {
			for _, sr := range ruleList {
				ar.putRule(sr.putMap)
				if result == nil {
					continue
				}
				sr.makeUpRule(result, ar.evalJSAny, ar.Get, ar.GetString)
				ar.applyJSONMode(sr)
				rule := sr.rule
				if strings.TrimSpace(rule) != "" || sr.replaceRegex == "" {
					switch sr.mode {
					case ModeWebJs:
						result = "" // WebJs 需要浏览器环境，服务端不支持
					case ModeJs:
						result = ar.evalJSAny(rule, result)
					case ModeJson:
						if a, err := ar.getAnalyzeByJSON(result); err == nil {
							s, err := a.GetString(rule)
							if err != nil {
								result = ""
							} else {
								result = s
							}
						} else {
							result = ""
						}
					case ModeXPath:
						if a, err := ar.getAnalyzeByXPath(result); err == nil {
							s, err := a.GetString(rule)
							if err != nil {
								result = ""
							} else {
								result = s
							}
						} else {
							result = ""
						}
					case ModeDefault:
						if a, err := ar.getAnalyzeByJSoup(result); err == nil {
							if isURL {
								s, _ := a.GetString0(rule)
								result = s
							} else {
								s, _ := a.GetString(rule)
								result = s
							}
						} else {
							result = ""
						}
					default: // ModeRegex：返回重建后的字面规则文本
						result = rule
					}
				}
				if result != nil && sr.replaceRegex != "" {
					result = replaceRegex(fmt.Sprintf("%v", result), sr.replaceRegex, sr.replacement, sr.replaceFirst)
				}
			}
		}
	}
	if result == nil {
		result = ""
	}
	resultStr := toStringValue(result)
	str := resultStr
	if unescape && strings.Contains(resultStr, "&") {
		str = UnescapeHTML(resultStr)
	}
	if isURL {
		if strings.TrimSpace(str) == "" {
			return ar.baseURL
		}
		return AbsoluteURLString(ar.redirectURL, str)
	}
	return str
}

// GetStringList 列表提取。
func (ar *AnalyzeRule) GetStringList(ruleStr string, mContent any, isURL bool) []string {
	if ruleStr == "" {
		return nil
	}
	ruleList := SplitSourceRule(ruleStr, false, false)
	return ar.getStringListByRules(ruleList, mContent, isURL)
}

func (ar *AnalyzeRule) getStringListByRules(ruleList []*SourceRule, mContent any, isURL bool) []string {
	var result any
	content := mContent
	if content == nil {
		content = ar.content
	}
	if content != nil && len(ruleList) > 0 {
		result = content
		if mp, ok := asJSObject(result); ok {
			sr := ruleList[0]
			ar.putRule(sr.putMap)
			sr.makeUpRule(nil, ar.evalJSAny, ar.Get, ar.GetString)
			switch {
			case sr.mode == ModeJs:
				result = ar.evalJSAny(sr.rule, mp)
			case sr.mode == ModeJson:
				if a, err := ar.getAnalyzeByJSON(normalizeJSONRoot(mp)); err == nil {
					result, _ = a.GetStringList(sr.rule)
				}
			case sr.GetParamSize() > 1:
				result = sr.rule
			default:
				result = objectKeyString(mp, sr.rule)
			}
			if result != nil && sr.replaceRegex != "" {
				if list, ok := result.([]any); ok {
					out := make([]any, 0, len(list))
					for _, item := range list {
						out = append(out, replaceRegex(fmt.Sprintf("%v", item), sr.replaceRegex, sr.replacement, sr.replaceFirst))
					}
					result = out
				} else if s, ok := result.(string); ok {
					result = replaceRegex(s, sr.replaceRegex, sr.replacement, sr.replaceFirst)
				}
			}
		} else {
			for _, sr := range ruleList {
				ar.putRule(sr.putMap)
				if result == nil {
					continue
				}
				sr.makeUpRule(result, ar.evalJSAny, ar.Get, ar.GetString)
				ar.applyJSONMode(sr)
				rule := sr.rule
				if rule != "" {
					switch sr.mode {
					case ModeWebJs:
						result = ""
					case ModeJs:
						result = ar.evalJSAny(rule, result)
					case ModeJson:
						if a, err := ar.getAnalyzeByJSON(result); err == nil {
							list, err := a.GetStringList(rule)
							if err != nil {
								result = ""
							} else {
								result = list
							}
						} else {
							result = ""
						}
					case ModeXPath:
						if a, err := ar.getAnalyzeByXPath(result); err == nil {
							list, err := a.GetStringList(rule)
							if err != nil {
								result = ""
							} else {
								result = list
							}
						} else {
							result = ""
						}
					case ModeDefault:
						if a, err := ar.getAnalyzeByJSoup(result); err == nil {
							list, err := a.GetStringList(rule)
							if err != nil {
								result = ""
							} else {
								result = list
							}
						} else {
							result = ""
						}
					default:
						result = rule
					}
				}
				if result != nil && sr.replaceRegex != "" {
					if list, ok := result.([]string); ok {
						out := make([]string, 0, len(list))
						for _, item := range list {
							out = append(out, replaceRegex(item, sr.replaceRegex, sr.replacement, sr.replaceFirst))
						}
						result = out
					} else if list, ok := result.([]any); ok {
						out := make([]any, 0, len(list))
						for _, item := range list {
							out = append(out, replaceRegex(fmt.Sprintf("%v", item), sr.replaceRegex, sr.replacement, sr.replaceFirst))
						}
						result = out
					} else {
						result = replaceRegex(toStringValue(result), sr.replaceRegex, sr.replacement, sr.replaceFirst)
					}
				}
			}
		}
	}
	if result == nil {
		return nil
	}
	// String 结果按 \n 切分
	if s, ok := result.(string); ok {
		result = strings.Split(s, "\n")
	}
	if isURL {
		var urlList []string
		if list, ok := result.([]string); ok {
			for _, u := range list {
				abs := AbsoluteURLString(ar.redirectURL, u)
				if abs != "" && !containsStr(urlList, abs) {
					urlList = append(urlList, abs)
				}
			}
		} else if list, ok := result.([]any); ok {
			for _, u := range list {
				abs := AbsoluteURLString(ar.redirectURL, fmt.Sprintf("%v", u))
				if abs != "" && !containsStr(urlList, abs) {
					urlList = append(urlList, abs)
				}
			}
		}
		return urlList
	}
	switch list := result.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			out = append(out, toStringValue(item))
		}
		return out
	case nil:
		return nil
	default:
		return []string{toStringValue(result)}
	}
}

// GetElement 取单个元素/对象。
func (ar *AnalyzeRule) GetElement(ruleStr string) any {
	if ruleStr == "" {
		return nil
	}
	var result any
	content := ar.content
	ruleList := SplitSourceRule(ruleStr, true, false)
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sr := range ruleList {
			ar.putRule(sr.putMap)
			if result == nil {
				continue
			}
			sr.makeUpRule(result, ar.evalJSAny, ar.Get, ar.GetString)
			ar.applyJSONMode(sr)
			rule := sr.rule
			switch sr.mode {
			case ModeRegex:
				regs := splitNotBlank(rule, "&&")
				result = RegexGetElement(toStringValue(result), regs)
			case ModeWebJs:
				result = nil
			case ModeJs:
				result = ar.evalJSAny(rule, result)
			case ModeJson:
				if a, err := ar.getAnalyzeByJSON(result); err == nil {
					obj, err := a.GetObject(rule)
					if err != nil {
						result = nil
					} else {
						result = obj
					}
				} else {
					result = nil
				}
			case ModeXPath:
				if a, err := ar.getAnalyzeByXPath(result); err == nil {
					nodes, err := a.GetElements(rule)
					if err != nil {
						result = nil
					} else {
						result = nodesToAny(nodes)
					}
				} else {
					result = nil
				}
			default:
				if a, err := ar.getAnalyzeByJSoup(result); err == nil {
					nodes, err := a.GetElements(rule)
					if err != nil {
						result = nil
					} else {
						result = nodesToAny(nodes)
					}
				} else {
					result = nil
				}
			}
			if result != nil && sr.replaceRegex != "" {
				result = replaceRegex(toStringValue(result), sr.replaceRegex, sr.replacement, sr.replaceFirst)
			}
		}
	}
	return result
}

// GetElements 取元素列表（书单/目录列表）。
func (ar *AnalyzeRule) GetElements(ruleStr string) []any {
	var result any
	content := ar.content
	ruleList := SplitSourceRule(ruleStr, true, false)
	if content != nil && len(ruleList) > 0 {
		result = content
		for _, sr := range ruleList {
			ar.putRule(sr.putMap)
			if result == nil {
				continue
			}
			sr.makeUpRule(result, ar.evalJSAny, ar.Get, ar.GetString)
			ar.applyJSONMode(sr)
			rule := sr.rule
			switch sr.mode {
			case ModeRegex:
				regs := splitNotBlank(rule, "&&")
				result = RegexGetElements(toStringValue(result), regs)
			case ModeWebJs:
				result = nil
			case ModeJs:
				result = ar.evalJSAny(rule, result)
			case ModeJson:
				if a, err := ar.getAnalyzeByJSON(result); err == nil {
					list, err := a.GetList(rule)
					if err != nil {
						result = nil
					} else {
						result = list
					}
				} else {
					result = nil
				}
			case ModeXPath:
				if a, err := ar.getAnalyzeByXPath(result); err == nil {
					nodes, err := a.GetElements(rule)
					if err != nil {
						result = nil
					} else {
						result = nodesToAny(nodes)
					}
				} else {
					result = nil
				}
			default:
				if a, err := ar.getAnalyzeByJSoup(result); err == nil {
					nodes, err := a.GetElements(rule)
					if err != nil {
						result = nil
					} else {
						result = nodesToAny(nodes)
					}
				} else {
					result = nil
				}
			}
		}
	}
	switch v := result.(type) {
	case []any:
		return v
	case []string:
		out := make([]any, 0, len(v))
		for _, s := range v {
			out = append(out, s)
		}
		return out
	case [][]string:
		out := make([]any, 0, len(v))
		for _, g := range v {
			out = append(out, g)
		}
		return out
	case nil:
		return nil
	default:
		return []any{result}
	}
}

// EvalJS 执行 JS（绑定 java/result/baseUrl 等）。
func (ar *AnalyzeRule) EvalJS(jsStr string, result any) any {
	return ar.evalJSAny(jsStr, result)
}

func (ar *AnalyzeRule) evalJSAny(jsStr string, result any) any {
	if ar.js == nil {
		return ""
	}
	bindings := map[string]any{}
	for k, v := range ar.bindings {
		bindings[k] = v
	}
	bindings["result"] = toJSValue(result)
	bindings["baseUrl"] = ar.baseURL
	if ar.content != nil {
		bindings["src"] = toJSValue(ar.content)
	}
	if ar.source != nil {
		bindings["source"] = ar.source
	}
	if ar.chapterTitle != "" {
		bindings["title"] = ar.chapterTitle
	}
	v, err := ar.js.Eval(jsStr, bindings)
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return "JS错误: " + msg
	}
	return v
}

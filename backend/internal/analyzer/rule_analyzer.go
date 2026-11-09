package analyzer

import (
	"fmt"
	"strings"
)

// RuleAnalyzer 是 legado RuleAnalyzer.kt 的忠实移植：
// 高效切割规则，支持 && || %% 组合符（首个出现的分隔符决定组合类型），
// 分隔符在平衡组 [...] / (...) 内不生效；innerRule 支持 {{...}} 与 {$....} 内嵌替换。
// 索引基于字节（UTF-8 安全：所有语法字符均为 ASCII，多字节字符的续字节 >= 0x80 不会与之冲突）。
type RuleAnalyzer struct {
	queue        string
	pos          int
	start        int
	startX       int
	rule         []string
	step         int
	elementsType string
	code         bool     // true: 代码平衡组（JSON/JS）；false: 规则平衡组（jsoup/xpath）
	splitSplits  []string // 一段匹配的分隔符集合（供回退递归使用）
}

// NewRuleAnalyzer 创建分析器。code=true 时平衡组按代码语义处理（含转义、[] 深度）。
func NewRuleAnalyzer(data string, code bool) *RuleAnalyzer {
	return &RuleAnalyzer{queue: data, code: code}
}

// Trim 修剪当前规则之前的 "@" 或空白/控制字符（< '!'）。
func (ra *RuleAnalyzer) Trim() {
	if ra.pos < len(ra.queue) && (ra.queue[ra.pos] == '@' || ra.queue[ra.pos] < '!') {
		ra.pos++
		for ra.pos < len(ra.queue) && (ra.queue[ra.pos] == '@' || ra.queue[ra.pos] < '!') {
			ra.pos++
		}
		ra.start = ra.pos
		ra.startX = ra.pos
	}
}

// ReSetPos 重置 pos 方便复用。
func (ra *RuleAnalyzer) ReSetPos() {
	ra.pos = 0
	ra.startX = 0
}

// consumeTo 从剩余字串查找 seq，找到则 pos 停在匹配处（不包含）。
func (ra *RuleAnalyzer) consumeTo(seq string) bool {
	ra.start = ra.pos
	offset := strings.Index(ra.queue[ra.pos:], seq)
	if offset < 0 {
		return false
	}
	ra.pos += offset
	return true
}

// consumeToAny 逐位置试探，任一 seq 命中即停（最先出现的位置优先）。
func (ra *RuleAnalyzer) consumeToAny(seqs []string) bool {
	pos := ra.pos
	for pos < len(ra.queue) {
		for _, s := range seqs {
			if strings.HasPrefix(ra.queue[pos:], s) {
				ra.step = len(s)
				ra.pos = pos
				return true
			}
		}
		pos++
	}
	return false
}

// findToAny 查找任一字符首次出现位置，未找到返回 -1。
func (ra *RuleAnalyzer) findToAny(chars ...byte) int {
	pos := ra.pos
	for pos < len(ra.queue) {
		for _, c := range chars {
			if ra.queue[pos] == c {
				return pos
			}
		}
		pos++
	}
	return -1
}

// chompCodeBalanced 拉出一个代码平衡组（用于 JSON/JS）：
// 处理反斜杠转义、单双引号；[...] 深度计 depth，open/close 在 depth==0 时计 otherDepth。
func (ra *RuleAnalyzer) chompCodeBalanced(open, close byte) bool {
	pos := ra.pos
	depth := 0
	otherDepth := 0
	inSingle := false
	inDouble := false
	for {
		if pos >= len(ra.queue) {
			break
		}
		c := ra.queue[pos]
		pos++
		if c != '\\' {
			if c == '\'' && !inDouble {
				inSingle = !inSingle
			} else if c == '"' && !inSingle {
				inDouble = !inDouble
			}
			if inSingle || inDouble {
				continue
			}
			switch {
			case c == '[':
				depth++
			case c == ']':
				depth--
			case depth == 0:
				if c == open {
					otherDepth++
				} else if c == close {
					otherDepth--
				}
			}
		} else {
			pos++ // 跳过被转义字符
		}
		if pos > len(ra.queue) {
			pos = len(ra.queue)
			break
		}
		if depth <= 0 && otherDepth <= 0 {
			break
		}
	}
	if depth > 0 || otherDepth > 0 {
		return false
	}
	ra.pos = pos
	return true
}

// chompRuleBalanced 拉出一个规则平衡组（用于 jsoup/xpath）：
// 引号内转义字符无效；引号外的 '\' 转义下一个字符。
func (ra *RuleAnalyzer) chompRuleBalanced(open, close byte) bool {
	pos := ra.pos
	depth := 0
	inSingle := false
	inDouble := false
	for {
		if pos >= len(ra.queue) {
			break
		}
		c := ra.queue[pos]
		pos++
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '"' && !inSingle {
			inDouble = !inDouble
		}
		if inSingle || inDouble {
			continue
		}
		if c == '\\' {
			pos++
			if pos > len(ra.queue) {
				pos = len(ra.queue)
				break
			}
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
		}
		if depth <= 0 {
			break
		}
	}
	if depth > 0 {
		return false
	}
	ra.pos = pos
	return true
}

func (ra *RuleAnalyzer) chompBalanced(open, close byte) bool {
	if ra.code {
		return ra.chompCodeBalanced(open, close)
	}
	return ra.chompRuleBalanced(open, close)
}

// SplitRule 按分隔符切分规则（legado splitRule 的移植）。
// 首个出现的分隔符决定组合类型（&&/||/%% 不可混用）；
// 分隔符位于 [...] / (...) 平衡组内时不切分。
func (ra *RuleAnalyzer) SplitRule(splits ...string) ([]string, error) {
	ra.splitSplits = splits
	if len(splits) == 1 {
		ra.elementsType = splits[0]
		if !ra.consumeTo(splits[0]) {
			ra.rule = append(ra.rule, ra.queue[ra.startX:])
			return ra.rule, nil
		}
		ra.step = len(splits[0])
		return ra.scanStage(false)
	}
	if !ra.consumeToAny(splits) {
		ra.rule = append(ra.rule, ra.queue[ra.startX:])
		return ra.rule, nil
	}
	return ra.scanStage(true)
}

// scanStage 为 splitRule 一段/二段匹配共享的 do-while 主体的循环化移植。
// firstStage=true：首段替换语义（rule 列表此时必为空，等价追加）、结尾回退 consumeToAny；
// firstStage=false：追加语义、结尾回退 consumeTo(elementsType)。
func (ra *RuleAnalyzer) scanStage(firstStage bool) ([]string, error) {
	for {
		end := ra.pos
		ra.pos = ra.start

		for {
			st := ra.findToAny('[', '(')

			if st == -1 {
				seg := ra.queue[ra.startX:end]
				if firstStage {
					ra.rule = []string{seg}
				} else {
					ra.rule = append(ra.rule, seg)
				}
				ra.elementsType = ra.queue[end : end+ra.step]
				ra.pos = end + ra.step
				for ra.consumeTo(ra.elementsType) {
					ra.rule = append(ra.rule, ra.queue[ra.start:ra.pos])
					ra.pos += ra.step
				}
				ra.rule = append(ra.rule, ra.queue[ra.pos:])
				return ra.rule, nil
			}

			if st > end {
				seg := ra.queue[ra.startX:end]
				if firstStage {
					ra.rule = []string{seg}
				} else {
					ra.rule = append(ra.rule, seg)
				}
				ra.elementsType = ra.queue[end : end+ra.step]
				ra.pos = end + ra.step
				for ra.consumeTo(ra.elementsType) && ra.pos < st {
					ra.rule = append(ra.rule, ra.queue[ra.start:ra.pos])
					ra.pos += ra.step
				}
				if ra.pos > st {
					ra.startX = ra.start
					return ra.scanStage(false)
				}
				ra.rule = append(ra.rule, ra.queue[ra.pos:])
				return ra.rule, nil
			}

			// 平衡组出现在分隔符之前：跳过平衡组继续扫描
			ra.pos = st
			next := byte(')')
			if ra.queue[ra.pos] == '[' {
				next = ']'
			}
			if !ra.chompBalanced(ra.queue[ra.pos], next) {
				return nil, fmt.Errorf("规则 %q 后未平衡", ra.queue[:ra.start])
			}
			if end <= ra.pos {
				break // Kotlin: while (end > pos) 退出条件
			}
		}

		ra.start = ra.pos
		if firstStage {
			if !ra.consumeToAny(ra.splitSplits) {
				ra.rule = append(ra.rule, ra.queue[ra.startX:])
				return ra.rule, nil
			}
			continue // 一段匹配继续扫描
		}
		if !ra.consumeTo(ra.elementsType) {
			ra.rule = append(ra.rule, ra.queue[ra.startX:])
			return ra.rule, nil
		}
		// 二段匹配继续
	}
}

// InnerRuleBalanced 替换 {$....} 形式的内嵌规则（chompCodeBalanced 平衡组语义）。
// inner 为起始标志（如 "{$."），fr 为解析函数；返回 nil/空串时该处保留字面量并跳过。
// 若无任何成功替换返回空串（与 legado 一致，由调用方判断回退）。
// startStep/endStep 为内嵌规则参数两侧不属于规则内容的字符长度（如 "{$." 中的 "{" 长度 1）。
func (ra *RuleAnalyzer) InnerRuleBalanced(inner string, startStep, endStep int, fr func(string) string) string {
	var st strings.Builder
	for ra.consumeTo(inner) {
		posPre := ra.pos
		if ra.chompCodeBalanced('{', '}') {
			frv := fr(ra.queue[posPre+startStep : ra.pos-endStep])
			if frv != "" {
				st.WriteString(ra.queue[ra.startX:posPre])
				st.WriteString(frv)
				ra.startX = ra.pos
			}
			// chomp 成功：无论替换是否为空，pos 均已越过平衡组（保留字面量时切勿再推进）
			continue
		}
		ra.pos += len(inner) // 不平衡：跳过此 inner 继续匹配（consumeTo 保证此处 pos+len(inner) ≤ len）
	}
	if ra.startX == 0 {
		return ""
	}
	st.WriteString(ra.queue[ra.startX:])
	return st.String()
}

// InnerRulePair 替换 startStr...endStr 形式的内嵌规则（如 {{js}}，无平衡组语义，直接找到 endStr）。
// fr 返回空串时保留原文（legado 此处会产生 "null" 字样，属可证伪的缺陷，此处按意图修正）。
func (ra *RuleAnalyzer) InnerRulePair(startStr, endStr string, fr func(string) string) string {
	var st strings.Builder
	for ra.consumeTo(startStr) {
		ra.pos += len(startStr)
		posPre := ra.pos
		if ra.consumeTo(endStr) {
			frv := fr(ra.queue[posPre:ra.pos])
			st.WriteString(ra.queue[ra.startX : posPre-len(startStr)])
			if frv == "" {
				// 解析失败：保留原内嵌文本（含首尾标记）
				st.WriteString(ra.queue[posPre-len(startStr) : ra.pos+len(endStr)])
			} else {
				st.WriteString(frv)
			}
			ra.pos += len(endStr)
			ra.startX = ra.pos
		}
	}
	if ra.startX == 0 {
		return ra.queue
	}
	st.WriteString(ra.queue[ra.startX:])
	return st.String()
}

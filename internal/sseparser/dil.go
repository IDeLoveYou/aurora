package sseparser

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	dilURLAttr       = regexp.MustCompile(`(?is)\b(?:url|href)\s*=\s*(?:"([^"]*)"|'([^']*)'|\{\s*"([^"]*)"\s*\}|\{\s*'([^']*)'\s*\})`)
	dilLabelAttr     = regexp.MustCompile(`(?is)\b(?:title|label|alt)\s*=\s*(?:"([^"]*)"|'([^']*)'|\{\s*"([^"]*)"\s*\}|\{\s*'([^']*)'\s*\})`)
	internalRefLabel = regexp.MustCompile(`【turn[0-9]+[A-Za-z_]+[0-9]+(?:†L[0-9]+(?:-L[0-9]+)?)?】`)
)

// RenderDILMarkdown 将正文中的 DIL/JSX 降级为普通 Markdown。
// 未知组件只移除标签并保留内部文字，无需按组件名称持续适配。
func RenderDILMarkdown(text string) string {
	if text == "" {
		return text
	}
	text = internalRefLabel.ReplaceAllString(text, "")
	if !looksLikeDIL(text) {
		return text
	}

	var out strings.Builder
	changed := false
	for i := 0; i < len(text); {
		if text[i] == '`' {
			if end := codeSpanEnd(text, i); end > i {
				out.WriteString(text[i:end])
				i = end
				continue
			}
		}
		if text[i] == '<' {
			if end, ok := markdownAutolinkEnd(text, i); ok {
				out.WriteString(text[i:end])
				i = end
				continue
			}
			if tag, end, ok := parseDILTag(text, i); ok {
				changed = true
				if !tag.closing {
					switch strings.ToLower(tag.name) {
					case "br":
						out.WriteByte('\n')
					case "hr":
						out.WriteString("\n---\n")
					default:
						if tag.selfClosing {
							out.WriteString(tagLink(tag.body))
						}
					}
				}
				i = end
				continue
			}
		}
		if text[i] == '{' {
			if expression, end, ok := jsxExpression(text, i); ok {
				if literal, ok := jsxLiteral(expression); ok {
					changed = true
					out.WriteString(literal)
					i = end
					continue
				}
				if isDILExpression(expression) {
					changed = true
					i = end
					continue
				}
			}
		}
		out.WriteByte(text[i])
		i++
	}
	if !changed {
		return text
	}
	return cleanDILMarkdown(out.String())
}

type dilTag struct {
	name        string
	body        string
	closing     bool
	selfClosing bool
}

func looksLikeDIL(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '<' {
			continue
		}
		j := i + 1
		if j < len(text) && text[j] == '/' {
			j++
		}
		if j < len(text) && isTagNameStart(text[j]) {
			return true
		}
	}
	return strings.Contains(text, "{@body") || strings.Contains(text, "{#") || strings.Contains(text, "{:") || strings.Contains(text, "{/")
}

func parseDILTag(text string, start int) (dilTag, int, bool) {
	i := start + 1
	if i >= len(text) {
		return dilTag{}, start, false
	}
	closing := text[i] == '/'
	if closing {
		i++
	}
	if i >= len(text) || !isTagNameStart(text[i]) {
		return dilTag{}, start, false
	}
	nameStart := i
	for i < len(text) && isTagNameChar(text[i]) {
		i++
	}
	end, ok := dilTagEnd(text, i)
	if !ok {
		return dilTag{}, start, false
	}
	body := strings.TrimSpace(text[i : end-1])
	selfClosing := strings.HasSuffix(body, "/")
	if selfClosing {
		body = strings.TrimSpace(strings.TrimSuffix(body, "/"))
	}
	return dilTag{name: text[nameStart:i], body: body, closing: closing, selfClosing: selfClosing}, end, true
}

func dilTagEnd(text string, start int) (int, bool) {
	depth := 0
	var quote byte
	for i := start; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '>':
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return start, false
}

func tagLink(body string) string {
	destination := firstCapture(dilURLAttr.FindStringSubmatch(body))
	if destination == "" {
		return ""
	}
	label := firstCapture(dilLabelAttr.FindStringSubmatch(body))
	if label == "" {
		label = destination
	}
	return safeMarkdownLink(destination, label)
}

func firstCapture(match []string) string {
	if len(match) < 2 {
		return ""
	}
	for _, value := range match[1:] {
		if value != "" {
			return value
		}
	}
	return ""
}

func markerLinkFallback(inner string) string {
	fields := strings.Split(inner, "")
	for i := 1; i < len(fields); i++ {
		destination := strings.TrimSpace(fields[i])
		label := destination
		if i > 1 && strings.TrimSpace(fields[i-1]) != "" {
			label = strings.TrimSpace(fields[i-1])
		}
		if link := safeMarkdownLink(destination, label); link != "" {
			return link
		}
	}
	return ""
}

func safeMarkdownLink(destination, label string) string {
	parsed, err := url.Parse(strings.TrimSpace(destination))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	label = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(label)
	destination = strings.NewReplacer("<", "%3C", ">", "%3E").Replace(parsed.String())
	return "[" + label + "](<" + destination + ">)"
}

func markdownAutolinkEnd(text string, start int) (int, bool) {
	end := strings.IndexByte(text[start+1:], '>')
	if end < 0 {
		return start, false
	}
	end += start + 1
	parsed, err := url.Parse(text[start+1 : end])
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return start, false
	}
	return end + 1, true
}

func jsxExpression(text string, start int) (string, int, bool) {
	depth := 0
	var quote byte
	for i := start; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start+1 : i], i + 1, true
			}
		}
	}
	return "", start, false
}

func jsxLiteral(expression string) (string, bool) {
	expression = strings.TrimSpace(expression)
	if len(expression) < 2 || expression[len(expression)-1] != expression[0] {
		return "", false
	}
	switch expression[0] {
	case '"', '`':
		value, err := strconv.Unquote(expression)
		return value, err == nil
	case '\'':
		return strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(expression[1 : len(expression)-1]), true
	default:
		return "", false
	}
}

func isDILExpression(expression string) bool {
	expression = strings.TrimSpace(expression)
	if isDILBlockDirective(expression) {
		return true
	}
	if strings.HasPrefix(expression, "@body") {
		return true
	}
	if strings.Contains(expression, "GenUI.") || strings.Contains(expression, "DIL.") || strings.Contains(expression, "useState(") || strings.Contains(expression, "=>") {
		return true
	}
	return !strings.ContainsAny(expression, " \t\r\n") && strings.ContainsAny(expression, ".[")
}

func isDILBlockDirective(expression string) bool {
	expression = strings.TrimSpace(expression)
	return len(expression) > 1 && strings.ContainsRune("#:/", rune(expression[0])) && isTagNameStart(expression[1])
}

func codeSpanEnd(text string, start int) int {
	run := 1
	for start+run < len(text) && text[start+run] == '`' {
		run++
	}
	marker := strings.Repeat("`", run)
	if offset := strings.Index(text[start+run:], marker); offset >= 0 {
		return start + run + offset + run
	}
	return 0
}

func cleanDILMarkdown(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank, inFence := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out = append(out, trimmed)
			blank = false
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		trimmed = strings.NewReplacer("****", "", "____", "").Replace(trimmed)
		if trimmed == "" {
			if len(out) > 0 && !blank {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, trimmed)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isTagNameStart(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }
func isTagNameChar(c byte) bool {
	return isTagNameStart(c) || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

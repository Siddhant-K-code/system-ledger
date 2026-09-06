package ledger

import "strings"

// Split top-level statements so text in strings, comments, and routine bodies
// cannot manufacture a CREATE TABLE declaration used by source-query links.
func ddlStatements(text string) []string {
	var statements []string
	var current strings.Builder
	unsupported := false
	for i := 0; i < len(text); {
		switch {
		case strings.HasPrefix(text[i:], "--"):
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				i = len(text)
			} else {
				i += end + 1
			}
			current.WriteByte(' ')
		case strings.HasPrefix(text[i:], "/*"):
			if strings.HasPrefix(text[i:], "/*!") || strings.HasPrefix(text[i:], "/*M!") || strings.HasPrefix(text[i:], "/*m!") {
				unsupported = true
			}
			depth := 1
			i += 2
			for i < len(text) && depth > 0 {
				if strings.HasPrefix(text[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(text[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
			current.WriteByte(' ')
		case text[i] == '\'' || text[i] == '"' || text[i] == '`' || text[i] == '[':
			start, quote := i, text[i]
			if quote == '[' {
				quote = ']'
			}
			i++
			for i < len(text) {
				if text[i] == '\\' && quote == '\'' && i+1 < len(text) {
					i += 2
					continue
				}
				if text[i] == quote {
					i++
					if i < len(text) && text[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			current.WriteString(text[start:i])
		case text[i] == '$':
			end := i + 1
			for end < len(text) && ((text[end] >= 'a' && text[end] <= 'z') || (text[end] >= 'A' && text[end] <= 'Z') || (text[end] >= '0' && text[end] <= '9') || text[end] == '_') {
				end++
			}
			if end < len(text) && text[end] == '$' {
				delimiter := text[i : end+1]
				close := strings.Index(text[end+1:], delimiter)
				if close < 0 {
					i = len(text)
				} else {
					i = end + 1 + close + len(delimiter)
				}
				current.WriteString("''")
			} else {
				current.WriteByte(text[i])
				i++
			}
		case text[i] == ';':
			if !unsupported {
				statements = append(statements, current.String())
			}
			current.Reset()
			unsupported = false
			i++
		default:
			current.WriteByte(text[i])
			i++
		}
	}
	if !unsupported {
		statements = append(statements, current.String())
	}
	return statements
}

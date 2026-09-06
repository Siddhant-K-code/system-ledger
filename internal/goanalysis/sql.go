package goanalysis

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type sqlToken struct {
	text string
	kind byte // i: identifier, q: quoted identifier, s: string, v: number/parameter, p: punctuation
}

type sqlReference struct {
	schema    string
	table     string
	access    string
	operation string
}

// This is deliberately a small SQL grammar, not a search for table-looking text.
// A reference is returned only after the entire single statement is accepted.
func parseSQL(text string) (sqlReference, error) {
	tokens, err := lexSQL(text)
	if err != nil {
		return sqlReference{}, err
	}
	p := sqlParser{tokens: tokens}
	var ref sqlReference
	switch {
	case p.accept("SELECT"):
		ref.access, ref.operation = "read", "SELECT"
		p.projections()
		p.expect("FROM")
		ref.schema, ref.table = p.table()
		p.alias()
		p.where()
		if p.accept("ORDER") {
			p.expect("BY")
			for {
				p.column()
				if !p.accept("ASC") {
					p.accept("DESC")
				}
				if !p.accept(",") {
					break
				}
			}
		}
		if p.accept("LIMIT") {
			p.limit()
		}
		if p.accept("OFFSET") {
			p.limit()
		}
	case p.accept("INSERT"):
		ref.access, ref.operation = "write", "INSERT"
		p.expect("INTO")
		ref.schema, ref.table = p.table()
		if p.accept("(") {
			p.identifier()
			for p.accept(",") {
				p.identifier()
			}
			p.expect(")")
		}
		p.expect("VALUES")
		for {
			p.expect("(")
			p.exprList()
			p.expect(")")
			if !p.accept(",") {
				break
			}
		}
		p.returning()
	case p.accept("UPDATE"):
		ref.access, ref.operation = "write", "UPDATE"
		ref.schema, ref.table = p.table()
		p.expect("SET")
		for {
			p.identifier()
			p.expect("=")
			p.expr()
			if !p.accept(",") {
				break
			}
		}
		p.where()
		p.returning()
	case p.accept("DELETE"):
		ref.access, ref.operation = "write", "DELETE"
		p.expect("FROM")
		ref.schema, ref.table = p.table()
		p.where()
		p.returning()
	default:
		p.fail("expected SELECT, INSERT, UPDATE, or DELETE")
	}
	p.accept(";")
	if p.pos != len(p.tokens) {
		p.fail("unsupported or trailing SQL syntax")
	}
	if p.err != nil {
		return sqlReference{}, p.err
	}
	return ref, nil
}

func lexSQL(text string) ([]sqlToken, error) {
	var out []sqlToken
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) {
			i += n
			continue
		}
		if strings.HasPrefix(text[i:], "--") {
			if end := strings.IndexByte(text[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				break
			}
			continue
		}
		if strings.HasPrefix(text[i:], "/*") {
			if strings.HasPrefix(text[i:], "/*!") || (len(text)-i >= 4 && strings.EqualFold(text[i:i+4], "/*M!")) {
				return nil, fmt.Errorf("executable SQL comments are unsupported")
			}
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("unterminated SQL comment")
			}
			if strings.Contains(text[i+2:i+2+end], "/*") {
				return nil, fmt.Errorf("nested SQL comments are unsupported")
			}
			i += end + 4
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote, start := byte(r), i
			i++
			var value strings.Builder
			closed := false
			for i < len(text) {
				if text[i] == '\\' {
					return nil, fmt.Errorf("SQL backslash escapes are dialect-dependent and unsupported")
				}
				if text[i] == quote {
					if i+1 < len(text) && text[i+1] == quote {
						value.WriteByte(quote)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(text[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated SQL quote at byte %d", start)
			}
			kind := byte('q')
			if quote == '\'' {
				kind = 's'
			}
			out = append(out, sqlToken{value.String(), kind})
			continue
		}
		if unicode.IsLetter(r) || r == '_' {
			start := i
			i += n
			for i < len(text) {
				r, n = utf8.DecodeRuneInString(text[i:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
					break
				}
				i += n
			}
			out = append(out, sqlToken{text[start:i], 'i'})
			continue
		}
		if r >= '0' && r <= '9' {
			start := i
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				i++
			}
			if i < len(text) && text[i] == '.' {
				i++
				if i == len(text) || text[i] < '0' || text[i] > '9' {
					return nil, fmt.Errorf("invalid SQL numeric literal")
				}
				for i < len(text) && text[i] >= '0' && text[i] <= '9' {
					i++
				}
			}
			out = append(out, sqlToken{text[start:i], 'v'})
			continue
		}
		if r == '?' || r == '$' || r == ':' {
			start := i
			i++
			if r != '?' {
				for i < len(text) && ((text[i] >= 'a' && text[i] <= 'z') || (text[i] >= 'A' && text[i] <= 'Z') || (text[i] >= '0' && text[i] <= '9') || text[i] == '_') {
					i++
				}
				if i == start+1 {
					return nil, fmt.Errorf("invalid SQL parameter")
				}
			}
			out = append(out, sqlToken{text[start:i], 'v'})
			continue
		}
		if strings.ContainsRune("(),.;*+-/=<>!", r) {
			start := i
			i += n
			if i < len(text) && (r == '<' || r == '>' || r == '!') && (text[i] == '=' || (r == '<' && text[i] == '>')) {
				i++
			}
			out = append(out, sqlToken{text[start:i], 'p'})
			continue
		}
		return nil, fmt.Errorf("unsupported SQL token")
	}
	return out, nil
}

type sqlParser struct {
	tokens []sqlToken
	pos    int
	err    error
	depth  int
}

func (p *sqlParser) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *sqlParser) accept(text string) bool {
	if p.err != nil || p.pos == len(p.tokens) {
		return false
	}
	t := p.tokens[p.pos]
	if (t.kind == 'i' || t.kind == 'p') && strings.EqualFold(t.text, text) {
		p.pos++
		return true
	}
	return false
}

func (p *sqlParser) expect(text string) {
	if !p.accept(text) {
		p.fail("expected %s", text)
	}
}

const sqlReserved = " SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE JOIN LEFT RIGHT INNER OUTER CROSS FULL NATURAL ON USING WITH AS UNION INTERSECT EXCEPT GROUP HAVING ORDER BY LIMIT OFFSET RETURNING AND OR NOT IS IN LIKE NULL TRUE FALSE DISTINCT ALL CASE WHEN THEN ELSE END ASC DESC FOR CONFLICT DO DEFAULT WINDOW OVER PARTITION FILTER FETCH FIRST NEXT ROW ROWS ONLY LATERAL RECURSIVE TABLE TABLESAMPLE LOCK SHARE NOWAIT SKIP QUALIFY TOP "

func (p *sqlParser) isIdentifier() bool {
	if p.err != nil || p.pos >= len(p.tokens) {
		return false
	}
	t := p.tokens[p.pos]
	return t.text != "" && (t.kind == 'q' || (t.kind == 'i' && !strings.Contains(sqlReserved, " "+strings.ToUpper(t.text)+" ")))
}

func (p *sqlParser) identifier() string {
	if !p.isIdentifier() {
		p.fail("expected identifier")
		return ""
	}
	t := p.tokens[p.pos]
	p.pos++
	if t.kind == 'i' {
		return strings.ToLower(t.text)
	}
	return t.text
}

func (p *sqlParser) table() (string, string) {
	name := p.identifier()
	if p.accept(".") {
		return name, p.identifier()
	}
	return "", name
}

func (p *sqlParser) column() {
	p.identifier()
	if p.accept(".") {
		p.identifier()
	}
}

func (p *sqlParser) alias() {
	if p.accept("AS") {
		p.identifier()
	} else if p.isIdentifier() {
		p.identifier()
	}
}

func (p *sqlParser) exprList() {
	p.expr()
	for p.accept(",") {
		p.expr()
	}
}

func (p *sqlParser) projections() {
	for {
		if !p.accept("*") {
			if p.pos+2 < len(p.tokens) && p.isIdentifier() && p.tokens[p.pos+1].text == "." && p.tokens[p.pos+2].text == "*" {
				p.pos += 3
			} else {
				p.expr()
				p.alias()
			}
		}
		if !p.accept(",") {
			return
		}
	}
}

func (p *sqlParser) expr() {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 100 {
		p.fail("SQL expression nesting exceeds limit")
		return
	}
	if p.accept("+") || p.accept("-") {
		p.expr()
		return
	}
	switch {
	case p.err != nil:
		return
	case p.pos >= len(p.tokens):
		p.fail("expected expression")
	case p.tokens[p.pos].kind == 'v', p.tokens[p.pos].kind == 's':
		p.pos++
	case p.accept("NULL"), p.accept("TRUE"), p.accept("FALSE"):
	case p.accept("("):
		p.expr()
		p.expect(")")
	case p.isIdentifier():
		name := p.identifier()
		if p.accept("(") {
			switch strings.ToUpper(name) {
			case "COUNT":
				if !p.accept("*") {
					p.expr()
				}
				p.expect(")")
			case "SUM", "MIN", "MAX", "AVG", "COALESCE":
				p.exprList()
				p.expect(")")
			default:
				p.fail("unsupported SQL function")
			}
		} else if p.accept(".") {
			p.identifier()
		}
	default:
		p.fail("expected simple SQL expression")
	}
	for p.accept("+") || p.accept("-") || p.accept("*") || p.accept("/") {
		p.expr()
	}
}

func (p *sqlParser) where() {
	if p.accept("WHERE") {
		p.condition()
	}
}

func (p *sqlParser) condition() {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 100 {
		p.fail("SQL condition nesting exceeds limit")
		return
	}
	p.accept("NOT")
	if p.accept("(") {
		p.condition()
		p.expect(")")
	} else {
		p.expr()
		switch {
		case p.accept("="), p.accept("!="), p.accept("<>"), p.accept("<"), p.accept(">"), p.accept("<="), p.accept(">="), p.accept("LIKE"):
			p.expr()
		case p.accept("IS"):
			p.accept("NOT")
			p.expect("NULL")
		case p.accept("IN"):
			p.expect("(")
			p.exprList()
			p.expect(")")
		default:
			p.fail("expected supported predicate")
		}
	}
	for p.accept("AND") || p.accept("OR") {
		p.condition()
	}
}

func (p *sqlParser) limit() {
	if p.err == nil && p.pos < len(p.tokens) && p.tokens[p.pos].kind == 'v' {
		p.pos++
		return
	}
	p.fail("expected literal or parameter limit")
}

func (p *sqlParser) returning() {
	if p.accept("RETURNING") {
		p.projections()
	}
}

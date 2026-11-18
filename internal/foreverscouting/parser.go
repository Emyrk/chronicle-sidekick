// Package foreverscouting extracts WoW Forever collections data from SavedVariables
// without executing Lua.
package foreverscouting

import (
	"bytes"
	"fmt"
	"unicode/utf8"
)

const globalName = "ChronicleCompanionForeverDB"

type valueKind uint8

const (
	kindNil valueKind = iota
	kindFalse
	kindTrue
	kindNumber
	kindString
	kindTable
)

type value struct {
	kind        valueKind
	start, end  int
	fields      int
	collections *value
}

type parser struct {
	src []byte
	pos int
}

// Extract validates a complete data-only SavedVariables file and returns the
// exact byte slice containing its non-empty collections table expression.
func Extract(src []byte) (collections string, present bool, err error) {
	value, present, err := Locate(src)
	if err != nil || !present {
		return "", present, err
	}
	return string(src[value.Start:value.End]), true, nil
}

// Span identifies the exact collections value, excluding surrounding settings.
type Span struct{ Start, End int }

// Locate validates the entire source and returns surgical replacement offsets.
func Locate(src []byte) (span Span, present bool, err error) {
	if !utf8.Valid(src) {
		return Span{}, false, fmt.Errorf("restricted Lua is not valid UTF-8")
	}
	p := parser{src: src}
	if err := p.skip(); err != nil {
		return Span{}, false, err
	}
	name, ok := p.identifier()
	if !ok || name != globalName {
		return Span{}, false, p.errorf("expected assignment to %s", globalName)
	}
	if err := p.skip(); err != nil {
		return Span{}, false, err
	}
	if !p.take('=') {
		return Span{}, false, p.errorf("expected '='")
	}
	root, err := p.parseValue(true)
	if err != nil {
		return Span{}, false, err
	}
	if root.kind != kindTable {
		return Span{}, false, p.errorf("%s must be a table", globalName)
	}
	if err := p.skip(); err != nil {
		return Span{}, false, err
	}
	if p.pos != len(p.src) {
		return Span{}, false, p.errorf("unexpected expression or second statement")
	}
	if root.collections == nil || root.collections.kind == kindNil || root.collections.kind == kindFalse {
		return Span{}, false, nil
	}
	if root.collections.kind != kindTable {
		return Span{}, false, p.errorf("collections must be a table, nil, or false")
	}
	if root.collections.fields == 0 {
		return Span{}, false, nil
	}
	return Span{root.collections.start, root.collections.end}, true, nil
}

func (p *parser) parseValue(root bool) (value, error) {
	if err := p.skip(); err != nil {
		return value{}, err
	}
	start := p.pos
	if p.pos >= len(p.src) {
		return value{}, p.errorf("expected value")
	}
	switch p.src[p.pos] {
	case '{':
		return p.parseTable(root)
	case '\'', '"':
		_, end, err := p.quotedString()
		return value{kind: kindString, start: start, end: end}, err
	}
	if isDigit(p.src[p.pos]) || (p.src[p.pos] == '-' && p.pos+1 < len(p.src) && isDigit(p.src[p.pos+1])) {
		if err := p.number(); err != nil {
			return value{}, err
		}
		return value{kind: kindNumber, start: start, end: p.pos}, nil
	}
	ident, ok := p.identifier()
	if !ok {
		return value{}, p.errorf("expected data value")
	}
	switch ident {
	case "nil":
		return value{kind: kindNil, start: start, end: p.pos}, nil
	case "false":
		return value{kind: kindFalse, start: start, end: p.pos}, nil
	case "true":
		return value{kind: kindTrue, start: start, end: p.pos}, nil
	default:
		return value{}, p.errorf("identifier %q is not a data value", ident)
	}
}

func (p *parser) parseTable(root bool) (value, error) {
	out := value{kind: kindTable, start: p.pos}
	p.pos++
	if err := p.skip(); err != nil {
		return value{}, err
	}
	if p.take('}') {
		out.end = p.pos
		return out, nil
	}
	for {
		var key string
		var keyed bool
		if p.take('[') {
			if err := p.skip(); err != nil {
				return value{}, err
			}
			switch {
			case p.pos < len(p.src) && (p.src[p.pos] == '\'' || p.src[p.pos] == '"'):
				decoded, _, err := p.quotedString()
				if err != nil {
					return value{}, err
				}
				key = decoded
			case p.pos < len(p.src) && (isDigit(p.src[p.pos]) || (p.src[p.pos] == '-' && p.pos+1 < len(p.src) && isDigit(p.src[p.pos+1]))):
				if err := p.number(); err != nil {
					return value{}, err
				}
			default:
				return value{}, p.errorf("bracketed table keys must be quoted strings or numbers")
			}
			if err := p.skip(); err != nil {
				return value{}, err
			}
			if !p.take(']') {
				return value{}, p.errorf("expected ']'")
			}
			if err := p.skip(); err != nil {
				return value{}, err
			}
			if !p.take('=') {
				return value{}, p.errorf("expected '=' after table key")
			}
			keyed = true
		} else if p.pos < len(p.src) && isIdentStart(p.src[p.pos]) {
			saved := p.pos
			candidate, _ := p.identifier()
			if err := p.skip(); err != nil {
				return value{}, err
			}
			if p.take('=') {
				key, keyed = candidate, true
			} else {
				p.pos = saved
			}
		}
		entry, err := p.parseValue(false)
		if err != nil {
			return value{}, err
		}
		out.fields++
		if root && keyed && key == "collections" {
			if out.collections != nil {
				return value{}, p.errorf("duplicate collections field")
			}
			copy := entry
			out.collections = &copy
		}
		if err := p.skip(); err != nil {
			return value{}, err
		}
		if p.take('}') {
			out.end = p.pos
			return out, nil
		}
		if !p.take(',') && !p.take(';') {
			return value{}, p.errorf("expected table separator or '}'")
		}
		if err := p.skip(); err != nil {
			return value{}, err
		}
		if p.take('}') {
			out.end = p.pos
			return out, nil
		}
	}
}

func (p *parser) number() error {
	start := p.pos
	if p.take('-') && (p.pos >= len(p.src) || !isDigit(p.src[p.pos])) {
		return p.errorf("invalid number")
	}
	if p.pos+1 < len(p.src) && p.src[p.pos] == '0' && (p.src[p.pos+1] == 'x' || p.src[p.pos+1] == 'X') {
		p.pos += 2
		digits := p.pos
		for p.pos < len(p.src) && isHex(p.src[p.pos]) {
			p.pos++
		}
		if p.pos == digits {
			return p.errorf("invalid hexadecimal number")
		}
		return nil
	}
	for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
		p.pos++
	}
	if p.pos < len(p.src) && p.src[p.pos] == '.' {
		p.pos++
		for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			p.pos++
		}
	}
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		digits := p.pos
		for p.pos < len(p.src) && isDigit(p.src[p.pos]) {
			p.pos++
		}
		if p.pos == digits {
			return p.errorf("invalid number exponent")
		}
	}
	if p.pos == start {
		return p.errorf("invalid number")
	}
	return nil
}

func (p *parser) quotedString() (string, int, error) {
	quote := p.src[p.pos]
	p.pos++
	var decoded bytes.Buffer
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		if c == quote {
			return decoded.String(), p.pos, nil
		}
		if c == '\n' || c == '\r' {
			return "", 0, p.errorf("newline in quoted string")
		}
		if c != '\\' {
			decoded.WriteByte(c)
			continue
		}
		if p.pos >= len(p.src) {
			return "", 0, p.errorf("unfinished string escape")
		}
		esc := p.src[p.pos]
		p.pos++
		switch esc {
		case 'a':
			decoded.WriteByte('\a')
		case 'b':
			decoded.WriteByte('\b')
		case 'f':
			decoded.WriteByte('\f')
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		case 'v':
			decoded.WriteByte('\v')
		case '\\', '\'', '"':
			decoded.WriteByte(esc)
		case '\n':
			decoded.WriteByte('\n')
		case '\r':
			if p.pos < len(p.src) && p.src[p.pos] == '\n' {
				p.pos++
			}
			decoded.WriteByte('\n')
		case 'x':
			if p.pos+2 > len(p.src) || !isHex(p.src[p.pos]) || !isHex(p.src[p.pos+1]) {
				return "", 0, p.errorf("invalid hexadecimal string escape")
			}
			decoded.WriteByte(hexValue(p.src[p.pos])<<4 | hexValue(p.src[p.pos+1]))
			p.pos += 2
		case 'z':
			for p.pos < len(p.src) && isSpace(p.src[p.pos]) {
				p.pos++
			}
		default:
			if !isDigit(esc) {
				return "", 0, p.errorf("invalid string escape")
			}
			n := int(esc - '0')
			for count := 1; count < 3 && p.pos < len(p.src) && isDigit(p.src[p.pos]); count++ {
				n = n*10 + int(p.src[p.pos]-'0')
				p.pos++
			}
			if n > 255 {
				return "", 0, p.errorf("decimal string escape out of range")
			}
			decoded.WriteByte(byte(n))
		}
	}
	return "", 0, p.errorf("unterminated quoted string")
}

func (p *parser) skip() error {
	for {
		for p.pos < len(p.src) && isSpace(p.src[p.pos]) {
			p.pos++
		}
		if p.pos+1 >= len(p.src) || p.src[p.pos] != '-' || p.src[p.pos+1] != '-' {
			return nil
		}
		p.pos += 2
		if p.pos+1 < len(p.src) && p.src[p.pos] == '[' && p.src[p.pos+1] == '[' {
			p.pos += 2
			end := bytes.Index(p.src[p.pos:], []byte("]]"))
			if end < 0 {
				return p.errorf("unterminated block comment")
			}
			p.pos += end + 2
			continue
		}
		for p.pos < len(p.src) && p.src[p.pos] != '\n' {
			p.pos++
		}
	}
}

func (p *parser) identifier() (string, bool) {
	if p.pos >= len(p.src) || !isIdentStart(p.src[p.pos]) {
		return "", false
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.src) && (isIdentStart(p.src[p.pos]) || isDigit(p.src[p.pos])) {
		p.pos++
	}
	return string(p.src[start:p.pos]), true
}

func (p *parser) take(c byte) bool {
	if p.pos < len(p.src) && p.src[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("restricted Lua at byte %d: %s", p.pos, fmt.Sprintf(format, args...))
}
func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHex(c byte) bool        { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func hexValue(c byte) byte {
	if isDigit(c) {
		return c - '0'
	}
	if c >= 'a' {
		return c - 'a' + 10
	}
	return c - 'A' + 10
}
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

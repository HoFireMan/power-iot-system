package migrations

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const d1LPGExprVersion = "D1L-PGEXPR-V1"

var ErrD1LUnsupportedExpression = errors.New("unsupported D1-L PostgreSQL expression")

// SerializeD1LPGExpr serializes PostgreSQL 15's pg_get_expr output using the
// deliberately small, fail-closed D1L-PGEXPR-V1 grammar.
func SerializeD1LPGExpr(expression string) ([]byte, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, ErrD1LUnsupportedExpression
	}
	// Do not let an unsupported membership shape fall through to the generic
	// token path. In particular, the missing text[] cast must not serialize as
	// a byte-for-byte alias of the required ScalarArrayOpExpr shape.
	trimmedExpression := strings.TrimSpace(expression)
	if membershipShapeRE.MatchString(trimmedExpression) && (!balancedD1LParentheses(trimmedExpression) || !containsD1LMembership(expression)) {
		return nil, ErrD1LUnsupportedExpression
	}
	var out bytes.Buffer
	out.WriteString(d1LPGExprVersion)
	out.WriteByte(0)
	for i := 0; i < len(expression); {
		if unicode.IsSpace(rune(expression[i])) {
			i++
			continue
		}
		if end, ok := membershipAt(expression, i); ok {
			if err := writeListToken(&out, expression[i:end]); err != nil {
				return nil, err
			}
			i = end
			continue
		}
		t, next, err := nextPGToken(expression, i)
		if err != nil {
			return nil, err
		}
		if err := writeToken(&out, t.kind, t.value); err != nil {
			return nil, err
		}
		i = next
	}
	out.WriteByte(0xff)
	return out.Bytes(), nil
}

func D1LPGExprSerialize(expression string) ([]byte, error) { return SerializeD1LPGExpr(expression) }
func NormalizeD1LPGExpr(expression string) ([]byte, error) { return SerializeD1LPGExpr(expression) }

type pgToken struct {
	kind  byte
	value string
}

func writeToken(out *bytes.Buffer, kind byte, value string) error {
	if kind != 'I' && kind != 'Q' && kind != 'K' && kind != 'T' && kind != 'S' && kind != 'N' && kind != 'O' && kind != 'P' {
		return ErrD1LUnsupportedExpression
	}
	out.WriteByte(kind)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(len(value)))
	out.Write(b[:])
	out.WriteString(value)
	return nil
}
func writeListToken(out *bytes.Buffer, text string) error {
	m := membershipSubmatches(text)
	if len(m) == 0 || m[2] != "=" {
		return ErrD1LUnsupportedExpression
	}
	parts := splitSQLList(m[3])
	if len(parts) == 0 {
		return ErrD1LUnsupportedExpression
	}
	var vals []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		literal := membershipLiteralRE.FindStringSubmatch(p)
		if len(literal) == 0 {
			return ErrD1LUnsupportedExpression
		}
		v, err := decodeSQLString(literal[1])
		if err != nil {
			return ErrD1LUnsupportedExpression
		}
		vals = append(vals, v)
	}
	var value bytes.Buffer
	// The outer D1L-PGEXPR-V1 token framing is unchanged. Preserve the
	// attribute identity inside this membership value so two otherwise
	// identical ScalarArrayOpExpr values cannot collide.
	value.WriteString(strings.ToLower(m[1]))
	value.WriteByte(0)
	value.WriteString("pg_catalog.text")
	value.WriteByte(0)
	value.WriteString("pg_catalog.\"C\"")
	value.WriteByte(0)
	value.WriteByte('=')
	value.WriteByte(0)
	binary.Write(&value, binary.BigEndian, uint32(len(vals)))
	for _, v := range vals {
		binary.Write(&value, binary.BigEndian, uint32(len(v)))
		value.WriteString(v)
	}
	out.WriteByte('L')
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(value.Len()))
	out.Write(b[:])
	out.Write(value.Bytes())
	return nil
}

// PostgreSQL's deparser emits this shape for the only membership rewrite we
// accept. It intentionally excludes operators, collations, and array types
// other than text/C/equality. The explicit text[] cast is part of the bounded
// V1 shape; alternate casts and implicit coercions fail closed.
//
// Outer parentheses are handled by membershipSubmatches rather than being
// optional in this expression. Keeping them out of the regexp prevents an
// unmatched opening parenthesis from bypassing the anchored grammar.
var membershipRE = regexp.MustCompile(`(?is)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(=)\s*ANY\s*\(\s*ARRAY\s*\[\s*(.*?)\s*\]\s*::\s*(?:pg_catalog\.)?text\[\]\s*\)\s*$`)
var membershipLiteralRE = regexp.MustCompile(`(?is)^'((?:''|[^'])*)'\s*::\s*(?:pg_catalog\.)?text$`)
var membershipShapeRE = regexp.MustCompile(`(?is)^\s*(?:\(\s*)*(?:"(?:""|[^"])*"|[A-Za-z_][A-Za-z0-9_]*)\s*(?:<>|!=|<=|>=|=|<|>)\s*ANY\s*\(\s*ARRAY`)

func containsD1LMembership(expression string) bool {
	for i := 0; i < len(expression); i++ {
		if _, ok := membershipAt(expression, i); ok {
			return true
		}
	}
	return false
}

func balancedD1LParentheses(expression string) bool {
	depth := 0
	quote := false
	for i := 0; i < len(expression); i++ {
		switch expression[i] {
		case '\'':
			if quote && i+1 < len(expression) && expression[i+1] == '\'' {
				i++
				continue
			}
			quote = !quote
		case '(':
			if !quote {
				depth++
			}
		case ')':
			if !quote {
				depth--
				if depth < 0 {
					return false
				}
			}
		}
	}
	return !quote && depth == 0
}

// membershipSubmatches applies the one optional, balanced outer pair emitted
// by pg_get_expr and then matches the mandatory text[] membership cast. It is
// deliberately not a general SQL parenthesis parser: unsupported nesting and
// malformed quoting remain outside D1L-PGEXPR-V1.
func membershipSubmatches(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if text[0] != '(' {
		return membershipRE.FindStringSubmatch(text)
	}
	depth := 0
	quote := false
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if quote && i+1 < len(text) && text[i+1] == '\'' {
				i++
				continue
			}
			quote = !quote
		case '(':
			if !quote {
				depth++
			}
		case ')':
			if quote {
				continue
			}
			depth--
			if depth == 0 {
				if strings.TrimSpace(text[i+1:]) != "" {
					return nil
				}
				return membershipRE.FindStringSubmatch(strings.TrimSpace(text[1:i]))
			}
			if depth < 0 {
				return nil
			}
		}
	}
	return nil
}

func membershipAt(s string, start int) (int, bool) {
	if start >= len(s) || !(s[start] == '(' || unicode.IsLetter(rune(s[start])) || s[start] == '_') {
		return start, false
	}
	depth := 0
	quote := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if quote {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				quote = false
			}
			continue
		}
		if c == '\'' {
			quote = true
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
			if depth == 0 {
				candidate := s[start : i+1]
				if membershipSubmatches(candidate) != nil {
					return i + 1, true
				}
				return start, false
			}
		}
	}
	candidate := s[start:]
	if membershipSubmatches(candidate) != nil {
		return len(s), true
	}
	return start, false
}
func splitSQLList(s string) []string {
	var out []string
	start := 0
	quote := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if quote && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			quote = !quote
		}
		if s[i] == ',' && !quote {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
func decodeSQLString(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return "", ErrD1LUnsupportedExpression
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

func nextPGToken(s string, i int) (pgToken, int, error) {
	c := s[i]
	if c == '(' || c == ')' || c == ',' {
		return pgToken{'P', string(c)}, i + 1, nil
	}
	if c == '\'' {
		j := i + 1
		for j < len(s) {
			if s[j] == '\'' {
				if j+1 < len(s) && s[j+1] == '\'' {
					j += 2
					continue
				}
				j++
				v, err := decodeSQLString(s[i+1 : j-1])
				return pgToken{'S', v}, j, err
			}
			j++
		}
		return pgToken{}, i, ErrD1LUnsupportedExpression
	}
	if c == '"' {
		j := i + 1
		for j < len(s) {
			if s[j] == '"' {
				if j+1 < len(s) && s[j+1] == '"' {
					j += 2
					continue
				}
				return pgToken{'Q', strings.ReplaceAll(s[i+1:j], `""`, `"`)}, j + 1, nil
			}
			j++
		}
		return pgToken{}, i, ErrD1LUnsupportedExpression
	}
	if c == ':' && i+1 < len(s) && s[i+1] == ':' {
		j := i + 2
		for j < len(s) && (unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] == '_' || s[j] == '.') {
			j++
		}
		if j == i+2 {
			return pgToken{}, i, ErrD1LUnsupportedExpression
		}
		typ := s[i+2 : j]
		if !strings.Contains(typ, ".") {
			typ = "pg_catalog." + typ
		}
		return pgToken{'T', typ}, j, nil
	}
	if unicode.IsDigit(rune(c)) {
		j := i + 1
		for j < len(s) && (unicode.IsDigit(rune(s[j])) || s[j] == '.') {
			j++
		}
		return pgToken{'N', s[i:j]}, j, nil
	}
	if unicode.IsLetter(rune(c)) || c == '_' {
		j := i + 1
		for j < len(s) && (unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] == '_' || s[j] == '.') {
			j++
		}
		v := strings.ToLower(s[i:j])
		if strings.Contains(v, ".") {
			return pgToken{'I', v}, j, nil
		}
		switch v {
		case "and", "or", "is", "not", "null", "true", "false", "any", "array":
			return pgToken{'K', v}, j, nil
		}
		return pgToken{'I', v}, j, nil
	}
	for _, op := range []string{"<>", "<=", ">=", "!=", "=", "<", ">", "+", "-", "*", "/"} {
		if strings.HasPrefix(s[i:], op) {
			return pgToken{'O', op}, i + len(op), nil
		}
	}
	return pgToken{}, i, fmt.Errorf("%w: byte %q", ErrD1LUnsupportedExpression, c)
}

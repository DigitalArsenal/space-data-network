package fbverify

import (
	"fmt"
	"strconv"
	"strings"
)

// The parser reads the FlatBuffers IDL subset the Space Data Standards use:
// include, namespace, attribute, enum, union, struct, table, root_type,
// file_identifier and file_extension. It keeps only what memory-safe
// verification needs — field slots, wire types, struct layouts and union
// members — and refuses anything it does not understand, so a schema the
// verifier cannot model never silently verifies as "anything goes".

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	line int
}

func lex(src string) ([]token, error) {
	var out []token
	line := 1
	i := 0
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated block comment", line)
			}
			line += strings.Count(src[i:i+2+end+2], "\n")
			i += 2 + end + 2
		case c == '"':
			j := i + 1
			var sb strings.Builder
			for j < n && src[j] != '"' {
				if src[j] == '\\' && j+1 < n {
					sb.WriteByte(src[j+1])
					j += 2
					continue
				}
				if src[j] == '\n' {
					return nil, fmt.Errorf("line %d: newline in string literal", line)
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= n {
				return nil, fmt.Errorf("line %d: unterminated string literal", line)
			}
			out = append(out, token{kind: tokString, text: sb.String(), line: line})
			i = j + 1
		case isIdentStart(c):
			j := i + 1
			for j < n && isIdentPart(src[j]) {
				j++
			}
			out = append(out, token{kind: tokIdent, text: src[i:j], line: line})
			i = j
		case isDigit(c) || ((c == '-' || c == '+') && i+1 < n && (isDigit(src[i+1]) || src[i+1] == '.')) || (c == '.' && i+1 < n && isDigit(src[i+1])):
			j := i + 1
			for j < n && (isIdentPart(src[j]) || src[j] == '.' ||
				((src[j] == '-' || src[j] == '+') && (src[j-1] == 'e' || src[j-1] == 'E') && !strings.HasPrefix(strings.ToLower(src[i:j]), "0x"))) {
				j++
			}
			out = append(out, token{kind: tokNumber, text: src[i:j], line: line})
			i = j
		case strings.IndexByte("{}()[]:;,=.<>", c) >= 0:
			out = append(out, token{kind: tokPunct, text: string(c), line: line})
			i++
		case c == '-' || c == '+':
			// a sign before an identifier default such as -inf
			out = append(out, token{kind: tokPunct, text: string(c), line: line})
			i++
		default:
			return nil, fmt.Errorf("line %d: unexpected character %q", line, c)
		}
	}
	out = append(out, token{kind: tokEOF, line: line})
	return out, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Parsed declarations, before cross-file resolution.

type rawType struct {
	vector   bool
	arrayLen int // > 0 for a fixed-length struct array [T:N]
	name     string
}

type rawField struct {
	name  string
	typ   rawType
	attrs map[string]string
	line  int
}

type rawDecl struct {
	kind       string // table, struct, enum, union
	name       string // fully qualified
	namespace  string
	fields     []rawField
	underlying string // enum underlying type
	members    []rawUnionMember
	attrs      map[string]string
	line       int
}

type rawUnionMember struct {
	typeName string
	value    int64
}

type rawFile struct {
	includes []string
	decls    []*rawDecl
	rootType string
	rootNS   string
	ident    string
}

type parser struct {
	toks []token
	pos  int
	ns   string
	file *rawFile
}

func parseFile(src string) (*rawFile, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, file: &rawFile{}}
	if err := p.parse(); err != nil {
		return nil, err
	}
	return p.file, nil
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", p.peek().line, fmt.Sprintf(format, args...))
}

func (p *parser) expectPunct(s string) error {
	t := p.next()
	if t.kind != tokPunct || t.text != s {
		return fmt.Errorf("line %d: expected %q, found %q", t.line, s, t.text)
	}
	return nil
}

func (p *parser) acceptPunct(s string) bool {
	t := p.peek()
	if t.kind == tokPunct && t.text == s {
		p.pos++
		return true
	}
	return false
}

func (p *parser) ident() (string, error) {
	t := p.next()
	if t.kind != tokIdent {
		return "", fmt.Errorf("line %d: expected identifier, found %q", t.line, t.text)
	}
	return t.text, nil
}

func (p *parser) qualifiedIdent() (string, error) {
	name, err := p.ident()
	if err != nil {
		return "", err
	}
	for p.acceptPunct(".") {
		part, err := p.ident()
		if err != nil {
			return "", err
		}
		name += "." + part
	}
	return name, nil
}

func (p *parser) qualify(name string) string {
	if p.ns == "" {
		return name
	}
	return p.ns + "." + name
}

func (p *parser) parse() error {
	for {
		t := p.peek()
		if t.kind == tokEOF {
			return nil
		}
		if t.kind != tokIdent {
			return p.errf("unexpected %q at top level", t.text)
		}
		switch t.text {
		case "include", "native_include":
			p.next()
			s := p.next()
			if s.kind != tokString {
				return p.errf("include needs a string path")
			}
			if t.text == "include" {
				p.file.includes = append(p.file.includes, s.text)
			}
			if err := p.expectPunct(";"); err != nil {
				return err
			}
		case "namespace":
			p.next()
			ns, err := p.qualifiedIdent()
			if err != nil {
				return err
			}
			p.ns = ns
			if err := p.expectPunct(";"); err != nil {
				return err
			}
		case "attribute":
			p.next()
			s := p.next()
			if s.kind != tokString && s.kind != tokIdent {
				return p.errf("attribute needs a name")
			}
			if err := p.expectPunct(";"); err != nil {
				return err
			}
		case "root_type":
			p.next()
			name, err := p.qualifiedIdent()
			if err != nil {
				return err
			}
			p.file.rootType = name
			p.file.rootNS = p.ns
			if err := p.expectPunct(";"); err != nil {
				return err
			}
		case "file_identifier", "file_extension":
			p.next()
			s := p.next()
			if s.kind != tokString {
				return p.errf("%s needs a string", t.text)
			}
			if t.text == "file_identifier" {
				p.file.ident = s.text
			}
			if err := p.expectPunct(";"); err != nil {
				return err
			}
		case "table", "struct":
			if err := p.parseTableOrStruct(); err != nil {
				return err
			}
		case "enum", "union":
			if err := p.parseEnumOrUnion(); err != nil {
				return err
			}
		default:
			return p.errf("unsupported declaration %q", t.text)
		}
	}
}

func (p *parser) parseMetadata() (map[string]string, error) {
	attrs := map[string]string{}
	if !p.acceptPunct("(") {
		return attrs, nil
	}
	for !p.acceptPunct(")") {
		key, err := p.ident()
		if err != nil {
			return nil, err
		}
		value := ""
		if p.acceptPunct(":") {
			v := p.next()
			if v.kind == tokEOF || (v.kind == tokPunct && v.text != "-" && v.text != "+") {
				return nil, fmt.Errorf("line %d: bad attribute value for %s", v.line, key)
			}
			value = v.text
			if v.kind == tokPunct {
				w := p.next()
				value += w.text
			}
		}
		attrs[key] = value
		if !p.acceptPunct(",") {
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			break
		}
	}
	return attrs, nil
}

func (p *parser) parseType() (rawType, error) {
	if p.acceptPunct("[") {
		inner, err := p.qualifiedIdent()
		if err != nil {
			return rawType{}, err
		}
		rt := rawType{vector: true, name: inner}
		if p.acceptPunct(":") {
			t := p.next()
			if t.kind != tokNumber {
				return rawType{}, fmt.Errorf("line %d: array length must be a number", t.line)
			}
			n, err := strconv.Atoi(t.text)
			if err != nil || n <= 0 || n > 1<<16 {
				return rawType{}, fmt.Errorf("line %d: bad array length %q", t.line, t.text)
			}
			rt.vector = false
			rt.arrayLen = n
		}
		if err := p.expectPunct("]"); err != nil {
			return rawType{}, err
		}
		return rt, nil
	}
	name, err := p.qualifiedIdent()
	if err != nil {
		return rawType{}, err
	}
	return rawType{name: name}, nil
}

// skipDefault consumes a field default value: a scalar, an enum name (or a
// space-separated flag list), a signed inf/nan, or [] / null.
func (p *parser) skipDefault() error {
	depth := 0
	for {
		t := p.peek()
		switch {
		case t.kind == tokEOF:
			return p.errf("unterminated default value")
		case t.kind == tokPunct && (t.text == "[" || t.text == "{"):
			depth++
		case t.kind == tokPunct && (t.text == "]" || t.text == "}"):
			depth--
		case depth == 0 && t.kind == tokPunct && (t.text == "(" || t.text == ";"):
			return nil
		}
		p.next()
	}
}

func (p *parser) parseTableOrStruct() error {
	kw := p.next().text
	line := p.peek().line
	name, err := p.ident()
	if err != nil {
		return err
	}
	attrs, err := p.parseMetadata()
	if err != nil {
		return err
	}
	decl := &rawDecl{kind: kw, name: p.qualify(name), namespace: p.ns, attrs: attrs, line: line}
	if err := p.expectPunct("{"); err != nil {
		return err
	}
	for !p.acceptPunct("}") {
		fline := p.peek().line
		fname, err := p.ident()
		if err != nil {
			return err
		}
		if err := p.expectPunct(":"); err != nil {
			return err
		}
		ft, err := p.parseType()
		if err != nil {
			return err
		}
		if p.acceptPunct("=") {
			if err := p.skipDefault(); err != nil {
				return err
			}
		}
		fattrs, err := p.parseMetadata()
		if err != nil {
			return err
		}
		if err := p.expectPunct(";"); err != nil {
			return err
		}
		decl.fields = append(decl.fields, rawField{name: fname, typ: ft, attrs: fattrs, line: fline})
	}
	p.file.decls = append(p.file.decls, decl)
	return nil
}

func (p *parser) parseEnumOrUnion() error {
	kw := p.next().text
	line := p.peek().line
	name, err := p.ident()
	if err != nil {
		return err
	}
	decl := &rawDecl{kind: kw, name: p.qualify(name), namespace: p.ns, line: line}
	if kw == "enum" {
		if err := p.expectPunct(":"); err != nil {
			return err
		}
		under, err := p.ident()
		if err != nil {
			return err
		}
		decl.underlying = under
	} else if p.acceptPunct(":") {
		// union with an explicit underlying type is not part of the SDS subset
		return p.errf("union %s: explicit underlying type is not supported", name)
	}
	attrs, err := p.parseMetadata()
	if err != nil {
		return err
	}
	decl.attrs = attrs
	if err := p.expectPunct("{"); err != nil {
		return err
	}
	next := int64(0)
	if kw == "union" {
		next = 1 // 0 is NONE
	}
	for !p.acceptPunct("}") {
		var memberName string
		if kw == "union" {
			first, err := p.qualifiedIdent()
			if err != nil {
				return err
			}
			memberName = first
			if p.acceptPunct(":") { // alias: Type
				target, err := p.qualifiedIdent()
				if err != nil {
					return err
				}
				memberName = target
			}
		} else {
			if _, err := p.ident(); err != nil {
				return err
			}
		}
		value := next
		if p.acceptPunct("=") {
			neg := p.acceptPunct("-")
			t := p.next()
			if t.kind != tokNumber {
				return fmt.Errorf("line %d: enum value must be a number", t.line)
			}
			v, err := strconv.ParseInt(t.text, 0, 64)
			if err != nil {
				u, uerr := strconv.ParseUint(t.text, 0, 64)
				if uerr != nil {
					return fmt.Errorf("line %d: bad enum value %q", t.line, t.text)
				}
				v = int64(u)
			}
			if neg {
				v = -v
			}
			value = v
		}
		if _, err := p.parseMetadata(); err != nil {
			return err
		}
		if kw == "union" {
			if memberName == "NONE" {
				return p.errf("union %s: NONE is implicit", name)
			}
			decl.members = append(decl.members, rawUnionMember{typeName: memberName, value: value})
		}
		next = value + 1
		if !p.acceptPunct(",") {
			if err := p.expectPunct("}"); err != nil {
				return err
			}
			break
		}
	}
	p.file.decls = append(p.file.decls, decl)
	return nil
}

package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pages build markup in JavaScript from data out of the database: names and
// e-mails people register in Keycloak, reservation notes, project texts, bank
// data. The pentest of 2026-10-23 (F-02) found a self-registered name pasted
// into the admin payment search as HTML.
//
// The rule this test holds the pages to: every ${...} inside a template
// literal that contains markup is escaped (esc, jsArg, encodeURIComponent,
// see web/static/js/html.js), is a number, or is markup built under this same
// rule and named ...Html. Text set with textContent needs none of it.
func TestScriptMarkupEscapesData(t *testing.T) {
	files, err := filepath.Glob("../../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	js, _ := filepath.Glob("../../web/static/js/*.js")
	for _, f := range js {
		// Vendored and minified: not ours to hold to this rule.
		if !strings.Contains(f, ".min.") && !strings.HasSuffix(f, "tailwind.js") {
			files = append(files, f)
		}
	}

	checked := 0
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		scripts := []string{string(src)}
		if strings.HasSuffix(file, ".html") {
			scripts = scriptBlocks(string(src))
		}
		for _, script := range scripts {
			for _, lit := range templateLiterals(script) {
				if !strings.Contains(lit.text, "<") {
					continue
				}
				for _, expr := range lit.exprs {
					checked++
					if !markupSafe(expr) {
						t.Errorf("%s: ${%s} goes into markup unescaped; wrap it in esc() (jsArg() inside an inline handler)", filepath.Base(file), expr)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no markup template literals at all; the scanner is broken")
	}
}

// The scanner must catch what the pentest found, or the test above proves
// nothing.
func TestScriptMarkupScannerCatchesTheAdminSearchXSS(t *testing.T) {
	before := "html += `\n" +
		"  <div class=\"user-item\" onclick=\"selectUser(${user.id}, '${escapedName}', '${escapedEmail}', this)\">\n" +
		"    <div style=\"font-weight: 600;\">${displayName}</div>\n" +
		"    <div>${user.email}${user.payments_id ? ' | VS: ' + user.payments_id : ''}</div>\n" +
		"  </div>`;\n" +
		"const re = /'/g; // a quote inside a regex is not a string\n" +
		"const nested = `<b>${ok ? `<i>${name}</i>` : esc(other)}</b>`;"

	var unsafe []string
	for _, lit := range templateLiterals(before) {
		if !strings.Contains(lit.text, "<") {
			continue
		}
		for _, expr := range lit.exprs {
			if !markupSafe(expr) {
				unsafe = append(unsafe, expr)
			}
		}
	}
	want := []string{"escapedName", "escapedEmail", "displayName", "user.email", "user.payments_id ? ' | VS: ' + user.payments_id : ''", "name", "ok ? `<i>${name}</i>` : esc(other)"}
	if strings.Join(unsafe, "\n") != strings.Join(want, "\n") {
		t.Errorf("flagged:\n%s\nwant:\n%s", strings.Join(unsafe, "\n"), strings.Join(want, "\n"))
	}
}

var (
	escapingCall = regexp.MustCompile(`^(esc|jsArg|escapeHtml|encodeURIComponent)\(`)
	builtMarkup  = regexp.MustCompile(`^\w+Html\b`)
	numericID    = regexp.MustCompile(`^(\w+\.)?(id|\w+Id)$`)
	formatted    = regexp.MustCompile(`^\w+\.toLocaleString\([^()]*\)$`)
)

// knownSafe are expressions that are neither escaped nor numbers, and why
// they need neither.
var knownSafe = map[string]string{
	"isPrimary ? 'primary' : ''": "a constant class",
	"noteTitle":                  "a title attribute built with esc()",
	"c.bg":                       "admin_settings: a constant class",
	"c.text":                     "admin_settings: a constant class",
	"c.icon":                     "admin_settings: a constant symbol",
	"label":                      "admin_settings: a constant label or a block name",
	"block.name":                 "admin_settings: block names come from the server's fixed list",
	"modifiedMark":               "admin_settings: constant markup",
	"rows":                       "admin_settings: a number",
}

func markupSafe(expr string) bool {
	if _, ok := knownSafe[expr]; ok {
		return true
	}
	if builtMarkup.MatchString(expr) || numericID.MatchString(expr) || formatted.MatchString(expr) {
		return true
	}
	// One escaping call around the whole expression, not esc(a) + b.
	if loc := escapingCall.FindStringIndex(expr); loc != nil {
		return closingParen(expr, loc[1]-1) == len(expr)-1
	}
	return false
}

// closingParen returns the index of the parenthesis closing the one at open.
func closingParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

var scriptTag = regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`)

func scriptBlocks(html string) []string {
	var blocks []string
	for _, m := range scriptTag.FindAllStringSubmatch(html, -1) {
		blocks = append(blocks, m[1])
	}
	return blocks
}

type templateLiteral struct {
	text  string   // the literal's own text, without the ${...} parts
	exprs []string // the ${...} expressions, trimmed
}

// templateLiterals finds the template literals in JavaScript source,
// including ones nested in another literal's ${...}. It knows just enough
// JavaScript to skip strings, comments and regex literals.
func templateLiterals(src string) []templateLiteral {
	s := &jsScanner{src: src}
	s.code(false)
	return s.found
}

type jsScanner struct {
	src   string
	pos   int
	found []templateLiteral
}

// code scans until the end, or with inExpr until the } that closes a ${.
func (s *jsScanner) code(inExpr bool) {
	depth := 0
	prev := byte(0) // last significant character, to tell a regex from a division
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '/' && s.peek(1) == '/':
			for s.pos < len(s.src) && s.src[s.pos] != '\n' {
				s.pos++
			}
			continue
		case c == '/' && s.peek(1) == '*':
			end := strings.Index(s.src[s.pos+2:], "*/")
			if end < 0 {
				s.pos = len(s.src)
				return
			}
			s.pos += end + 4
			continue
		case c == '\'' || c == '"':
			s.quoted(c)
		case c == '`':
			s.pos++
			s.literal()
			prev = '`'
			continue
		case c == '/' && (prev == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", prev) >= 0):
			s.regex()
		case c == '{':
			depth++
		case c == '}':
			if inExpr && depth == 0 {
				s.pos++
				return
			}
			depth--
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			prev = c
		}
		s.pos++
	}
}

func (s *jsScanner) peek(n int) byte {
	if s.pos+n < len(s.src) {
		return s.src[s.pos+n]
	}
	return 0
}

// quoted skips a '...' or "..." string; pos ends on the closing quote.
func (s *jsScanner) quoted(q byte) {
	for s.pos++; s.pos < len(s.src) && s.src[s.pos] != q; s.pos++ {
		if s.src[s.pos] == '\\' {
			s.pos++
		}
	}
}

// regex skips a regex literal; pos ends on the closing slash.
func (s *jsScanner) regex() {
	inClass := false
	for s.pos++; s.pos < len(s.src); s.pos++ {
		switch s.src[s.pos] {
		case '\\':
			s.pos++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				return
			}
		case '\n':
			return
		}
	}
}

// literal reads a template literal whose opening backtick is behind pos.
func (s *jsScanner) literal() {
	var lit templateLiteral
	var text strings.Builder
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '\\':
			text.WriteString(s.src[s.pos:min(s.pos+2, len(s.src))])
			s.pos += 2
		case c == '`':
			s.pos++
			lit.text = text.String()
			s.found = append(s.found, lit)
			return
		case c == '$' && s.peek(1) == '{':
			s.pos += 2
			start := s.pos
			s.code(true)
			lit.exprs = append(lit.exprs, strings.TrimSpace(s.src[start:s.pos-1]))
		default:
			text.WriteByte(c)
			s.pos++
		}
	}
}

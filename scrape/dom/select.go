package dom

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/scrape/rx"
	"golang.org/x/net/html"
)

// Selector is a parsed jsoup selector. Matching follows jsoup's evaluators: a selection from an element includes
// that element itself, and combinators look at ancestors and siblings only up to the element selected from (the
// root).
type Selector struct {
	alternatives []complexSel // the comma-separated parts
	text         string
}

func (s *Selector) String() string { return s.text }

// complexSel is compound selectors joined by combinators, left to right. A relative selector (":has(> p)") starts
// with a combinator: its first compound is the root itself.
type complexSel struct {
	parts       []compound
	combinators []byte // combinators[i] joins parts[i] and parts[i+1]: ' ', '>', '+', '~'
	relative    byte   // the leading combinator of a relative selector, or 0
}

type compound []matcher

type matcher func(el, root *html.Node) bool

// Compile parses a jsoup selector.
func Compile(selector string) (*Selector, error) {
	p := &parser{s: selector}
	alts, err := p.group()
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	return &Selector{alternatives: alts, text: selector}, nil
}

// MustCompile is Compile for selectors known to be valid.
func MustCompile(selector string) *Selector {
	s, err := Compile(selector)
	if err != nil {
		panic(err)
	}
	return s
}

// Select is jsoup's Element.select: every element in root's subtree, root included, that matches, in document
// order.
func Select(root *html.Node, sel *Selector) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && sel.matches(n, root) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// SelectFirst is jsoup's Element.selectFirst: the first match in document order, or nil.
func SelectFirst(root *html.Node, sel *Selector) *html.Node {
	var found *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && sel.matches(n, root) {
			found = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
	return found
}

// Matches reports whether el matches sel, selecting from root.
func (s *Selector) Matches(el, root *html.Node) bool { return s.matches(el, root) }

func (s *Selector) matches(el, root *html.Node) bool {
	for i := range s.alternatives {
		if s.alternatives[i].matches(el, root) {
			return true
		}
	}
	return false
}

func (c *complexSel) matches(el, root *html.Node) bool {
	return c.matchAt(len(c.parts)-1, el, root)
}

func (c *complexSel) matchAt(i int, el, root *html.Node) bool {
	for _, m := range c.parts[i] {
		if !m(el, root) {
			return false
		}
	}
	var comb byte
	if i == 0 {
		if c.relative == 0 {
			return true
		}
		comb = c.relative
	} else {
		comb = c.combinators[i-1]
	}
	left := func(n *html.Node) bool {
		if i == 0 {
			return n == root // the implied root of a relative selector
		}
		return c.matchAt(i-1, n, root)
	}
	if el == root {
		return false
	}
	switch comb {
	case ' ':
		for p := el.Parent; p != nil; p = p.Parent {
			if p.Type == html.ElementNode || p == root {
				if left(p) {
					return true
				}
			}
			if p == root {
				break
			}
		}
		return false
	case '>':
		p := el.Parent
		return p != nil && left(p)
	case '+':
		p := prevElement(el)
		return p != nil && left(p)
	case '~':
		for p := prevElement(el); p != nil; p = prevElement(p) {
			if left(p) {
				return true
			}
		}
		return false
	}
	return false
}

func prevElement(n *html.Node) *html.Node {
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

func nextElement(n *html.Node) *html.Node {
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			return s
		}
	}
	return nil
}

// --- parsing ---

type parser struct {
	s string
	i int
}

func (p *parser) eof() bool { return p.i >= len(p.s) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.s[p.i]
}

func (p *parser) skipSpace() bool {
	start := p.i
	for !p.eof() && isSpace(p.s[p.i]) {
		p.i++
	}
	return p.i > start
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func (p *parser) group() ([]complexSel, error) {
	var alts []complexSel
	for {
		c, err := p.complex()
		if err != nil {
			return nil, err
		}
		alts = append(alts, c)
		p.skipSpace()
		if p.eof() {
			return alts, nil
		}
		if p.peek() != ',' {
			return nil, fmt.Errorf("unexpected %q at %d", p.s[p.i:], p.i)
		}
		p.i++
	}
}

func (p *parser) complex() (complexSel, error) {
	var c complexSel
	p.skipSpace()
	if b := p.peek(); b == '>' || b == '+' || b == '~' {
		c.relative = b
		p.i++
		p.skipSpace()
	}
	for {
		comp, err := p.compound()
		if err != nil {
			return c, err
		}
		if len(comp) == 0 {
			return c, fmt.Errorf("expected a selector at %d", p.i)
		}
		c.parts = append(c.parts, comp)
		white := p.skipSpace()
		if p.eof() || p.peek() == ',' {
			return c, nil
		}
		switch b := p.peek(); b {
		case '>', '+', '~':
			p.i++
			p.skipSpace()
			c.combinators = append(c.combinators, b)
		default:
			if !white {
				return c, fmt.Errorf("unexpected %q at %d", p.s[p.i:], p.i)
			}
			c.combinators = append(c.combinators, ' ')
		}
	}
}

func isWordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c >= 0x80
}

func (p *parser) word() string {
	start := p.i
	for !p.eof() && (isWordChar(p.s[p.i]) || p.s[p.i] == '\\') {
		if p.s[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	return strings.ReplaceAll(p.s[start:p.i], `\`, "")
}

// balanced returns the text between the open char at p.i and its matching close, honouring quotes.
func (p *parser) balanced(open, close byte) (string, error) {
	if p.peek() != open {
		return "", fmt.Errorf("expected %q at %d", open, p.i)
	}
	depth := 0
	var quote byte
	start := p.i + 1
	for ; !p.eof(); p.i++ {
		c := p.s[p.i]
		if c == '\\' {
			p.i++
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				inner := p.s[start:p.i]
				p.i++
				return inner, nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced %q", open)
}

func (p *parser) compound() (compound, error) {
	var comp compound
	for !p.eof() {
		switch c := p.peek(); {
		case c == '*':
			p.i++
			comp = append(comp, func(el, _ *html.Node) bool { return true })
		case c == '#':
			p.i++
			id := p.word()
			if id == "" {
				return nil, fmt.Errorf("empty id at %d", p.i)
			}
			comp = append(comp, func(el, _ *html.Node) bool { return Attr(el, "id") == id })
		case c == '.':
			p.i++
			class := strings.ToLower(p.word())
			if class == "" {
				return nil, fmt.Errorf("empty class at %d", p.i)
			}
			comp = append(comp, func(el, _ *html.Node) bool { return hasClass(el, class) })
		case c == '[':
			inner, err := p.balanced('[', ']')
			if err != nil {
				return nil, err
			}
			m, err := attributeMatcher(inner)
			if err != nil {
				return nil, err
			}
			comp = append(comp, m)
		case c == ':':
			p.i++
			m, err := p.pseudo()
			if err != nil {
				return nil, err
			}
			comp = append(comp, m)
		case isWordChar(c) || c == '|':
			start := p.i
			for !p.eof() && (isWordChar(p.s[p.i]) || p.s[p.i] == '|' || p.s[p.i] == ':' && p.i+1 < len(p.s) && isWordChar(p.s[p.i+1]) && strings.Contains(p.s[start:p.i], "|")) {
				p.i++
			}
			tag := strings.ToLower(p.s[start:p.i])
			if strings.HasPrefix(tag, "*|") {
				tag = tag[2:]
			}
			tag = strings.ReplaceAll(tag, "|", ":")
			comp = append(comp, func(el, _ *html.Node) bool { return el.Type == html.ElementNode && el.Data == tag })
		default:
			return comp, nil
		}
	}
	return comp, nil
}

func hasClass(el *html.Node, class string) bool {
	for _, c := range strings.Fields(Attr(el, "class")) {
		if strings.ToLower(c) == class {
			return true
		}
	}
	return false
}

// attributeMatcher is jsoup's QueryParser.byAttribute on the text inside [ ].
func attributeMatcher(inner string) (matcher, error) {
	ops := []string{"!=", "^=", "$=", "*=", "~=", "="}
	opAt, op := -1, ""
	for i := 0; i < len(inner) && opAt < 0; i++ {
		for _, o := range ops {
			if strings.HasPrefix(inner[i:], o) {
				opAt, op = i, o
				break
			}
		}
	}
	if opAt < 0 {
		key := strings.ToLower(strings.TrimSpace(inner))
		if key == "" {
			return nil, fmt.Errorf("empty attribute selector")
		}
		if strings.HasPrefix(key, "^") {
			prefix := key[1:]
			return func(el, _ *html.Node) bool {
				for _, a := range el.Attr {
					if strings.HasPrefix(strings.ToLower(a.Key), prefix) {
						return true
					}
				}
				return false
			}, nil
		}
		return func(el, _ *html.Node) bool { return HasAttr(el, key) }, nil
	}
	key := strings.ToLower(strings.TrimSpace(inner[:opAt]))
	raw := strings.TrimLeft(inner[opAt+len(op):], " \t\n\r\f")
	if key == "" {
		return nil, fmt.Errorf("empty attribute name in [%s]", inner)
	}
	if op == "~=" {
		re, err := rx.Compile(raw)
		if err != nil {
			return nil, err
		}
		return func(el, _ *html.Node) bool {
			v, ok := attr(el, key)
			return ok && re.Find(v)
		}, nil
	}
	literal := len(raw) >= 2 && (raw[0] == '\'' && raw[len(raw)-1] == '\'' || raw[0] == '"' && raw[len(raw)-1] == '"')
	value := raw
	if literal {
		value = raw[1 : len(raw)-1]
	} else {
		value = strings.TrimSpace(value)
	}
	value = strings.ToLower(value)
	switch op {
	case "=":
		return func(el, _ *html.Node) bool {
			v, ok := attr(el, key)
			return ok && strings.EqualFold(value, strings.TrimSpace(v))
		}, nil
	case "!=":
		return func(el, _ *html.Node) bool { return !strings.EqualFold(value, Attr(el, key)) }, nil
	case "^=":
		return func(el, _ *html.Node) bool {
			v, ok := attr(el, key)
			return ok && strings.HasPrefix(strings.ToLower(v), value)
		}, nil
	case "$=":
		return func(el, _ *html.Node) bool {
			v, ok := attr(el, key)
			return ok && strings.HasSuffix(strings.ToLower(v), value)
		}, nil
	default: // *=
		return func(el, _ *html.Node) bool {
			v, ok := attr(el, key)
			return ok && strings.Contains(strings.ToLower(v), value)
		}, nil
	}
}

func (p *parser) pseudo() (matcher, error) {
	name := strings.ToLower(p.word())
	var arg string
	hasArg := false
	if p.peek() == '(' {
		a, err := p.balanced('(', ')')
		if err != nil {
			return nil, err
		}
		arg, hasArg = a, true
	}
	needArg := func() error {
		if !hasArg {
			return fmt.Errorf(":%s needs an argument", name)
		}
		return nil
	}
	switch name {
	case "has":
		if err := needArg(); err != nil {
			return nil, err
		}
		inner, err := Compile(arg)
		if err != nil {
			return nil, err
		}
		return func(el, _ *html.Node) bool { return has(el, inner) }, nil
	case "not":
		if err := needArg(); err != nil {
			return nil, err
		}
		inner, err := Compile(arg)
		if err != nil {
			return nil, err
		}
		return func(el, root *html.Node) bool { return !inner.matches(el, root) }, nil
	case "is", "matches-any":
		inner, err := Compile(arg)
		if err != nil {
			return nil, err
		}
		return func(el, root *html.Node) bool { return inner.matches(el, root) }, nil
	case "contains", "containsown", "containswholetext", "containswholeowntext", "containsdata":
		if err := needArg(); err != nil {
			return nil, err
		}
		search := strings.ToLower(unescape(arg))
		read := map[string]func(*html.Node) string{
			"contains": Text, "containsown": OwnText, "containsdata": Data,
			"containswholetext": wholeText, "containswholeowntext": wholeOwnText,
		}[name]
		return func(el, _ *html.Node) bool { return strings.Contains(strings.ToLower(read(el)), search) }, nil
	case "matches", "matchesown", "matcheswholetext", "matcheswholeowntext":
		if err := needArg(); err != nil {
			return nil, err
		}
		re, err := rx.Compile(arg)
		if err != nil {
			return nil, err
		}
		read := map[string]func(*html.Node) string{
			"matches": Text, "matchesown": OwnText, "matcheswholetext": wholeText, "matcheswholeowntext": wholeOwnText,
		}[name]
		return func(el, _ *html.Node) bool { return re.Find(read(el)) }, nil
	case "first-child":
		return nth(false, false, 0, 1), nil
	case "last-child":
		return nth(true, false, 0, 1), nil
	case "first-of-type":
		return nth(false, true, 0, 1), nil
	case "last-of-type":
		return nth(true, true, 0, 1), nil
	case "only-child":
		return func(el, _ *html.Node) bool {
			return inElementParent(el) && prevElement(el) == nil && nextElement(el) == nil
		}, nil
	case "only-of-type":
		first, last := nth(false, true, 0, 1), nth(true, true, 0, 1)
		return func(el, root *html.Node) bool { return first(el, root) && last(el, root) }, nil
	case "nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type":
		if err := needArg(); err != nil {
			return nil, err
		}
		a, b, err := parseNth(arg)
		if err != nil {
			return nil, err
		}
		return nth(strings.Contains(name, "last"), strings.HasSuffix(name, "of-type"), a, b), nil
	case "eq", "lt", "gt":
		if err := needArg(); err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return nil, err
		}
		return func(el, _ *html.Node) bool {
			i := siblingIndex(el)
			switch name {
			case "eq":
				return i == n
			case "lt":
				return i < n
			}
			return i > n
		}, nil
	case "root":
		return func(el, _ *html.Node) bool { return el.Parent != nil && el.Parent.Type == html.DocumentNode }, nil
	case "empty":
		return func(el, _ *html.Node) bool {
			for c := el.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode || c.Type == html.TextNode && strings.TrimSpace(c.Data) != "" {
					return false
				}
			}
			return true
		}, nil
	}
	return nil, fmt.Errorf("unknown pseudo-class :%s", name)
}

// has is jsoup's :has: a descendant that matches inner with el as the root, or, for "+ x" and "~ x", a following
// sibling.
func has(el *html.Node, inner *Selector) bool {
	for i := range inner.alternatives {
		alt := &inner.alternatives[i]
		if alt.relative == '+' || alt.relative == '~' {
			for s := nextElement(el); s != nil; s = nextElement(s) {
				if alt.matches(s, el) {
					return true
				}
				if alt.relative == '+' {
					break
				}
			}
			continue
		}
		var found bool
		var walk func(*html.Node)
		walk = func(n *html.Node) {
			for c := n.FirstChild; c != nil && !found; c = c.NextSibling {
				if c.Type == html.ElementNode {
					if alt.matches(c, el) {
						found = true
						return
					}
					walk(c)
				}
			}
		}
		walk(el)
		if found {
			return true
		}
	}
	return false
}

func inElementParent(el *html.Node) bool {
	return el.Parent != nil && el.Parent.Type == html.ElementNode
}

func siblingIndex(el *html.Node) int {
	i := 0
	for s := prevElement(el); s != nil; s = prevElement(s) {
		i++
	}
	return i
}

// nth is jsoup's CssNthEvaluator: the element's 1-based position among its element siblings (of its type, with
// ofType; counted from the end, with last) is a*n+b for some n >= 0. The root <html> never matches.
func nth(last, ofType bool, a, b int) matcher {
	return func(el, _ *html.Node) bool {
		if !inElementParent(el) {
			return false
		}
		pos := 1
		step := prevElement
		if last {
			step = nextElement
		}
		for s := step(el); s != nil; s = step(s) {
			if !ofType || s.Data == el.Data {
				pos++
			}
		}
		if a == 0 {
			return pos == b
		}
		return (pos-b)*a >= 0 && (pos-b)%a == 0
	}
}

func parseNth(arg string) (int, int, error) {
	s := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(arg), " ", ""))
	switch s {
	case "odd":
		return 2, 1, nil
	case "even":
		return 2, 0, nil
	}
	if !strings.Contains(s, "n") {
		b, err := strconv.Atoi(strings.TrimPrefix(s, "+"))
		return 0, b, err
	}
	parts := strings.SplitN(s, "n", 2)
	a := 1
	switch parts[0] {
	case "", "+":
	case "-":
		a = -1
	default:
		var err error
		if a, err = strconv.Atoi(strings.TrimPrefix(parts[0], "+")); err != nil {
			return 0, 0, err
		}
	}
	b := 0
	if parts[1] != "" {
		var err error
		if b, err = strconv.Atoi(strings.TrimPrefix(parts[1], "+")); err != nil {
			return 0, 0, err
		}
	}
	return a, b, nil
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func wholeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			} else if c.Type == html.ElementNode {
				walk(c)
			}
		}
	}
	walk(n)
	return b.String()
}

func wholeOwnText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	}
	return b.String()
}

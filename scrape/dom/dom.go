// Package dom reads parsed HTML the way jsoup (Ksoup in the news app) does: text, ownText, html, data and attr
// with jsoup's whitespace rules, and selection with jsoup's selector syntax and semantics (select.go). The site
// configs were written and tested against jsoup, so this package follows it, not the CSS specification, where the
// two differ.
package dom

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ParseString parses a page as jsoup's HTML parser does: with scripting off, so <noscript> holds markup, and with
// CDATA sections as text (jsoup's CDataNode), which RSS feeds rely on. The HTML parser never fails on a string.
func ParseString(s string) *html.Node {
	doc, err := html.ParseWithOptions(strings.NewReader(markCDATA(s)), html.ParseOptionEnableScripting(false))
	if err != nil {
		return &html.Node{Type: html.DocumentNode}
	}
	unmarkCDATA(doc)
	return doc
}

const cdataTag = "news-cdata"

// markCDATA rewrites each CDATA section the tokenizer meets in ordinary content (not inside scripts or titles) as a
// <news-cdata> element holding its escaped text. Go's parser, like the HTML specification, reads CDATA outside SVG
// and MathML as a comment that ends at the first ">"; jsoup reads it as text.
func markCDATA(s string) string {
	if !strings.Contains(s, "<![CDATA[") {
		return s
	}
	var b strings.Builder
	z := html.NewTokenizer(strings.NewReader(s))
	z.AllowCDATA(true)
	inRaw := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return b.String()
		}
		raw := string(z.Raw())
		wasRaw := inRaw
		inRaw = false
		if tt == html.StartTagToken {
			name, _ := z.TagName()
			switch string(name) {
			case "iframe", "noembed", "noframes", "noscript", "plaintext", "script", "style", "textarea", "title", "xmp":
				inRaw = true
			}
		}
		if tt == html.TextToken && !wasRaw && strings.HasPrefix(raw, "<![CDATA[") {
			inner := strings.TrimSuffix(strings.TrimPrefix(raw, "<![CDATA["), "]]>")
			b.WriteString("<" + cdataTag + ">" + html.EscapeString(inner) + "</" + cdataTag + ">")
		} else {
			b.WriteString(raw)
		}
	}
}

// unmarkCDATA turns each <news-cdata> element back into a text node, marked as CDATA in its Namespace field (unused
// on text nodes).
func unmarkCDATA(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && c.Data == cdataTag {
			text := &html.Node{Type: html.TextNode, Data: wholeText(c), Namespace: cdataTag}
			n.InsertBefore(text, c)
			n.RemoveChild(c)
		} else {
			unmarkCDATA(c)
		}
		c = next
	}
}

func isCDATA(n *html.Node) bool { return n.Type == html.TextNode && n.Namespace == cdataTag }

// IsElement reports whether n is an element.
func IsElement(n *html.Node) bool { return n != nil && n.Type == html.ElementNode }

// Parent is n's parent element or document, or nil.
func Parent(n *html.Node) *html.Node { return n.Parent }

// Attr is the value of attribute key (case-insensitive), "" when missing.
func Attr(n *html.Node, key string) string {
	v, _ := attr(n, key)
	return v
}

// HasAttr reports whether n has attribute key (case-insensitive).
func HasAttr(n *html.Node, key string) bool {
	_, ok := attr(n, key)
	return ok
}

func attr(n *html.Node, key string) (string, bool) {
	if n.Type != html.ElementNode {
		return "", false
	}
	for _, a := range n.Attr {
		name := a.Key
		if a.Namespace != "" {
			name = a.Namespace + ":" + a.Key
		}
		if strings.EqualFold(name, key) {
			return a.Val, true
		}
	}
	return "", false
}

// rawText reports whether n's text children are data to jsoup (DataNode), not text.
func rawText(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Xmp, atom.Iframe, atom.Noembed, atom.Noframes:
		return true
	}
	return false
}

// blockTags are the tags jsoup's Tag marks as block: text() puts a space around them.
var blockTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`html head body frameset script noscript style meta link title frame noframes
		section nav aside hgroup header footer p h1 h2 h3 h4 h5 h6 ul ol pre div blockquote hr address figure
		figcaption form fieldset ins del dl dt dd li table caption thead tfoot tbody colgroup col tr th td video
		audio canvas details menu plaintext template article main svg math center dir applet marquee listing`) {
		blockTags[t] = true
	}
}

func isBlock(n *html.Node) bool { return n.Type == html.ElementNode && blockTags[n.Data] }

// preserveWhitespace reports whether text in n keeps its whitespace (inside pre, textarea, title, plaintext), looking
// up to 6 ancestors as jsoup does.
func preserveWhitespace(n *html.Node) bool {
	for i := 0; n != nil && i < 6; i, n = i+1, n.Parent {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "pre", "textarea", "title", "plaintext":
				return true
			}
		}
	}
	return false
}

func lastIsSpace(b *strings.Builder) bool {
	s := b.String()
	return len(s) > 0 && s[len(s)-1] == ' '
}

func actuallyWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r' || r == 160
}

// appendNormalised is jsoup's StringUtil.appendNormalisedWhitespace: runs of whitespace become one space,
// zero-width spaces and soft hyphens are dropped.
func appendNormalised(b *strings.Builder, s string, stripLeading bool) {
	lastWasWhite, reachedNonWhite := false, false
	for _, r := range s {
		if actuallyWhitespace(r) {
			if (stripLeading && !reachedNonWhite) || lastWasWhite {
				continue
			}
			b.WriteByte(' ')
			lastWasWhite = true
		} else if r != 8203 && r != 173 {
			b.WriteRune(r)
			lastWasWhite = false
			reachedNonWhite = true
		}
	}
}

func appendTextNode(b *strings.Builder, n *html.Node) {
	if isCDATA(n) || preserveWhitespace(n.Parent) {
		b.WriteString(n.Data)
	} else {
		appendNormalised(b, n.Data, lastIsSpace(b))
	}
}

// javaTrim is Java's String.trim: it removes every char up to and including ' '.
func javaTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
}

// Text is jsoup's Element.text(): the text of n and its descendants, whitespace normalised, with a space at block
// boundaries and for <br>.
func Text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			if !rawText(node.Parent) {
				appendTextNode(&b, node)
			}
			return
		case html.ElementNode:
			if b.Len() > 0 && (isBlock(node) || node.Data == "br") && !lastIsSpace(&b) {
				b.WriteByte(' ')
			}
		case html.DocumentNode:
		default:
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if node.Type == html.ElementNode && isBlock(node) && node.NextSibling != nil &&
			node.NextSibling.Type == html.TextNode && !lastIsSpace(&b) {
			b.WriteByte(' ')
		}
	}
	walk(n)
	return javaTrim(b.String())
}

// OwnText is jsoup's Element.ownText(): only n's own text children, and a space for each <br> child.
func OwnText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode && !rawText(n) {
			appendTextNode(&b, c)
		} else if c.Type == html.ElementNode && c.Data == "br" && !lastIsSpace(&b) {
			b.WriteByte(' ')
		}
	}
	return javaTrim(b.String())
}

// Data is jsoup's Element.data(): the content of scripts and styles (and comments) in n, without markup.
func Data(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			if rawText(n) || isCDATA(c) {
				b.WriteString(c.Data)
			}
		case html.CommentNode:
			b.WriteString(c.Data)
		case html.ElementNode:
			b.WriteString(Data(c))
		}
	}
	return b.String()
}

// HTML is jsoup's Element.html(): n's inner HTML, trimmed. Unlike jsoup it does not pretty-print, so whitespace
// between tags is as in the page; the configs only search it with regexes.
func HTML(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeOuter(&b, c)
	}
	return strings.TrimSpace(b.String())
}

var voidElements = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "keygen": true, "link": true, "meta": true, "param": true, "source": true,
	"track": true, "wbr": true, "basefont": true, "bgsound": true, "frame": true}

func writeOuter(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if isCDATA(n) {
			b.WriteString("<![CDATA[" + n.Data + "]]>")
		} else if rawText(n.Parent) {
			b.WriteString(n.Data)
		} else {
			escape(b, n.Data, false)
		}
	case html.CommentNode:
		b.WriteString("<!--" + n.Data + "-->")
	case html.DoctypeNode:
		b.WriteString("<!doctype " + n.Data + ">")
	case html.ElementNode:
		b.WriteString("<" + n.Data)
		for _, a := range n.Attr {
			b.WriteByte(' ')
			if a.Namespace != "" {
				b.WriteString(a.Namespace + ":")
			}
			b.WriteString(a.Key)
			b.WriteString(`="`)
			escape(b, a.Val, true)
			b.WriteByte('"')
		}
		b.WriteByte('>')
		if voidElements[n.Data] {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeOuter(b, c)
		}
		b.WriteString("</" + n.Data + ">")
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeOuter(b, c)
		}
	}
}

// escape is jsoup's base escaping: &, <, > and no-break spaces in text; &, " and no-break spaces in attributes.
func escape(b *strings.Builder, s string, inAttr bool) {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == 0xA0:
			b.WriteString("&nbsp;")
		case r == '<' && !inAttr:
			b.WriteString("&lt;")
		case r == '>' && !inAttr:
			b.WriteString("&gt;")
		case r == '"' && inAttr:
			b.WriteString("&quot;")
		default:
			b.WriteString(s[:size])
		}
		s = s[size:]
	}
}

// Remove takes n out of its tree.
func Remove(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

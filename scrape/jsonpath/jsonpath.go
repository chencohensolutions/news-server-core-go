// Package jsonpath is the JSONPath subset of the site configs ($, .key, ..key, .*, [n], [*]; the news repo's
// docs/source-config.md), over JSON parsed with its key order kept, as kotlinx.serialization keeps it: ".*" and
// "..key" return matches in document order.
package jsonpath

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Kind is the type of a JSON value.
type Kind int

const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Value is a parsed JSON value. Numbers and booleans keep their literal text, as kotlinx's JsonPrimitive.content.
type Value struct {
	Kind    Kind
	Literal string // the string's content, or the number's or boolean's text
	Items   []*Value
	Keys    []string
	Members map[string]*Value
}

// Parse parses JSON text strictly; trailing content is an error.
func Parse(text string) (*Value, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing content")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (*Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return fromToken(dec, tok)
}

func fromToken(dec *json.Decoder, tok json.Token) (*Value, error) {
	switch t := tok.(type) {
	case nil:
		return &Value{Kind: Null}, nil
	case bool:
		return &Value{Kind: Bool, Literal: strconv.FormatBool(t)}, nil
	case json.Number:
		return &Value{Kind: Number, Literal: string(t)}, nil
	case string:
		return &Value{Kind: String, Literal: t}, nil
	case json.Delim:
		switch t {
		case '[':
			v := &Value{Kind: Array}
			for dec.More() {
				item, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				v.Items = append(v.Items, item)
			}
			_, err := dec.Token()
			return v, err
		case '{':
			v := &Value{Kind: Object, Members: map[string]*Value{}}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyTok.(string)
				item, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				if _, seen := v.Members[key]; !seen {
					v.Keys = append(v.Keys, key)
				}
				v.Members[key] = item
			}
			_, err := dec.Token()
			return v, err
		}
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// Content is the value as a config field reads it: a string as is, a number or boolean as its text; ok is false for
// null, arrays and objects.
func (v *Value) Content() (string, bool) {
	switch v.Kind {
	case String, Number, Bool:
		return v.Literal, true
	}
	return "", false
}

func (v *Value) children() []*Value {
	switch v.Kind {
	case Array:
		return v.Items
	case Object:
		out := make([]*Value, len(v.Keys))
		for i, k := range v.Keys {
			out[i] = v.Members[k]
		}
		return out
	}
	return nil
}

// decodeIfString parses a string as JSON before a step, since sites embed JSON as strings in their page data.
func (v *Value) decodeIfString() *Value {
	if v.Kind == String {
		if parsed, err := Parse(v.Literal); err == nil {
			return parsed
		}
	}
	return v
}

type stepKind int

const (
	key stepKind = iota
	index
	children
	descendants
)

type step struct {
	kind  stepKind
	name  string
	index int
}

// Path is a parsed path.
type Path struct{ steps []step }

var keyPattern = regexp.MustCompile(`^[^.\[\]]+`)

// Compile parses a path, as the Kotlin JsonPath.parse.
func Compile(path string) (*Path, error) {
	if !strings.HasPrefix(path, "$") {
		return nil, fmt.Errorf("JSON path must start with '$': '%s'", path)
	}
	var steps []step
	i := 1
	readKey := func() (string, error) {
		m := keyPattern.FindString(path[i:])
		if m == "" {
			return "", fmt.Errorf("expected a key at %d in '%s'", i+1, path)
		}
		i += len(m)
		return m, nil
	}
	for i < len(path) {
		switch {
		case strings.HasPrefix(path[i:], ".."):
			i += 2
			k, err := readKey()
			if err != nil {
				return nil, err
			}
			steps = append(steps, step{kind: descendants, name: k})
		case strings.HasPrefix(path[i:], ".*"):
			i += 2
			steps = append(steps, step{kind: children})
		case strings.HasPrefix(path[i:], "[*]"):
			i += 3
			steps = append(steps, step{kind: children})
		case path[i] == '.':
			i++
			k, err := readKey()
			if err != nil {
				return nil, err
			}
			steps = append(steps, step{kind: key, name: k})
		case path[i] == '[':
			end := strings.IndexByte(path[i:], ']')
			n := -1
			if end >= 0 {
				if v, err := strconv.Atoi(path[i+1 : i+end]); err == nil {
					n = v
				}
			}
			if n < 0 {
				return nil, fmt.Errorf("expected [n] or [*] at %d in '%s'", i+1, path)
			}
			steps = append(steps, step{kind: index, index: n})
			i += end + 1
		default:
			return nil, fmt.Errorf("unexpected '%c' at %d in '%s'", path[i], i+1, path)
		}
	}
	return &Path{steps: steps}, nil
}

// Select is every match, in document order.
func (p *Path) Select(root *Value) []*Value {
	nodes := []*Value{root}
	for _, s := range p.steps {
		var next []*Value
		for _, n := range nodes {
			next = append(next, s.apply(n.decodeIfString())...)
		}
		nodes = next
	}
	return nodes
}

func (s step) apply(n *Value) []*Value {
	switch s.kind {
	case key:
		if n.Kind == Object {
			if v, ok := n.Members[s.name]; ok {
				return []*Value{v}
			}
		}
	case index:
		if n.Kind == Array && s.index < len(n.Items) {
			return []*Value{n.Items[s.index]}
		}
	case children:
		return n.children()
	case descendants:
		var out []*Value
		if n.Kind == Object {
			if v, ok := n.Members[s.name]; ok {
				out = append(out, v)
			}
		}
		for _, c := range n.children() {
			out = append(out, s.apply(c)...)
		}
		return out
	}
	return nil
}

// compact is used by tests to print a value.
func (v *Value) compact() string {
	var b bytes.Buffer
	switch v.Kind {
	case Null:
		b.WriteString("null")
	case String:
		s, _ := json.Marshal(v.Literal)
		b.Write(s)
	case Number, Bool:
		b.WriteString(v.Literal)
	case Array:
		b.WriteByte('[')
		for i, c := range v.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(c.compact())
		}
		b.WriteByte(']')
	case Object:
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			s, _ := json.Marshal(k)
			b.Write(s)
			b.WriteByte(':')
			b.WriteString(v.Members[k].compact())
		}
		b.WriteByte('}')
	}
	return b.String()
}

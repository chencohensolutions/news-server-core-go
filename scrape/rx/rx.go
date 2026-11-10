// Package rx runs the configs' regexes with Java's semantics as far as the news app relies on them: backtracking,
// lazy bounded repeats over 1,000 ({0,3000}?), and ASCII \s \d \w. Go's regexp (RE2) refuses repeats over 1,000,
// so this uses regexp2, in ECMAScript mode for the ASCII classes.
package rx

import (
	"sync"
	"time"

	"github.com/dlclark/regexp2"
)

// Regex is a compiled pattern.
type Regex struct{ re *regexp2.Regexp }

var cache sync.Map // pattern -> *Regex

// Compile compiles pattern, cached: the same config patterns run on every page.
func Compile(pattern string) (*Regex, error) {
	if r, ok := cache.Load(pattern); ok {
		return r.(*Regex), nil
	}
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		// ECMAScript mode refuses a few constructs Java has (lookbehind, possessive); fall back to .NET syntax.
		if re, err = regexp2.Compile(pattern, regexp2.None); err != nil {
			return nil, err
		}
	}
	// A page is at most a few MB; a pathological pattern must not hang the server.
	re.MatchTimeout = 5 * time.Second
	r := &Regex{re}
	cache.Store(pattern, r)
	return r, nil
}

// MustCompile is Compile for patterns known to be valid.
func MustCompile(pattern string) *Regex {
	r, err := Compile(pattern)
	if err != nil {
		panic(err)
	}
	return r
}

// Find is Kotlin's Regex.containsMatchIn.
func (r *Regex) Find(s string) bool {
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}

// Group is Kotlin's Regex.find(s)?.groupValues?.getOrNull(group): capture group of the first match ("" when the
// group took no part), ok false when nothing matches or the group doesn't exist.
func (r *Regex) Group(s string, group int) (string, bool) {
	m, err := r.re.FindStringMatch(s)
	if err != nil || m == nil {
		return "", false
	}
	groups := m.Groups()
	if group < 0 || group >= len(groups) {
		return "", false
	}
	return groups[group].String(), true
}

// GroupValues is every group of the first match, ok false when nothing matches.
func (r *Regex) GroupValues(s string) ([]string, bool) {
	m, err := r.re.FindStringMatch(s)
	if err != nil || m == nil {
		return nil, false
	}
	var out []string
	for _, g := range m.Groups() {
		out = append(out, g.String())
	}
	return out, true
}

// Replace is Kotlin's Regex.replace(s, with): every match replaced, $1 naming a group.
func (r *Regex) Replace(s, with string) string {
	out, err := r.re.Replace(s, with, -1, -1)
	if err != nil {
		return s
	}
	return out
}

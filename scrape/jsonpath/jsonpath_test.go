package jsonpath

import (
	"strings"
	"testing"
)

// The Kotlin JsonPathTest.

func selectContent(t *testing.T, path, json string) string {
	t.Helper()
	p, err := Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := Parse(json)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range p.Select(root) {
		content, _ := v.Content()
		out = append(out, content)
	}
	return strings.Join(out, ",")
}

func TestKeysAndIndexes(t *testing.T) {
	for _, c := range []struct{ path, json, want string }{
		{"$.a[1].x", `{"a":[{"x":"a"},{"x":"b"}]}`, "b"},
		{"$.sizes.ilh_3:2_lg", `{"sizes":{"ilh_3:2_lg":"v"}}`, "v"},
		{"$.a[5]", `{"a":[1]}`, ""},
	} {
		if got := selectContent(t, c.path, c.json); got != c.want {
			t.Errorf("%s = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestWildcardsVisitEveryChild(t *testing.T) {
	if got := selectContent(t, "$.a[*]", `{"a":[1,2]}`); got != "1,2" {
		t.Error(got)
	}
	if got := selectContent(t, "$.o.*", `{"o":{"x":1,"y":2}}`); got != "1,2" {
		t.Error(got)
	}
}

func TestRecursiveDescentIsPreOrder(t *testing.T) {
	json := `{"post":"p1","list":[{"post":"p2","inner":{"post":"p3"}},{"post":"p4"}]}`
	if got := selectContent(t, "$..post", json); got != "p1,p2,p3,p4" {
		t.Error(got)
	}
}

func TestEmbeddedJSONStringsAreParsedMidPath(t *testing.T) {
	json := `{"data":"{\"items\":[{\"id\":7}]}"}`
	if got := selectContent(t, "$.data..id", json); got != "7" {
		t.Error(got)
	}
	if got := selectContent(t, "$.data", json); got != `{"items":[{"id":7}]}` {
		t.Error(got)
	}
}

func TestInvalidPathsAreRejected(t *testing.T) {
	for _, path := range []string{"a.b", "$.", "$[x]", "$..", "$.a["} {
		if _, err := Compile(path); err == nil {
			t.Errorf("%s compiled", path)
		}
	}
}

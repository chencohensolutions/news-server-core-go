package fetch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheServerNamesItselfWhereTheSiteConfigSetNoUserAgent(t *testing.T) {
	var got []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = append(got, r.UserAgent()) }))
	defer site.Close()
	client, err := NewClient(Options{EngineDefault: "placeholder"})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"placeholder", "", "own/1.0"} {
		req, _ := http.NewRequest("GET", site.URL, nil)
		req.Header.Set("User-Agent", agent)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// An empty User-Agent header makes Go send its own; the client sets the server's before that happens.
	if got[0] != UserAgent || got[1] != UserAgent || got[2] != "own/1.0" {
		t.Fatalf("user agents %q", got)
	}
}

// A pack transport wraps the core's, sees the options (sites and its own settings) and passes on what it doesn't
// handle.
func TestPackTransportsWrapTheCoresInNameOrder(t *testing.T) {
	defer func(saved map[string]Wrap) { registry = saved }(registry)
	registry = map[string]Wrap{}
	var order []string
	wrap := func(name string) Wrap {
		return func(base http.RoundTripper, o Options) (http.RoundTripper, error) {
			if name == "a" && (string(o.Packs["a"]) != `{"x":1}` || o.Sites[0].Host != "example.com") {
				t.Errorf("options %+v", o)
			}
			return roundTripFunc(func(r *http.Request) (*http.Response, error) {
				order = append(order, name)
				return base.RoundTrip(r)
			}), nil
		}
	}
	RegisterTransport("b", wrap("b"))
	RegisterTransport("a", wrap("a"))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	client, err := NewClient(Options{Sites: []Site{{ID: "ex", Host: "example.com"}}, Packs: map[string]json.RawMessage{"a": json.RawMessage(`{"x":1}`)}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// b wraps a, which wraps the core: b sees the request first.
	if len(order) != 2 || order[0] != "b" || order[1] != "a" {
		t.Fatalf("order %v", order)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Package fetch is the HTTP client the engine reads the sites with. On its own it is a plain client that names
// itself honestly. Packs (Go modules built in with newsbuild, see cmd/newsbuild) can change how the server reaches
// the sites by registering a transport with RegisterTransport from their init function.
package fetch

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// UserAgent is what the server sends when a site config sets no user agent of its own: it names the server and
// where to read about it, so a site can tell who is asking.
const UserAgent = "news-server-go (+https://github.com/chencohensolutions/news-server-core-go)"

// Site is one site the engine reads, as a transport sees it.
type Site struct {
	ID string
	// Host is the site's host without "www.".
	Host string
	// Extra holds the keys of the site config's "http" object that the core doesn't know, for packs to read.
	Extra map[string]json.RawMessage
}

// Options are what a transport is built from.
type Options struct {
	// EngineDefault is the scrape engine's placeholder user agent, which the client replaces with UserAgent.
	EngineDefault string
	// Sites are the sites the engine reads.
	Sites []Site
	// Packs holds each pack's own settings, the "packs" object of config.json, by pack name.
	Packs map[string]json.RawMessage
}

// Wrap builds a pack's transport. base is the transport so far (the core's, or the previous pack's); a pack
// handles the requests it cares about and passes the rest to base.
type Wrap func(base http.RoundTripper, o Options) (http.RoundTripper, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Wrap{}
)

// RegisterTransport adds a pack's transport. Call it from the pack's init function; name is the pack's name, the
// key of its settings in config.json's "packs". Packs wrap the core's transport in the order of their names.
func RegisterTransport(name string, wrap Wrap) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, taken := registry[name]; taken {
		panic(fmt.Sprintf("fetch: two packs registered the transport %q", name))
	}
	registry[name] = wrap
}

// Transports are the names of the registered pack transports, sorted.
func Transports() []string {
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Headers sets the server's user agent on requests whose site config set none (the engine's placeholder).
type Headers struct {
	Base          http.RoundTripper
	EngineDefault string
}

func (h *Headers) RoundTrip(req *http.Request) (*http.Response, error) {
	agent := req.Header.Get("User-Agent")
	if agent != "" && agent != h.EngineDefault {
		return h.Base.RoundTrip(req)
	}
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", UserAgent)
	return h.Base.RoundTrip(r)
}

// NewClient is the HTTP client the engine fetches with: Go's standard transport with the server's user agent,
// wrapped by every registered pack transport.
func NewClient(o Options) (*http.Client, error) {
	var transport http.RoundTripper = &Headers{Base: http.DefaultTransport.(*http.Transport).Clone(), EngineDefault: o.EngineDefault}
	registryMu.Lock()
	defer registryMu.Unlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		wrapped, err := registry[name](transport, o)
		if err != nil {
			return nil, fmt.Errorf("pack %s: %w", name, err)
		}
		transport = wrapped
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

// HostOf is a base URL's host without "www.", "" when it has none.
func HostOf(baseURL string) string {
	rest := baseURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.Contains(rest[i:], "]") {
		rest = rest[:i]
	}
	return strings.TrimPrefix(rest, "www.")
}

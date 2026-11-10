// Package fetch is the HTTP client the engine reads the sites with: a plain client that names itself.
package fetch

import (
	"net/http"
	"strings"
	"time"
)

// UserAgent is what the server sends when a site config sets no user agent of its own.
const UserAgent = "news-server-go (+https://github.com/chencohensolutions/news-server-core-go)"

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

// Options are how NewClient reaches the sites.
type Options struct {
	// EngineDefault is the scrape engine's placeholder user agent, which the client replaces with UserAgent.
	EngineDefault string
}

// NewClient is the HTTP client the engine fetches with.
func NewClient(o Options) *http.Client {
	return &http.Client{Transport: &Headers{Base: http.DefaultTransport, EngineDefault: o.EngineDefault}, Timeout: 30 * time.Second}
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

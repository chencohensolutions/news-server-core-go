// Package video turns an article's embedded video player into a stream the server can play. The core knows no
// player: packs register resolvers from their init functions, and a video no resolver knows stays its embed, which
// the app plays in a web view.
package video

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

// Stream is a video the server can read directly: a progressive file (MP4) or an HLS playlist, with any headers the
// requests for it need (a Referer, say).
type Stream struct {
	URL    string
	HLS    bool
	Header http.Header
}

// Resolver finds the stream behind embed, an embedded player's address, using client (the server's fetch client,
// with the packs' transports). It returns nil and no error for a player it doesn't know, without a request.
type Resolver func(ctx context.Context, client *http.Client, embed string) (*Stream, error)

var (
	mu        sync.Mutex
	resolvers = map[string]Resolver{}
)

// Register adds a pack's resolver under name (pack and player, say "xx/player"). Call it from an init function.
func Register(name string, r Resolver) {
	mu.Lock()
	defer mu.Unlock()
	if _, taken := resolvers[name]; taken {
		panic(fmt.Sprintf("video: two packs registered the resolver %q", name))
	}
	resolvers[name] = r
}

// Names are the registered resolvers, sorted.
func Names() []string {
	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(resolvers))
	for name := range resolvers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Resolve asks every resolver, in the order of their names, for embed's stream. It returns nil and no error when
// none knows the player; an error only when every resolver that knew it failed.
func Resolve(ctx context.Context, client *http.Client, embed string) (*Stream, error) {
	var firstErr error
	for _, name := range Names() {
		mu.Lock()
		r := resolvers[name]
		mu.Unlock()
		stream, err := r(ctx, client, embed)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", name, err)
			}
			continue
		}
		if stream != nil {
			return stream, nil
		}
	}
	return nil, firstErr
}

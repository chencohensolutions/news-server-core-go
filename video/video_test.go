package video

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestTheFirstResolverThatKnowsThePlayerWins(t *testing.T) {
	defer func(saved map[string]Resolver) { resolvers = saved }(resolvers)
	resolvers = map[string]Resolver{}
	Register("b/known", func(ctx context.Context, c *http.Client, embed string) (*Stream, error) {
		return &Stream{URL: "https://cdn/" + embed + ".m3u8", HLS: true}, nil
	})
	Register("a/broken", func(ctx context.Context, c *http.Client, embed string) (*Stream, error) {
		return nil, errors.New("site changed")
	})
	stream, err := Resolve(context.Background(), nil, "x")
	if err != nil || stream == nil || stream.URL != "https://cdn/x.m3u8" {
		t.Fatalf("%+v %v", stream, err)
	}
}

func TestNoResolverLeavesTheEmbed(t *testing.T) {
	defer func(saved map[string]Resolver) { resolvers = saved }(resolvers)
	resolvers = map[string]Resolver{}
	Register("a/other", func(ctx context.Context, c *http.Client, embed string) (*Stream, error) { return nil, nil })
	if stream, err := Resolve(context.Background(), nil, "x"); stream != nil || err != nil {
		t.Fatalf("%+v %v", stream, err)
	}
}

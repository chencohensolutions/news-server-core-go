package newsserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/video"
)

// The app opens an article's video with videoStream and plays it from the server, with its certificate only.
func TestAVideoPlaysThroughTheServer(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Time{}, strings.NewReader("MP4BYTES"))
	}))
	defer cdn.Close()
	env := newTestEnv(t)
	env.api.Videos = &video.Relay{Client: cdn.Client(), Resolve: func(ctx context.Context, c *http.Client, embed string) (*video.Stream, error) {
		if embed == "https://player.example/1" {
			return &video.Stream{URL: cdn.URL + "/v.mp4"}, nil
		}
		return nil, nil
	}}
	ctx := context.Background()
	for _, a := range []*scrape.Article{
		{SourceID: "siteh", URL: "https://www.siteh.example/v", Title: "With video", Video: &scrape.ArticleVideo{Embed: "https://player.example/1"}},
		{SourceID: "siteh", URL: "https://www.siteh.example/u", Title: "Unknown player", Video: &scrape.ArticleVideo{Embed: "https://other.example/2"}},
	} {
		if err := env.db.SaveArticle(ctx, a, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	cert, _ := env.enrollDevice(t, "dana")
	_, resp := env.query(t, cert, `query($url: String!) { videoStream(url: $url) { path hls } }`, map[string]any{"url": "https://www.siteh.example/v"})
	var stream struct {
		Path string
		HLS  bool
	}
	json.Unmarshal(resp.Data["videoStream"], &stream)
	if len(resp.Errors) > 0 || stream.Path == "" || stream.HLS {
		t.Fatalf("videoStream: %+v %s", resp.Errors, resp.Data["videoStream"])
	}
	_, resp = env.query(t, cert, `{ videoStream(url: "https://www.siteh.example/u") { path } }`, nil)
	if string(resp.Data["videoStream"]) != "null" {
		t.Fatalf("an unknown player: %s", resp.Data["videoStream"])
	}

	got, err := env.client(cert).Get(env.server.URL + stream.Path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if string(body) != "MP4BYTES" {
		t.Fatalf("%d %q", got.StatusCode, body)
	}
	if got, err := env.client(nil).Get(env.server.URL + stream.Path); err != nil || got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a certificate: %v %v", got, err)
	}
}

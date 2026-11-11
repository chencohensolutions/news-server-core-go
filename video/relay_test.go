package video

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// upstream is a video's host: an HLS master playlist, a variant playlist with a key, segments, and an MP4 that
// answers Range requests; every request must carry the stream's Referer.
func upstream(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nhi/index.m3u8\n")
	})
	mux.HandleFunc("/v/hi/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		io.WriteString(w, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"/keys/k1\"\n#EXTINF:6,\nseg1.ts\n#EXTINF:6,\nhttps://elsewhere.invalid/seg2.ts\n")
	})
	mux.HandleFunc("/v/hi/seg1.ts", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "SEGMENT1") })
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "clip.mp4", fixedTime, strings.NewReader("0123456789"))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://player.example/" {
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func relayFor(t *testing.T, up *httptest.Server) (*Relay, *httptest.Server) {
	header := http.Header{"Referer": {"https://player.example/"}}
	r := &Relay{Client: up.Client(), Resolve: func(ctx context.Context, c *http.Client, embed string) (*Stream, error) {
		switch embed {
		case "https://player.example/hls":
			return &Stream{URL: up.URL + "/v/master.m3u8", HLS: true, Header: header}, nil
		case "https://player.example/mp4":
			return &Stream{URL: up.URL + "/clip.mp4", Header: header}, nil
		}
		return nil, nil
	}}
	front := httptest.NewServer(r)
	t.Cleanup(front.Close)
	return r, front
}

func get(t *testing.T, address string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, address, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestAnHLSVideoComesThroughTheRelay(t *testing.T) {
	up := upstream(t)
	r, front := relayFor(t, up)
	opened, err := r.Open(context.Background(), "", "https://player.example/hls")
	if err != nil || !opened.HLS || !strings.HasSuffix(opened.Path, "/index.m3u8") {
		t.Fatalf("%+v %v", opened, err)
	}
	_, master := get(t, front.URL+opened.Path, nil)
	lines := strings.Split(strings.TrimSpace(master), "\n")
	variant := lines[len(lines)-1]
	if !strings.HasPrefix(variant, Prefix) || strings.Contains(master, up.URL+"/v/hi/index.m3u8\n") {
		t.Fatalf("the master playlist isn't rewritten:\n%s", master)
	}
	resp, playlist := get(t, front.URL+variant, nil)
	if resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("variant: %d %s\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), playlist)
	}
	if !strings.Contains(playlist, `URI="/video/`) || strings.Contains(playlist, "\nseg1.ts\n") {
		t.Fatalf("the variant isn't rewritten:\n%s", playlist)
	}
	if want := url.QueryEscape(up.URL + "/keys/k1"); !strings.Contains(playlist, want) {
		t.Fatalf("the key isn't resolved against the playlist:\n%s", playlist)
	}
	var segment string
	for _, line := range strings.Split(playlist, "\n") {
		if strings.HasPrefix(line, Prefix) && strings.Contains(line, "seg1") {
			segment = line
		}
	}
	if _, body := get(t, front.URL+segment, nil); body != "SEGMENT1" {
		t.Fatalf("segment: %q", body)
	}
	// An address the playlists didn't name isn't read.
	forged := strings.Replace(segment, url.QueryEscape("seg1.ts"), url.QueryEscape("other.ts"), 1)
	if resp, _ := get(t, front.URL+forged, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a forged part got %d", resp.StatusCode)
	}
}

func TestAnMP4SeeksThroughTheRelay(t *testing.T) {
	up := upstream(t)
	r, front := relayFor(t, up)
	opened, err := r.Open(context.Background(), "", "https://player.example/mp4")
	if err != nil || opened.HLS {
		t.Fatalf("%+v %v", opened, err)
	}
	resp, body := get(t, front.URL+opened.Path, http.Header{"Range": {"bytes=2-5"}})
	if resp.StatusCode != http.StatusPartialContent || body != "2345" || resp.Header.Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("%d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
}

func TestAVideoNoResolverKnowsIsNotOpened(t *testing.T) {
	up := upstream(t)
	r, front := relayFor(t, up)
	if _, err := r.Open(context.Background(), "", "https://unknown.example/embed"); err != ErrNoStream {
		t.Fatalf("%v", err)
	}
	if resp, _ := get(t, front.URL+Prefix+"0123/file", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an unknown token got %d", resp.StatusCode)
	}
}

var fixedTime = mustTime("2026-11-11T00:00:00Z")

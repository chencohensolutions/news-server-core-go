package video

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Relay streams videos to the app through the server: the app never talks to a video's site or player, so it needs
// no site's handshake, cookies or player code. A video is opened once (Open, which resolves its player), and then
// read under /video/<token>/: an HLS playlist with every address in it rewritten to come back through the relay, or
// an MP4 file with Range requests passed on, so the app can seek.
type Relay struct {
	// Client reads the videos: the server's fetch client, with the packs' transports.
	Client *http.Client
	// Resolve finds the stream behind an embedded player; nil uses the registered resolvers (Resolve).
	Resolve func(ctx context.Context, client *http.Client, embed string) (*Stream, error)
	// TTL is how long an opened video can be read; 0 is DefaultTTL.
	TTL time.Duration
	Now func() time.Time

	once    sync.Once
	key     []byte
	mu      sync.Mutex
	streams map[string]opened
}

// DefaultTTL is how long an opened video stays readable: past it, the app opens it again.
const DefaultTTL = 6 * time.Hour

// Prefix is the path the relay serves under.
const Prefix = "/video/"

type opened struct {
	stream  Stream
	expires time.Time
}

// Opened is an opened video: Path, under Prefix, is what the app plays (index.m3u8 for HLS, file for an MP4).
type Opened struct {
	Path string
	HLS  bool
}

// ErrNoStream is a video no resolver knows: the app plays its embed in a web view instead.
var ErrNoStream = errors.New("no stream for this video")

func (r *Relay) init() {
	r.once.Do(func() {
		r.key = make([]byte, 32)
		rand.Read(r.key)
		r.streams = map[string]opened{}
		if r.Now == nil {
			r.Now = time.Now
		}
		if r.TTL == 0 {
			r.TTL = DefaultTTL
		}
		if r.Resolve == nil {
			r.Resolve = Resolve
		}
		if r.Client == nil {
			r.Client = http.DefaultClient
		}
	})
}

// Open makes a video readable through the relay: stream when the article's page gave one, else the stream a
// resolver finds behind embed. ErrNoStream when there is neither.
func (r *Relay) Open(ctx context.Context, stream, embed string) (Opened, error) {
	r.init()
	var s *Stream
	switch {
	case stream != "":
		s = &Stream{URL: stream, HLS: looksHLS(stream)}
	case embed != "":
		found, err := r.Resolve(ctx, r.Client, embed)
		if err != nil {
			return Opened{}, err
		}
		s = found
	}
	if s == nil || s.URL == "" {
		return Opened{}, ErrNoStream
	}
	raw := make([]byte, 16)
	rand.Read(raw)
	token := hex.EncodeToString(raw)
	now := r.Now()
	r.mu.Lock()
	for t, o := range r.streams {
		if now.After(o.expires) {
			delete(r.streams, t)
		}
	}
	r.streams[token] = opened{stream: *s, expires: now.Add(r.TTL)}
	r.mu.Unlock()
	if s.HLS {
		return Opened{Path: Prefix + token + "/index.m3u8", HLS: true}, nil
	}
	return Opened{Path: Prefix + token + "/file", HLS: false}, nil
}

// ServeHTTP serves /video/<token>/index.m3u8, /video/<token>/file and /video/<token>/p/<signature>?u=<address>
// (a part of an HLS video: a playlist, a segment or a key).
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.init()
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(req.URL.Path, Prefix)
	token, what, _ := strings.Cut(rest, "/")
	r.mu.Lock()
	o, ok := r.streams[token]
	r.mu.Unlock()
	if !ok || r.Now().After(o.expires) {
		http.Error(w, "no such video: open it again", http.StatusNotFound)
		return
	}
	switch {
	case what == "index.m3u8" && o.stream.HLS:
		r.relay(w, req, token, o.stream, o.stream.URL, true)
	case what == "file" && !o.stream.HLS:
		r.relay(w, req, token, o.stream, o.stream.URL, false)
	case strings.HasPrefix(what, "p/"):
		target := req.URL.Query().Get("u")
		if !hmac.Equal([]byte(strings.TrimPrefix(what, "p/")), []byte(r.sign(token, target))) {
			http.Error(w, "bad signature", http.StatusForbidden)
			return
		}
		r.relay(w, req, token, o.stream, target, false)
	default:
		http.NotFound(w, req)
	}
}

// maxPlaylist bounds a playlist read into memory to be rewritten.
const maxPlaylist = 4 << 20

// relay reads target (a playlist when playlist is set, or when its type or name says so) with the stream's headers (and the app's Range) and passes it on; a playlist comes back with
// its addresses pointing at the relay.
func (r *Relay) relay(w http.ResponseWriter, req *http.Request, token string, s Stream, target string, playlist bool) {
	up, err := http.NewRequestWithContext(req.Context(), req.Method, target, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vs := range s.Header {
		up.Header[k] = vs
	}
	for _, k := range []string{"Range", "If-Range"} {
		if v := req.Header.Get(k); v != "" {
			up.Header.Set(k, v)
		}
	}
	resp, err := r.Client.Do(up)
	if err != nil {
		http.Error(w, "video: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("video: the stream answered %d", resp.StatusCode), http.StatusBadGateway)
		return
	}
	if playlist || isPlaylist(resp, target) {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylist))
		if err != nil {
			http.Error(w, "video: "+err.Error(), http.StatusBadGateway)
			return
		}
		base := resp.Request.URL // after redirects: relative addresses are relative to where the playlist is
		out := r.rewrite(body, base, token)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Length", fmt.Sprint(len(out)))
		w.WriteHeader(http.StatusOK)
		if req.Method == http.MethodGet {
			w.Write(out)
		}
		return
	}
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if req.Method == http.MethodGet {
		io.Copy(w, resp.Body)
	}
}

var uriAttribute = regexp.MustCompile(`URI="([^"]*)"`)

// rewrite points every address in an HLS playlist (its segment and playlist lines, and URI="…" in its tags: keys,
// maps, other renditions) at the relay, signed so the relay reads only what the video's playlists name.
func (r *Relay) rewrite(playlist []byte, base *url.URL, token string) []byte {
	var out bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(playlist))
	scanner.Buffer(make([]byte, 64*1024), maxPlaylist)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "#"):
			line = uriAttribute.ReplaceAllStringFunc(line, func(m string) string {
				return `URI="` + r.through(base, token, uriAttribute.FindStringSubmatch(m)[1]) + `"`
			})
		default:
			line = r.through(base, token, trimmed)
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

// through is ref, resolved against base, as a path through the relay.
func (r *Relay) through(base *url.URL, token, ref string) string {
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ref
	}
	target := u.String()
	return Prefix + token + "/p/" + r.sign(token, target) + "?" + url.Values{"u": {target}}.Encode()
}

func (r *Relay) sign(token, target string) string {
	mac := hmac.New(sha256.New, r.key)
	mac.Write([]byte(token + "\n" + target))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func isPlaylist(resp *http.Response, target string) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(ct, "mpegurl") || looksHLS(target)
}

func looksHLS(address string) bool {
	u, err := url.Parse(address)
	return err == nil && strings.HasSuffix(strings.ToLower(u.Path), ".m3u8")
}

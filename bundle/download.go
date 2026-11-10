// Package bundle downloads what the stories need beside the server, as journal-server-go's bundle does: ONNX Runtime
// and the embedding model. Each file is checked against a sha256 the source publishes (or one kept in the code), and
// files already downloaded and intact are kept, so running it again only fetches what is missing. Used by
// `news-server models`.
package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Logf writes what is being done; log.Printf by default.
var Logf = log.Printf

// cacheDir is where downloads that are unpacked elsewhere (archives) are kept between runs.
func cacheDir(parts ...string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{cache, "news-server"}, parts...)...), nil
}

// download fetches url into path unless a file with sha256 hash is already there; an empty hash is not checked.
func download(ctx context.Context, url, path, hash string) error {
	if hash != "" {
		if sum, err := fileSha256(path); err == nil && sum == hash {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	Logf("Downloading %s", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Accept", "application/octet-stream") // the file, not its description
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	partial := path + ".partial"
	body := io.Reader(resp.Body)
	if resp.ContentLength > 50<<20 {
		body = &progress{r: resp.Body, total: resp.ContentLength, name: filepath.Base(path), last: time.Now()}
	}
	if err := writeFile(partial, body); err != nil {
		os.Remove(partial)
		return fmt.Errorf("%s: %w", url, err)
	}
	if hash != "" {
		if sum, err := fileSha256(partial); err != nil || sum != hash {
			os.Remove(partial)
			return fmt.Errorf("%s: sha256 does not match", url)
		}
	}
	return os.Rename(partial, path)
}

// progress writes how far a large download is, every ten seconds.
type progress struct {
	r           io.Reader
	done, total int64
	name        string
	last        time.Time
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Since(p.last) >= 10*time.Second {
		p.last = time.Now()
		Logf("  %s: %d%% of %d MB", p.name, p.done*100/p.total, p.total>>20)
	}
	return n, err
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func writeFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	f, err := os.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeFile(to, f)
}

func fileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

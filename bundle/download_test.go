package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadChecksTheHashAndKeepsAnIntactFile(t *testing.T) {
	Logf = t.Logf
	body := "model bytes"
	sum := sha256.Sum256([]byte(body))
	hash := hex.EncodeToString(sum[:])
	requests := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte(body))
	}))
	defer site.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "m", "model.onnx")
	if err := download(ctx, site.URL, path, hash); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != body {
		t.Fatalf("got %q", got)
	}
	// Already there and intact: not fetched again.
	if err := download(ctx, site.URL, path, hash); err != nil || requests != 1 {
		t.Fatalf("err %v, %d requests", err, requests)
	}
	// Another hash: refused, nothing left behind.
	other := filepath.Join(filepath.Dir(path), "other.onnx")
	if err := download(ctx, site.URL, other, "00"); err == nil {
		t.Fatal("a wrong hash is an error")
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("a file with the wrong hash was kept")
	}
	if _, err := os.Stat(other + ".partial"); err == nil {
		t.Fatal("the partial file was kept")
	}
}

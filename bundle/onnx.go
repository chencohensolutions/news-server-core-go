package bundle

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/stories"
)

// RuntimeVersion is the ONNX Runtime release to download (journal-server-go's); the server works with 1.16 or later.
const RuntimeVersion = "1.31.0"

// ONNX downloads this platform's ONNX Runtime into runtimeDir (replaced) and model's files into
// modelsDir/<model id>, checked against the sizes and sha256s in stories.ModelSpec. It returns the model's folder.
func ONNX(ctx context.Context, runtimeDir, modelsDir string, model stories.ModelSpec) (string, error) {
	if err := bundleRuntime(ctx, runtimeDir); err != nil {
		return "", fmt.Errorf("ONNX Runtime: %w", err)
	}
	out := filepath.Join(modelsDir, model.ID)
	for _, asset := range []stories.Asset{model.Model, model.Tokenizer} {
		if err := download(ctx, model.BaseURL+"/"+asset.Path, filepath.Join(out, asset.Name), asset.SHA256); err != nil {
			return "", err
		}
	}
	Logf("%s in %s", model.ID, out)
	return out, nil
}

// bundleRuntime puts this platform's ONNX Runtime library (and the libraries it loads) in dir.
func bundleRuntime(ctx context.Context, dir string) error {
	var asset string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		asset = "onnxruntime-win-x64-" + RuntimeVersion + ".zip"
	case "windows/arm64":
		asset = "onnxruntime-win-arm64-" + RuntimeVersion + ".zip"
	case "darwin/arm64":
		asset = "onnxruntime-osx-arm64-" + RuntimeVersion + ".tgz"
	case "linux/amd64":
		asset = "onnxruntime-linux-x64-" + RuntimeVersion + ".tgz"
	case "linux/arm64":
		asset = "onnxruntime-linux-aarch64-" + RuntimeVersion + ".tgz"
	default:
		return fmt.Errorf("no ONNX Runtime download for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	var release struct {
		Assets []struct {
			Name, Digest string
			URL          string `json:"browser_download_url"`
		}
	}
	if err := getJSON(ctx, "https://api.github.com/repos/microsoft/onnxruntime/releases/tags/v"+RuntimeVersion, &release); err != nil {
		return err
	}
	cache, err := cacheDir("onnxruntime")
	if err != nil {
		return err
	}
	archive := filepath.Join(cache, asset)
	for _, a := range release.Assets {
		if a.Name == asset {
			if err := download(ctx, a.URL, archive, strings.TrimPrefix(a.Digest, "sha256:")); err != nil {
				return err
			}
		}
	}
	if _, err := os.Stat(archive); err != nil {
		return fmt.Errorf("%s is not in ONNX Runtime %s", asset, RuntimeVersion)
	}

	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Only the shared libraries in lib/; headers and the static libraries aren't needed.
	wanted := func(name string) (string, bool) {
		dirName, base := path.Split(name)
		isLib := strings.HasSuffix(base, ".dll") || strings.Contains(base, ".dylib") || strings.Contains(base, ".so")
		return base, strings.HasSuffix(dirName, "/lib/") && isLib
	}
	n := 0
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if base, ok := wanted(f.Name); ok {
				r, err := f.Open()
				if err != nil {
					return err
				}
				err = writeFile(filepath.Join(dir, base), r)
				r.Close()
				if err != nil {
					return err
				}
				n++
			}
		}
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		t := tar.NewReader(gz)
		var links [][2]string
		for {
			h, err := t.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			base, ok := wanted(h.Name)
			switch {
			case !ok:
			case h.Typeflag == tar.TypeSymlink:
				// libonnxruntime.dylib → libonnxruntime.1.31.0.dylib; a copy works everywhere.
				links = append(links, [2]string{filepath.Join(dir, path.Base(h.Linkname)), filepath.Join(dir, base)})
				n++
			case h.Typeflag == tar.TypeReg:
				if err := writeFile(filepath.Join(dir, base), t); err != nil {
					return err
				}
				n++
			}
		}
		for _, l := range links {
			if err := copyFile(l[0], l[1]); err != nil {
				return err
			}
		}
	}
	Logf("ONNX Runtime %s: %d files in %s", RuntimeVersion, n, dir)
	return nil
}

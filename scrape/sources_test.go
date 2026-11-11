package scrape

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func withPacks(t *testing.T, packs map[string]fs.FS) {
	t.Helper()
	sourcesMu.Lock()
	saved := packFS
	packFS = map[string]fs.FS{}
	sourcesMu.Unlock()
	t.Cleanup(func() { sourcesMu.Lock(); packFS = saved; sourcesMu.Unlock() })
	for name, fsys := range packs {
		RegisterSources(name, fsys)
	}
}

func TestSourcesComeFromTheFolderAndThePacks(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"b", "_example"} {
		os.MkdirAll(filepath.Join(dir, id), 0o755)
		os.WriteFile(filepath.Join(dir, id, "config.json"), []byte(`{"id":"`+id+`"}`), 0o644)
	}
	withPacks(t, map[string]fs.FS{"p": fstest.MapFS{
		"a/config.json":        {Data: []byte(`{"id":"a"}`)},
		"c/config.json":        {Data: []byte(`{"id":"c"}`)},
		"c/fixtures/list.html": {Data: []byte(`<p>`)},
		"notes/readme.txt":     {Data: []byte(`no config here`)},
	}})
	got, err := ReadSources(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range got {
		ids = append(ids, s.ID+"@"+s.Origin)
	}
	if strings.Join(ids, " ") != "a@pack p b@"+dir+" c@pack p" {
		t.Fatalf("%v", ids)
	}
}

func TestAMissingFolderHasNoSites(t *testing.T) {
	withPacks(t, map[string]fs.FS{"p": fstest.MapFS{"a/config.json": {Data: []byte(`{}`)}}})
	got, err := ReadSources(filepath.Join(t.TempDir(), "none"))
	if err != nil || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestASiteInTwoPlacesIsAnError(t *testing.T) {
	withPacks(t, map[string]fs.FS{
		"p": fstest.MapFS{"a/config.json": {Data: []byte(`{}`)}},
		"q": fstest.MapFS{"a/config.json": {Data: []byte(`{}`)}},
	})
	if _, err := ReadSources(""); err == nil || !strings.Contains(err.Error(), `"a" is in both pack p and pack q`) {
		t.Fatalf("%v", err)
	}
}

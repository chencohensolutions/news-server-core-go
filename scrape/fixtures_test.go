package scrape

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The golden fixtures of every site: this repo's sources/ (the example), or NEWS_SOURCES (a pack's sources/, say
// ../news-sources-xx/sources): the Go engine must give the same output as the Kotlin one on every saved page.
func TestGoldenFixtures(t *testing.T) {
	root := os.Getenv("NEWS_SOURCES")
	if root == "" {
		root = filepath.Join("..", "sources")
	}
	dirs, err := SourceDirs(root)
	if err != nil || len(dirs) == 0 {
		t.Skipf("no sources at %s", root)
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			for _, r := range RunFixtures(dir) {
				if r.Failure != "" {
					name := "config"
					if r.Entry != nil {
						name = r.Entry.HTML
					}
					t.Errorf("%s: %s", name, strings.TrimSpace(r.Failure))
				}
			}
		})
	}
}

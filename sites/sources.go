// Package sites reads the site configs (the news repo's sources/<id>/config.json). For now it reads only what
// the API lists about each site; the full schema (docs/source-config.md in the news repo) comes with the engine
// port.
package sites

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source is one news site.
type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"baseUrl"`
	Language string `json:"language"`
}

// Load reads every dir/<id>/config.json, sorted by id. Folders whose name starts with "_" (the example) are
// skipped. A config whose id isn't its folder's name is an error, as in the Kotlin engine's checks.
func Load(dir string) ([]Source, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sites: %w (is the news submodule checked out? git submodule update --init)", err)
	}
	var found []Source
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name(), "config.json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var s Source
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if s.ID != e.Name() {
			return nil, fmt.Errorf("%s: id is %q, not the folder's name %q", path, s.ID, e.Name())
		}
		found = append(found, s)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	return found, nil
}

// Package sites reads the site configs (sources_dir's and the packs' <id>/config.json) for what the API lists about
// each site, and keeps each site's settings.
package sites

import (
	"encoding/json"
	"fmt"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// Source is one news site.
type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"baseUrl"`
	Language string `json:"language"`
	// The site has an RSS feed the server can read its headlines from instead of its pages.
	HasFeed bool `json:"-"`
}

// Load reads every site's config.json, from dir (sources_dir) and from the packs (scrape.ReadSources), sorted by
// id. A config whose id isn't its folder's name is an error, as in the Kotlin engine's checks.
func Load(dir string) ([]Source, error) {
	files, err := scrape.ReadSources(dir)
	if err != nil {
		return nil, fmt.Errorf("sites: %w (sources_dir in config.json)", err)
	}
	var found []Source
	for _, f := range files {
		var s Source
		var feed struct {
			Feed json.RawMessage `json:"feed"`
		}
		if err := json.Unmarshal(f.Data, &s); err != nil {
			return nil, fmt.Errorf("%s (%s): %w", f.ID, f.Origin, err)
		}
		if err := json.Unmarshal(f.Data, &feed); err == nil && len(feed.Feed) > 0 && string(feed.Feed) != "null" {
			s.HasFeed = true
		}
		if s.ID != f.ID {
			return nil, fmt.Errorf("%s (%s): id is %q, not the folder's name %q", f.ID, f.Origin, s.ID, f.ID)
		}
		found = append(found, s)
	}
	return found, nil
}

package sites

import (
	"context"
	"fmt"
	"sync"

	"github.com/chencohensolutions/news-server-core-go/store"
)

// DefaultWaitMs is a site's wait between two of its requests when nothing else is set (the app's 250 ms).
const DefaultWaitMs = 250

// MaxWaitMs is the longest wait a site can be given: a minute.
const MaxWaitMs = 60_000

// Setting is what the app's Sources screen chose for one site.
type Setting = store.SiteSetting

// Settings are every site's settings: config.json's feed_sources and site_waits_ms at first, then what the app sets,
// kept in the database. Each read of the sites asks it, so a change counts from the next read. Safe for concurrent
// use.
type Settings struct {
	mu   sync.RWMutex
	byID map[string]Setting
	db   *store.Store
	// OnChange, when set, runs after a site's setting is saved, with every site's settings.
	OnChange func(all map[string]Setting)
}

// NewSettings starts from defaults (every site on, feeds from feedSources, waits from waitsMs) and the saved ones.
func NewSettings(ctx context.Context, db *store.Store, found []Source, feedSources []string, waitsMs map[string]int) (*Settings, error) {
	feeds := map[string]bool{}
	for _, id := range feedSources {
		feeds[id] = true
	}
	byID := map[string]Setting{}
	for _, s := range found {
		wait := DefaultWaitMs
		if ms, ok := waitsMs[s.ID]; ok {
			wait = ms
		}
		byID[s.ID] = Setting{Enabled: true, Feed: feeds[s.ID] && s.HasFeed, WaitMs: wait}
	}
	saved, err := db.SiteSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("site settings: %w", err)
	}
	for id, set := range saved {
		if _, ok := byID[id]; ok {
			byID[id] = set
		}
	}
	return &Settings{byID: byID, db: db}, nil
}

// Get is id's setting. A site the server doesn't know (gone from the sources folder) has the defaults, so its kept
// headlines still show until they are pruned.
func (s *Settings) Get(id string) Setting {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if set, ok := s.byID[id]; ok {
		return set
	}
	return Setting{Enabled: true, WaitMs: DefaultWaitMs}
}

// Enabled says whether the server reads id.
func (s *Settings) Enabled(id string) bool { return s.Get(id).Enabled }

// Set saves id's setting.
func (s *Settings) Set(ctx context.Context, id string, set Setting) error {
	s.mu.Lock()
	if _, ok := s.byID[id]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("no site %q", id)
	}
	if err := s.db.SaveSiteSetting(ctx, id, set); err != nil {
		s.mu.Unlock()
		return err
	}
	s.byID[id] = set
	all := make(map[string]Setting, len(s.byID))
	for k, v := range s.byID {
		all[k] = v
	}
	s.mu.Unlock()
	if s.OnChange != nil {
		s.OnChange(all)
	}
	return nil
}

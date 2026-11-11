package scrape

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
)

// SourceFile is one site's config.json as read: from sources_dir on disk or from a pack.
type SourceFile struct {
	// ID is the site's folder name, which its config's id must equal.
	ID string
	// Origin says where it came from, for errors: a folder on disk, or "pack <name>".
	Origin string
	Data   []byte
}

var (
	sourcesMu sync.Mutex
	packFS    = map[string]fs.FS{}
)

// RegisterSources adds a pack's sites: fsys holds <id>/config.json folders, like sources_dir (usually a pack's
// embedded sources/ folder, through fs.Sub). Call it from the pack's init function; pack is the pack's name.
func RegisterSources(pack string, fsys fs.FS) {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	if _, taken := packFS[pack]; taken {
		panic(fmt.Sprintf("scrape: two packs registered sources as %q", pack))
	}
	packFS[pack] = fsys
}

// SourcePacks are the names of the packs that registered sources, sorted.
func SourcePacks() []string {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	names := make([]string, 0, len(packFS))
	for name := range packFS {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ReadSources reads the config.json of every site: dir's (sources_dir; a missing folder has none) and every
// pack's, sorted by id. Folders whose name starts with "_" (the example) or "." are skipped. A site id in two
// places is an error.
func ReadSources(dir string) ([]SourceFile, error) {
	type origin struct {
		name string
		fsys fs.FS
	}
	var origins []origin
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			origins = append(origins, origin{dir, os.DirFS(dir)})
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	for _, name := range SourcePacks() {
		sourcesMu.Lock()
		fsys := packFS[name]
		sourcesMu.Unlock()
		origins = append(origins, origin{"pack " + name, fsys})
	}
	seen := map[string]string{}
	var out []SourceFile
	for _, o := range origins {
		entries, err := fs.ReadDir(o.fsys, ".")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", o.name, err)
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, err := fs.ReadFile(o.fsys, path.Join(e.Name(), "config.json"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", o.name, err)
			}
			if first, dup := seen[e.Name()]; dup {
				return nil, fmt.Errorf("site %q is in both %s and %s", e.Name(), first, o.name)
			}
			seen[e.Name()] = o.name
			out = append(out, SourceFile{ID: e.Name(), Origin: o.name, Data: data})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

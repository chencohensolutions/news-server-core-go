package scrape

import "strings"

// ResolveURL resolves ref against base the way a browser resolves a link on the page at base (Urls.kt).
func ResolveURL(base, ref string) string {
	r := strings.TrimSpace(ref)
	if strings.HasPrefix(r, "http://") || strings.HasPrefix(r, "https://") {
		return r
	}
	scheme, afterScheme := base, base
	if i := strings.Index(base, "://"); i >= 0 {
		scheme, afterScheme = base[:i], base[i+3:]
	}
	origin := scheme + "://" + before(before(before(afterScheme, "/"), "?"), "#")
	switch {
	case strings.HasPrefix(r, "//"):
		return scheme + ":" + r
	case strings.HasPrefix(r, "/"):
		return origin + removeDotSegments(r)
	case strings.HasPrefix(r, "?"):
		return before(before(base, "?"), "#") + r
	case strings.HasPrefix(r, "#"):
		return before(base, "#") + r
	default:
		basePath := before(before(strings.TrimPrefix(base, origin), "?"), "#")
		dir := ""
		if i := strings.LastIndex(basePath, "/"); i >= 0 {
			dir = basePath[:i]
		}
		return origin + removeDotSegments(dir+"/"+r)
	}
}

// before is Kotlin's substringBefore: s up to the first sep, or all of s.
func before(s, sep string) string {
	if i := strings.Index(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

// removeDotSegments collapses "." and ".." path segments (RFC 3986 §5.2.4), leaving any query or fragment alone.
func removeDotSegments(pathAndRest string) string {
	split := strings.IndexAny(pathAndRest, "?#")
	if split < 0 {
		split = len(pathAndRest)
	}
	path := pathAndRest[:split]
	segments := strings.Split(path, "/")[1:]
	var out []string
	for i, seg := range segments {
		last := i == len(segments)-1
		switch seg {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, seg)
		}
	}
	return "/" + strings.Join(out, "/") + pathAndRest[split:]
}

// ListPageURLs are the absolute URLs of a source's headline pages: list.urls, then list.sections.
func (c *SourceConfig) ListPageURLs() []string {
	var out []string
	for _, u := range c.List.URLs {
		out = append(out, ResolveURL(c.BaseURL, u))
	}
	for _, s := range c.List.Sections {
		out = append(out, ResolveURL(c.BaseURL, s.URL))
	}
	return out
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/scrape"
)

// The site tools: the news repo's ./scrape check, extract and fetch, on the Go engine. They need no config.json, so
// anyone writing a source folder can run them.

// runSiteCommand runs name if it is a site tool; handled is false when it isn't one.
func runSiteCommand(name string, args []string, out io.Writer) (handled bool, err error) {
	switch name {
	case "check":
		return true, checkSources(args, out)
	case "extract":
		return true, extractPage(args, out)
	case "fetch":
		return true, fetchSite(args, out)
	}
	return false, nil
}

// checkSources runs the golden fixtures of each folder: a source folder (it holds config.json) or a folder of them.
func checkSources(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: news-server check <sources folder or source folder>...")
	}
	var dirs []string
	for _, arg := range args {
		if _, err := os.Stat(filepath.Join(arg, "config.json")); err == nil {
			dirs = append(dirs, arg)
			continue
		}
		found, err := scrape.SourceDirs(arg)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return fmt.Errorf("%s holds no config.json and no source folders", arg)
		}
		dirs = append(dirs, found...)
	}
	failed := 0
	for _, dir := range dirs {
		results := scrape.RunFixtures(dir)
		bad := 0
		for _, r := range results {
			if r.Failure == "" {
				continue
			}
			bad++
			name := "config"
			if r.Entry != nil {
				name = r.Entry.HTML
			}
			fmt.Fprintf(out, "FAIL %s %s: %s\n", filepath.Base(dir), name, strings.TrimSpace(r.Failure))
		}
		if bad == 0 {
			fmt.Fprintf(out, "ok   %s (%d fixtures)\n", filepath.Base(dir), len(results))
		}
		if bad > 0 {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d sources failed", failed, len(dirs))
	}
	return nil
}

// extractPage reads a saved page with a source's config and prints the result as the fixtures' expected JSON.
func extractPage(args []string, out io.Writer) error {
	if len(args) < 3 || len(args) > 4 {
		return errors.New("usage: news-server extract <source folder> list|feed|article <saved page> [page url]")
	}
	config, err := scrape.LoadConfig(args[0])
	if err != nil {
		return err
	}
	page, err := os.ReadFile(args[2])
	if err != nil {
		return err
	}
	url := config.BaseURL
	if len(args) == 4 {
		url = args[3]
	}
	result, err := scrape.Extract(config, args[1], string(page), url)
	if err != nil {
		return err
	}
	return printJSON(out, result)
}

// fetchSite reads a source's headlines from the live site, or with a URL, that article.
func fetchSite(args []string, out io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: news-server fetch <source folder> [article url]")
	}
	config, err := scrape.LoadConfig(args[0])
	if err != nil {
		return err
	}
	engine := scrape.NewEngine(nil, nil)
	if len(args) == 2 {
		article, err := engine.FetchArticle(context.Background(), config, args[1])
		if err != nil {
			return err
		}
		return printJSON(out, article)
	}
	items, err := engine.FetchLatest(context.Background(), config)
	if err != nil {
		return err
	}
	return printJSON(out, items)
}

func printJSON(out io.Writer, v any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(v)
}

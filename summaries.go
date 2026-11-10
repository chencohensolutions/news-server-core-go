package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/gemini"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// openSummaries is the summary job on config's Gemini key, or why it is off.
func openSummaries(config auth.AppConfig, db *store.Store, found []sites.Source) (*summary.Writer, error) {
	if config.SummaryBudget < 0 {
		return nil, fmt.Errorf("summary_budget is %d", config.SummaryBudget)
	}
	key, err := gemini.ReadKey(config.GeminiKeyFile)
	if err != nil {
		return nil, fmt.Errorf("no Gemini key (%w): put a free-tier key from Google AI Studio in %s", err, config.GeminiKeyFile)
	}
	names := map[string]string{}
	for _, s := range found {
		names[s.ID] = s.Name
	}
	return &summary.Writer{
		Gemini:      &gemini.Client{Key: key, Model: config.GeminiModel},
		Store:       db,
		Budget:      config.SummaryBudget,
		Hours:       config.SummaryHours,
		MinReports:  config.SummaryMinReports,
		MaxRequests: config.SummaryRequestsPerRun,
		Every:       time.Duration(config.RefreshMinutes) * time.Minute,
		SiteName:    func(id string) string { return names[id] },
	}, nil
}

// summarizeCommand is the summarize command: group the stories now, summarize what changed, and print the
// summaries and the digest.
func summarizeCommand(config auth.AppConfig, out io.Writer) error {
	found, err := sites.Load(config.SourcesDir)
	if err != nil {
		return err
	}
	db, settings, err := openSettings(config, found)
	if err != nil {
		return err
	}
	defer db.Close()
	writer, err := openSummaries(config, db, found)
	if err != nil {
		return err
	}
	writer.Log = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	job, err := openStoryJob(config, db)
	if err != nil {
		return err
	}
	defer job.close()
	job.enabled = settings.Enabled
	ctx := context.Background()
	grouped, err := job.run(ctx)
	if err != nil {
		return err
	}
	start := time.Now()
	result, err := writer.Run(ctx, grouped)
	fmt.Fprintf(out, "%d stories, %d summaries written, %d waiting, digest %v, in %s\n\n", len(grouped), result.Written,
		result.Waiting, result.Digest, time.Since(start).Round(time.Millisecond))
	if err != nil {
		return err
	}
	if digest, err := summary.LatestDigest(ctx, db); err == nil && digest != nil {
		fmt.Fprintf(out, "## %s (%s)\n", digest.Title, digest.WrittenAt.Local().Format("15:04"))
		printSentences(out, digest.Sentences)
		fmt.Fprintln(out)
	}
	saved, err := summary.Summaries(ctx, db)
	if err != nil {
		return err
	}
	for _, s := range grouped {
		if sum, ok := saved[summary.StoryKey(s)]; ok {
			fmt.Fprintf(out, "### %s\n", sum.Title)
			printSentences(out, sum.Sentences)
			if len(sum.Contradictions) > 0 {
				fmt.Fprintln(out, "  Contradictions:")
				printSentences(out, sum.Contradictions)
			}
			fmt.Fprintln(out)
		}
	}
	return nil
}

func printSentences(out io.Writer, sentences []summary.Sentence) {
	for _, s := range sentences {
		fmt.Fprintf(out, "  %s\n", s.Text)
		for _, url := range s.Sources {
			fmt.Fprintf(out, "      %s\n", url)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/bundle"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/stories"
)

// storyJob groups the stored headlines into stories after each read of the sites, with the model config.json names
// and the grouping the app set (config.json's until it does), and keeps the latest stories for the API.
type storyJob struct {
	db       *store.Store
	embedder interface {
		stories.TextEmbedder
		Close()
	}
	model stories.ModelSpec
	// enabled, when set, says whether a site's headlines are grouped: the sites switched off are left out.
	enabled func(id string) bool
	// summaries says the summary job runs after each grouping (serverInfo.summaries).
	summaries bool

	// running is held by a grouping: one at a time, as the finder isn't safe for concurrent use.
	running sync.Mutex
	finder  *stories.Finder

	mu sync.Mutex
	// embedded is how many headlines the last grouping embedded.
	embedded int
	grouping stories.Grouping
	latest   []stories.Story
	at       time.Time
}

// groupingKey is where the app's grouping is kept in the database.
const groupingKey = "grouping"

// storySettings checks config.json's story keys: the model, the text and the threshold its strictness is.
func storySettings(config auth.AppConfig) (stories.ModelSpec, stories.StoryText, float32, error) {
	model, ok := stories.Models[config.StoryModel]
	if !ok {
		return stories.ModelSpec{}, 0, 0, fmt.Errorf("story_model %q is not bge-m3 or multilingual-e5-small", config.StoryModel)
	}
	text, err := stories.ParseStoryText(config.StoryText)
	if err != nil {
		return stories.ModelSpec{}, 0, 0, fmt.Errorf("story_text: %w", err)
	}
	if config.StoryStrictness < stories.StrictnessMin || config.StoryStrictness > stories.StrictnessMax {
		return stories.ModelSpec{}, 0, 0, fmt.Errorf("story_strictness %d is not between %d and %d", config.StoryStrictness, stories.StrictnessMin, stories.StrictnessMax)
	}
	return model, text, model.Scale(text).Threshold(config.StoryStrictness), nil
}

// openStoryJob loads ONNX Runtime and the model; an error says what to download. The grouping the app saved, if
// any, replaces config.json's.
func openStoryJob(config auth.AppConfig, db *store.Store) (*storyJob, error) {
	model, text, _, err := storySettings(config)
	if err != nil {
		return nil, err
	}
	grouping, err := savedGrouping(db, stories.Grouping{Text: text, Strictness: config.StoryStrictness, Days: config.StoryDays})
	if err != nil {
		return nil, err
	}
	embedder, err := openEmbedder(config, model)
	if err != nil {
		return nil, err
	}
	return &storyJob{db: db, embedder: embedder, model: model, grouping: grouping}, nil
}

// savedGrouping is the grouping the app saved, else config (config.json's).
func savedGrouping(db *store.Store, config stories.Grouping) (stories.Grouping, error) {
	saved, ok, err := db.ServerSetting(context.Background(), groupingKey)
	if err != nil || !ok {
		return config, err
	}
	g, err := parseGrouping(saved)
	if err != nil {
		log.Printf("stories: the saved grouping is ignored: %v", err)
		return config, nil
	}
	return g, nil
}

func parseGrouping(data []byte) (stories.Grouping, error) {
	var g stories.Grouping
	if err := json.Unmarshal(data, &g); err != nil {
		return g, err
	}
	text, err := stories.ParseStoryText(g.TextName)
	if err != nil {
		return g, err
	}
	g.Text = text
	return g, g.Check()
}

// Grouping is how the job groups stories now, and the similarity that is.
func (j *storyJob) Grouping() (stories.Grouping, float32) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.grouping, j.model.Scale(j.grouping.Text).Threshold(j.grouping.Strictness)
}

// SetGrouping keeps g and groups the stories again with it, in the background.
func (j *storyJob) SetGrouping(ctx context.Context, g stories.Grouping) error {
	if err := g.Check(); err != nil {
		return err
	}
	g.TextName = g.Text.String()
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if err := j.db.SaveServerSetting(ctx, groupingKey, data); err != nil {
		return err
	}
	j.mu.Lock()
	j.grouping = g
	j.mu.Unlock()
	go func() {
		start := time.Now()
		found, err := j.run(context.Background())
		if err != nil {
			log.Printf("stories: regrouping after the app's change: %v", err)
			return
		}
		log.Printf("stories: regrouped by %s at strictness %d over %d days: %d stories in %s", g.Text, g.Strictness, g.Days,
			len(found), time.Since(start).Round(time.Millisecond))
	}()
	return nil
}

// openEmbedder loads ONNX Runtime and model from models_dir.
func openEmbedder(config auth.AppConfig, model stories.ModelSpec) (*stories.Embedder, error) {
	rt, err := stories.LoadRuntime(config.ONNXRuntime)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(config.ModelsDir, model.ID)
	tokenizer, err := stories.LoadTokenizer(filepath.Join(dir, model.Tokenizer.Name))
	if err != nil {
		return nil, fmt.Errorf("%s's tokenizer (news-server models downloads it): %w", model.ID, err)
	}
	// Half the cores: the PC is also someone's computer.
	embedder, err := stories.NewEmbedder(rt, filepath.Join(dir, model.Model.Name), tokenizer, model, max(1, runtime.NumCPU()/2))
	if err != nil {
		return nil, fmt.Errorf("%s (news-server models downloads it): %w", model.ID, err)
	}
	return embedder, nil
}

func (j *storyJob) close() { j.embedder.Close() }

// run groups the stored headlines of the grouping's days.
func (j *storyJob) run(ctx context.Context) ([]stories.Story, error) {
	j.running.Lock()
	defer j.running.Unlock()
	g, threshold := j.Grouping()
	if j.finder == nil || j.finder.Text() != g.Text {
		// Another text is other vectors: embedded once, kept in the database beside the others.
		j.finder = stories.NewFinder(j.embedder, j.db, j.model, g.Text)
	}
	// The grouping's days, by when a headline was last on its site too: one without a time isn't grouped forever.
	days := g.Days
	if days == 0 {
		days = stories.MaxDays
	}
	headlines, err := j.db.Headlines(ctx, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}
	items := make([]scrape.ArticleSummary, 0, len(headlines))
	for _, h := range headlines {
		if j.enabled == nil || j.enabled(h.SourceID) {
			items = append(items, h.ArticleSummary)
		}
	}
	now := time.Now()
	feed := stories.WithinDays(items, g.Days, now)
	found, err := j.finder.Find(ctx, feed, threshold)
	j.mu.Lock()
	j.embedded = j.finder.LastEmbedded
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// What Gemini found to be about another event goes to the story it is closest to, or out.
	flags, err := j.db.Unrelated(ctx, now.AddDate(0, 0, -max(1, g.Days)))
	if err != nil {
		return nil, err
	}
	found = j.finder.MoveUnrelated(found, feed, flags, threshold)
	j.mu.Lock()
	j.latest, j.at = found, now
	j.mu.Unlock()
	return found, nil
}

// Embedded is how many headlines the last grouping embedded.
func (j *storyJob) Embedded() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.embedded
}

// Stories are the latest run's stories and when it ran.
func (j *storyJob) Stories() ([]stories.Story, time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.latest, j.at
}

// downloadModels is the models command: ONNX Runtime and config.json's story model.
func downloadModels(config auth.AppConfig, out io.Writer) error {
	model, _, _, err := storySettings(config)
	if err != nil {
		return err
	}
	// Always ./onnxruntime (replaced), never the folder of a configured library: that could be anything.
	runtimeDir := "onnxruntime"
	dir, err := bundle.ONNX(context.Background(), runtimeDir, config.ModelsDir, model)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Done: ONNX Runtime in %s, %s in %s\n", runtimeDir, model.ID, dir)
	if config.ONNXRuntime != "" {
		fmt.Fprintf(out, "config.json's \"onnxruntime\" still names %s; empty it to use the one downloaded\n", config.ONNXRuntime)
	}
	return nil
}

// printStories is the stories command: the stored headlines grouped now, as the server does after each read.
func printStories(config auth.AppConfig, out io.Writer) error {
	db, err := store.Open(config.DatabaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	job, err := openStoryJob(config, db)
	if err != nil {
		return err
	}
	defer job.close()
	start := time.Now()
	found, err := job.run(context.Background())
	if err != nil {
		return err
	}
	g, threshold := job.Grouping()
	fmt.Fprintf(out, "%d stories (%s, %s, strictness %d: similarity %.3f); %d headlines embedded in %s\n\n",
		len(found), config.StoryModel, g.Text, g.Strictness, threshold, job.Embedded(),
		time.Since(start).Round(time.Millisecond))
	for _, s := range found {
		for i, r := range s.Reports {
			mark := "  "
			if i == 0 {
				mark = "* "
			}
			fmt.Fprintf(out, "%s%-10s %-20s %s\n", mark, r.SourceID, r.PublishedAt, r.Title)
		}
		fmt.Fprintln(out)
	}
	return nil
}

// evalSetDir is the app's story model test set, in the news submodule.
var evalSetDir = filepath.Join("news", "tools", "story-model-eval")

// appScores are the app's measurements on the test set's titles and list summaries (title + ". " + summary), from
// the news repo's docs/story-model-comparison.md: AUC, best F1 and its threshold.
var appScores = map[string][3]float64{
	stories.BGEM3.ID:   {0.950, 0.83, 0.660},
	stories.E5Small.ID: {0.929, 0.79, 0.893},
}

// evalStories is the eval-stories command: config.json's story model on the app's 348 labeled pairs, against the
// app's numbers, and the stories it groups at story_strictness.
func evalStories(config auth.AppConfig, out io.Writer) error {
	model, _, _, err := storySettings(config)
	if err != nil {
		return err
	}
	items, pairs, err := stories.LoadEvalSet(evalSetDir)
	if err != nil {
		return fmt.Errorf("the test set (the news submodule's tools/story-model-eval): %w", err)
	}
	embedder, err := openEmbedder(config, model)
	if err != nil {
		return err
	}
	defer embedder.Close()
	// As the app measured: the title and the list's summary in one text.
	text := stories.TitleAndSubtitle
	start := time.Now()
	vectors := make([][]float32, len(items))
	for i, item := range items {
		if vectors[i], err = embedder.Embed(text.Texts(item)[0]); err != nil {
			return fmt.Errorf("%s: %w", item.URL, err)
		}
	}
	took := time.Since(start)
	similarities := make([]float32, len(pairs))
	for k, p := range pairs {
		similarities[k] = stories.Dot(vectors[p.I], vectors[p.J])
	}
	s := stories.ScorePairs(pairs, similarities)
	fmt.Fprintf(out, "%s: %d headlines in %s (%s a headline)\n", model.ID, len(items), took.Round(time.Millisecond),
		(took / time.Duration(len(items))).Round(100*time.Microsecond))
	fmt.Fprintf(out, "%d labeled pairs, %d the same event\n", s.Pairs, s.Same)
	fmt.Fprintf(out, "server: AUC %.3f, best F1 %.2f at %.3f (precision %.2f, recall %.2f)\n", s.AUC, s.F1, s.Threshold, s.Precision, s.Recall)
	if app, ok := appScores[model.ID]; ok {
		fmt.Fprintf(out, "app:    AUC %.3f, best F1 %.2f at %.3f\n", app[0], app[1], app[2])
	}
	threshold := model.Scale(text).Threshold(config.StoryStrictness)
	groups := stories.ClusterBySimilarity(vectors, threshold, true, nil)
	n := 0
	for _, g := range groups {
		sites := map[string]bool{}
		for _, i := range g {
			sites[items[i].SourceID] = true
		}
		if len(sites) > 1 {
			n++
		}
	}
	fmt.Fprintf(out, "%d stories of two sites or more at strictness %d (similarity %.3f)\n", n, config.StoryStrictness, threshold)
	return nil
}

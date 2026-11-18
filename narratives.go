package newsserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/narrative"
	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// narratives is the index of the stories' and threads' vectors, by the job's model.
func (j *storyJob) narratives() *narrative.Index { return narrative.New(j.db, j.embedder, j.model.ID) }

// narrativesCommand is the narratives command: embed every kept story not embedded yet and make the threads'
// vectors (the server does it before each linking; this does the first, long one ahead), then measure the shortlist.
func narrativesCommand(config auth.AppConfig, out io.Writer) error {
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
	ix := job.narratives()
	w := &summary.Writer{Store: db, Narratives: ix, Log: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}
	start := time.Now()
	if err := w.CatchUpNarratives(context.Background()); err != nil {
		return err
	}
	fmt.Fprintf(out, "Narrative vectors (%s) up to date in %s\n\n", ix.Space, time.Since(start).Round(time.Millisecond))
	return evalNarratives(db, ix, out)
}

// evalNarratives measures the shortlist on the threads Gemini linked so far (narrative.Evaluate).
func evalNarratives(db *store.Store, ix *narrative.Index, out io.Writer) error {
	ctx := context.Background()
	members, err := db.ThreadMembers(ctx)
	if err != nil {
		return err
	}
	threadsOf := map[string][]string{}
	for t, ids := range members {
		for _, id := range ids {
			threadsOf[id] = append(threadsOf[id], t)
		}
	}
	var all []narrative.EvalStory
	if err := db.Narratives(ctx, ix.Space, store.KindStory, func(n store.Narrative) error {
		all = append(all, narrative.EvalStory{ID: n.Ref, At: n.At, Vector: n.Vector, Threads: threadsOf[n.Ref]})
		return nil
	}); err != nil {
		return err
	}
	threads := len(members)
	fmt.Fprintf(out, "%d stories with vectors, %d threads with stories; the linking listed up to 3000 threads before\n", len(all), threads)
	start := time.Now()
	day := 24 * time.Hour
	results := narrative.Evaluate(all, 500, summary.DefaultThreadCandidates, 20, []time.Duration{0, 7 * day, narrative.HalfLife, 90 * day})
	fmt.Fprintf(out, "Would the shortlist have listed a thread Gemini put the story in (latest stories that continue a thread; %d nearest each way):\n", summary.DefaultThreadCandidates)
	narrative.PrintEval(out, results)
	fmt.Fprintf(out, "(in %s)\n", time.Since(start).Round(time.Millisecond))
	data, _ := json.Marshal(results)
	fmt.Fprintf(out, "%s\n", data)
	return nil
}

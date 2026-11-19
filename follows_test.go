package newsserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/store"
	"github.com/chencohensolutions/news-server-core-go/summary"
)

// A user follows a story and gets what came after: its new facts and the stories that joined its thread; another
// user's follows are their own.
func TestFollowingAStoryAndItsThread(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	hen, _ := env.enrollDevice(t, "hen")
	dana, _ := env.enrollDevice(t, "dana")
	danaTablet, _ := env.enrollDevice(t, "dana")
	now := time.Now().UTC()
	save := func(s summary.StorySummary, at time.Time) {
		t.Helper()
		data, _ := json.Marshal(s)
		if err := env.db.SaveStoryRecord(ctx, s.ID, data, at); err != nil {
			t.Fatal(err)
		}
	}
	say := func(text string) []summary.Sentence { return []summary.Sentence{{Text: text}} }
	storm := summary.StorySummary{ID: "storm", Title: "סערה בצפון", WrittenAt: now.Add(-time.Hour),
		Updates: []summary.Update{{At: now.Add(-time.Hour), Sentences: say("רוחות חזקות.")}}}
	save(storm, storm.WrittenAt)

	_, resp := env.query(t, dana, `mutation { followStory(id: "nothing", follow: true) }`, nil)
	if len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != "BAD_REQUEST" {
		t.Fatalf("following no story: %+v", resp.Errors)
	}
	_, resp = env.query(t, dana, `mutation { followStory(id: "storm", follow: true) }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["followStory"]) != "true" {
		t.Fatalf("followStory: %+v %s", resp.Errors, resp.Data["followStory"])
	}

	// After following: a new version, and a story that joined the storm's thread.
	storm.Updates = append(storm.Updates, summary.Update{At: now.Add(time.Minute), Sentences: say("הכביש נסגר."), Kinds: []string{"development"}})
	storm.WrittenAt = now.Add(time.Minute)
	save(storm, storm.WrittenAt)
	after := summary.StorySummary{ID: "after", Title: "אחרי הסערה", WrittenAt: now.Add(2 * time.Minute),
		Updates: []summary.Update{{At: now.Add(2 * time.Minute), Sentences: say("מתחילים לפנות.")}}}
	save(after, after.WrittenAt)
	if err := env.db.SaveThread(ctx, store.Thread{ID: "winter", Name: "החורף", Summary: "עד כה", StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"storm", "after"} {
		if err := env.db.LinkStory(ctx, id, []string{"winter"}); err != nil {
			t.Fatal(err)
		}
	}

	query := `{ following { id title threads { name } updates { storyId storyTitle newStory sentences { text } kinds } } }`
	_, resp = env.query(t, danaTablet, query, nil)
	want := `[{"id":"storm","title":"סערה בצפון","threads":[{"name":"החורף"}],"updates":[` +
		`{"storyId":"after","storyTitle":"אחרי הסערה","newStory":true,"sentences":[{"text":"מתחילים לפנות."}],"kinds":[]},` +
		`{"storyId":"storm","storyTitle":"סערה בצפון","newStory":false,"sentences":[{"text":"הכביש נסגר."}],"kinds":["development"]}]}]`
	if len(resp.Errors) > 0 || string(resp.Data["following"]) != want {
		t.Fatalf("following:\n%+v\n%s\nwant\n%s", resp.Errors, resp.Data["following"], want)
	}
	_, resp = env.query(t, hen, query, nil)
	if string(resp.Data["following"]) != `[]` {
		t.Fatalf("hen sees dana's follows: %s", resp.Data["following"])
	}

	_, resp = env.query(t, dana, `mutation { followStory(id: "storm", follow: false) }`, nil)
	if len(resp.Errors) > 0 || string(resp.Data["followStory"]) != "false" {
		t.Fatalf("unfollow: %+v %s", resp.Errors, resp.Data["followStory"])
	}
	_, resp = env.query(t, dana, `{ following { id } }`, nil)
	if !strings.HasPrefix(string(resp.Data["following"]), `[]`) {
		t.Fatalf("still followed: %s", resp.Data["following"])
	}
}

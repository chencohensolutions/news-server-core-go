package narrative

import (
	"fmt"
	"testing"
	"time"
)

func vec(xs ...float32) []float32 { return Normalize(append([]float32{}, xs...)) }

func TestTheTidyUpFindsMergesSplitsAndStrays(t *testing.T) {
	story := func(id string, d int, v []float32) TidyStory {
		return TidyStory{ID: id, At: day.Add(time.Duration(d) * 24 * time.Hour), Vector: v}
	}
	var mixed []TidyStory
	for i := range 4 {
		mixed = append(mixed, story(fmt.Sprintf("war%d", i), i, vec(1, 0, 0, float32(i)*0.05)))
		mixed = append(mixed, story(fmt.Sprintf("trial%d", i), i, vec(0, 1, 0, float32(i)*0.05)))
	}
	mixed = append(mixed, story("odd", 9, vec(0.9, 0.2, 0, 0)))
	threads := []TidyThread{
		{ID: "war", Vector: vec(1, 0, 0, 0), Stories: []TidyStory{story("a", 0, vec(1, 0, 0, 0))}},
		{ID: "war-again", Vector: vec(1, 0.1, 0, 0), Stories: []TidyStory{story("b", 1, vec(1, 0.1, 0, 0))}},
		{ID: "mixed", Vector: vec(1, 1, 0, 0), Stories: mixed},
		{ID: "strike", Vector: vec(0, 0, 1, 0), Stories: []TidyStory{
			story("s1", 0, vec(0, 0, 1, 0)), story("s2", 1, vec(0, 0.1, 1, 0)), story("s3", 2, vec(0, 0, 1, 0.1)),
			story("lost", 3, vec(1, 0, 0, 0)),
		}},
	}
	c := FindTidyCandidates(threads)
	if len(c.Merges) != 1 || c.Merges[0].A != "war" || c.Merges[0].B != "war-again" {
		t.Fatalf("merges %+v", c.Merges)
	}
	if len(c.Splits) != 1 || c.Splits[0].Thread != "mixed" || len(c.Splits[0].Groups) != 2 ||
		len(c.Splits[0].Groups[0])+len(c.Splits[0].Groups[1]) != 9 {
		t.Fatalf("splits %+v", c.Splits)
	}
	if len(c.Strays) != 1 || c.Strays[0].Story != "lost" {
		t.Fatalf("strays %+v", c.Strays)
	}
}

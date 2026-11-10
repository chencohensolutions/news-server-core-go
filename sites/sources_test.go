package sites

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSkipsTheExampleAndSorts(t *testing.T) {
	dir := t.TempDir()
	write := func(folder, body string) {
		os.MkdirAll(filepath.Join(dir, folder), 0o755)
		os.WriteFile(filepath.Join(dir, folder, "config.json"), []byte(body), 0o644)
	}
	write("siteh", `{"id":"siteh","name":"siteh","baseUrl":"https://www.siteh.example","language":"he"}`)
	write("sitea", `{"id":"sitea","name":"אתר א","baseUrl":"https://sitea.example","language":"he"}`)
	write("_example", `{"id":"example","name":"Example","baseUrl":"https://example.com","language":"en"}`)
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "sitea" || got[1].ID != "siteh" || got[0].Name != "אתר א" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadRefusesAnIdThatIsNotTheFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "siteh"), 0o755)
	os.WriteFile(filepath.Join(dir, "siteh", "config.json"), []byte(`{"id":"siteg"}`), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("want an error")
	}
}

// The real configs, when the news submodule is checked out.
func TestLoadTheNewsRepoSources(t *testing.T) {
	dir := filepath.Join("..", "news", "sources")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("news submodule not checked out")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 30 {
		t.Fatalf("only %d sources", len(got))
	}
	feeds := map[string]bool{}
	for _, s := range got {
		feeds[s.ID] = s.HasFeed
	}
	if !feeds["siteb"] || feeds["sitee"] {
		t.Fatalf("siteb has a feed, sitee doesn't: %v", feeds)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModulesAreFoldersOrPathsWithVersions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pack\n\ngo 1.26\n"), 0o644)
	m, err := parseModule(dir)
	if err != nil || m.Path != "example.com/pack" || m.Dir != dir {
		t.Fatalf("%+v %v", m, err)
	}
	if m, _ := parseModule("example.com/other@v1.2.0"); m.Path != "example.com/other" || m.Version != "v1.2.0" || m.Dir != "" {
		t.Fatalf("%+v", m)
	}
	if m, _ := parseModule("example.com/other"); m.Version != "latest" {
		t.Fatalf("%+v", m)
	}
	if _, err := parseModule("./missing"); err == nil {
		t.Fatal("a folder without go.mod")
	}
}

func TestTheMainPackageImportsEveryPack(t *testing.T) {
	src := mainSource([]module{{Path: "example.com/a"}, {Path: "example.com/b"}})
	for _, want := range []string{`newsserver "` + coreModule + `"`, `_ "example.com/a"`, `_ "example.com/b"`, "newsserver.Main()"} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing %s in\n%s", want, src)
		}
	}
}

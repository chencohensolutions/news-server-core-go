// news-server reads the news sites for the news app: headlines, articles and stories, and (later) their Gemini
// summaries. Phones reach it over GraphQL with mutual TLS, enrolled the way the journal app is. See README.md.
package newsserver

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/fetch"
	"github.com/chencohensolutions/news-server-core-go/scrape"
	"github.com/chencohensolutions/news-server-core-go/video"
)

const configPath = "config.json"

// version is set at build time: go build -ldflags "-X github.com/chencohensolutions/news-server-core-go.version=1.0.0".
var version = "dev"

const usage = `usage:
  news-server                      run the server
  news-server version              the version
  news-server enroll-code <user> [--admin|--user]
                                   create a one-time enrollment code (and its QR code) for a phone; a new user is
                                   made as a user unless --admin (an admin changes the server's settings in the app)
  news-server devices [user]       list enrolled devices
  news-server revoke <serial>      revoke an enrolled device
  news-server refresh              read every site once now (the server does it every refresh_minutes)
  news-server discovery            publish this server's addresses in the DHT and look them up again, to check it works
  news-server models               download ONNX Runtime and the story model (story_model) into models_dir
  news-server stories              group the stored headlines into stories now and print them
  news-server summarize            group the stories and summarize what changed with Gemini now, and print them
  news-server eval-stories         score the story model on the app's 348 labeled headline pairs
  news-server eval-gemini-grouping score Gemini's grouping on the same pairs (one request of the day's budget)
  news-server incremental [off|shadow|on]
                                   the incremental story update: off, in shadow beside the whole rewrite, or on in its
                                   place; and print what it recorded (docs/incremental.md)
  news-server eval-incremental [-days N] [-until day] [-batch 8] [-max-requests 10] [-sample N] [-seed 1] [-offset N]
                               [-cases file] [-dry]
                                   replay the archive's rewrites through the incremental update and report; -cases
                                   writes every step with what was sent and answered, to read (docs/incremental.md)
  news-server archive              bring the archive folder (archive_dir) up to date now; the server does it after each read
  news-server restore              make a new database_file from the archive folder (after installing from scratch)

site tools (no config.json needed):
  news-server check <folder>...                              run the golden fixtures of a source folder, or of every source in a folder
  news-server extract <source> list|feed|article <page> [url] read a saved page with a source's config, print it as JSON
  news-server fetch <source> [article url]                   read a source's headlines (or one article) from the live site
`

// Main runs news-server: the command line in os.Args (see usage). A build with packs (cmd/newsbuild) calls it from
// its own main, after the packs registered themselves in their init functions.
func Main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("news-server", version)
		for _, built := range []struct {
			what  string
			names []string
		}{{"transports", fetch.Transports()}, {"sources", scrape.SourcePacks()}, {"video resolvers", video.Names()}} {
			if len(built.names) > 0 {
				fmt.Printf("packs' %s: %s\n", built.what, strings.Join(built.names, ", "))
			}
		}
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print(usage)
		return
	}
	if len(os.Args) >= 2 {
		if handled, err := runSiteCommand(os.Args[1], os.Args[2:], os.Stdout); handled {
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			return
		}
	}
	if _, err := auth.UseInstallFolder(configPath); err != nil {
		log.Fatal(err)
	}
	config, err := auth.LoadConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}
	if len(os.Args) < 2 {
		serve(config)
		return
	}
	local := map[string]func(auth.AppConfig, io.Writer) error{"refresh": refreshOnce, "discovery": func(c auth.AppConfig, _ io.Writer) error { return discoveryCommand(c) }, "models": downloadModels, "stories": printStories, "summarize": summarizeCommand,
		"eval-stories": evalStories, "eval-gemini-grouping": evalGeminiGrouping, "archive": archiveCommand, "restore": restoreCommand}
	if run, ok := local[os.Args[1]]; ok && len(os.Args) == 2 {
		if err := run(config, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	withArgs := map[string]func(auth.AppConfig, []string, io.Writer) error{"incremental": incrementalCommand, "eval-incremental": evalIncremental}
	if run, ok := withArgs[os.Args[1]]; ok {
		if err := run(config, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := runAdminCommand(config, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

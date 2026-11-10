// news-server reads the news sites for the news app: headlines, articles and stories, and (later) their Gemini
// summaries. Phones reach it over GraphQL with mutual TLS, enrolled the way the journal app is. See README.md.
package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/chencohensolutions/news-server-core-go/auth"
)

const configPath = "config.json"

// version is set at build time: go build -ldflags "-X main.version=1.0.0 (3f2a9c1)".
var version = "dev"

const usage = `usage:
  news-server                      run the server
  news-server version              the version
  news-server enroll-code <user>   create a one-time enrollment code (and its QR code) for a phone
  news-server devices [user]       list enrolled devices
  news-server revoke <serial>      revoke an enrolled device
  news-server refresh              read every site once now (the server does it every refresh_minutes)
  news-server discovery            publish this server's addresses in the DHT and look them up again, to check it works
  news-server models               download ONNX Runtime and the story model (story_model) into models_dir
  news-server stories              group the stored headlines into stories now and print them
  news-server eval-stories         score the story model on the app's 348 labeled headline pairs

site tools (no config.json needed):
  news-server check <folder>...                              run the golden fixtures of a source folder, or of every source in a folder
  news-server extract <source> list|feed|article <page> [url] read a saved page with a source's config, print it as JSON
  news-server fetch <source> [article url]                   read a source's headlines (or one article) from the live site
`

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println("news-server", version)
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
	local := map[string]func(auth.AppConfig, io.Writer) error{"refresh": refreshOnce, "discovery": func(c auth.AppConfig, _ io.Writer) error { return discoveryCommand(c) }, "models": downloadModels, "stories": printStories,
		"eval-stories": evalStories}
	if run, ok := local[os.Args[1]]; ok && len(os.Args) == 2 {
		if err := run(config, os.Stdout); err != nil {
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

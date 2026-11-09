// news-server reads the news sites for the news app: headlines, articles and stories, and (later) their Gemini
// summaries. Phones reach it over GraphQL with mutual TLS, enrolled the way the journal app is. See README.md.
package main

import (
	"fmt"
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
	if err := runAdminCommand(config, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// news-server without packs: the plain server, reading only what its site configs describe. To build it with packs,
// use newsbuild (cmd/newsbuild).
package main

import newsserver "github.com/chencohensolutions/news-server-core-go"

func main() { newsserver.Main() }

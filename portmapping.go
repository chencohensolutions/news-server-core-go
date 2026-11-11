package newsserver

import (
	"context"
	"log"
	"strconv"
	"sync/atomic"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/portmap"
)

// running is the server's port mapping while it runs, nil when it is off.
var running atomic.Pointer[portmap.Mapper]

// mapPort asks the router to forward the server's port to this machine while ctx runs (package portmap, as the
// journal server does), unless config.json sets "port_mapping": false. done is closed once the mapping is removed
// after ctx ends (at once when there is none).
func mapPort(ctx context.Context, config auth.AppConfig, done chan<- struct{}) {
	port, err := strconv.ParseUint(config.Port, 10, 16)
	if !config.PortMappingOn() || err != nil {
		close(done)
		return
	}
	m := &portmap.Mapper{Port: uint16(port), Description: "News server"}
	running.Store(m)
	go func() { m.Run(ctx); close(done) }()
	if config.PublicURL == "" {
		log.Printf("public_url is empty: enrollment links get the router's outside address once it maps port %d", port)
	}
}

// mappedURL is the router's outside address for the server's port, "" when there is none (yet).
func mappedURL() string {
	m := running.Load()
	if m == nil {
		return ""
	}
	return m.Current().URL()
}

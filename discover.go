package newsserver

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/discovery"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
)

// Finding the server when its address changes, as journal-server-go does (package discovery): the server publishes
// where it is in the BitTorrent DHT under a key derived from its TLS key, the enrollment link and serverInfo give
// the app that key and the current addresses, and the app looks the server up there when none of them answers.

// serverDiscovery is this server's side: its fingerprint, its DHT key and the publisher of its addresses.
type serverDiscovery struct {
	fingerprint string
	key         ed25519.PrivateKey // nil with "discovery": false
	publisher   *discovery.Publisher
}

// dhtNode is this process's place in the DHT; it joins at first use.
var dhtNode = &discovery.Node{}

// openDiscovery reads this server's key. Unless "discovery": false, its publisher publishes in the DHT once run.
func openDiscovery(config auth.AppConfig) (*serverDiscovery, error) {
	pair, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	d := &serverDiscovery{fingerprint: Fingerprint(leaf)}
	d.publisher = &discovery.Publisher{Fingerprint: d.fingerprint, PublicURL: config.PublicURL, Port: config.Port,
		STUN: discovery.STUNServers, Mapped: mappedURL}
	if config.DiscoveryOn() {
		if d.key, err = discovery.KeyFor(pair.PrivateKey); err != nil {
			return nil, err
		}
		d.publisher.Node, d.publisher.Key = dhtNode, d.key
	}
	return d, nil
}

func (d *serverDiscovery) dhtKey() string {
	if d.key == nil {
		return ""
	}
	return hex.EncodeToString(d.key.Public().(ed25519.PublicKey))
}

// thisServer is the running server's discovery, nil before it is opened.
var thisServer atomic.Pointer[serverDiscovery]

// startDiscovery opens and runs the server's discovery until ctx ends; without it the app has only the address it
// enrolled with.
func startDiscovery(ctx context.Context, config auth.AppConfig) error {
	d, err := openDiscovery(config)
	if err != nil {
		return err
	}
	thisServer.Store(d)
	go d.publisher.Run(ctx)
	return nil
}

// runningDiscovery is the admin API's view of the running server: fingerprint, DHT key and current addresses.
func runningDiscovery() (fingerprint, key string, addresses []string) {
	d := thisServer.Load()
	if d == nil {
		return "", "", nil
	}
	return d.fingerprint, d.dhtKey(), d.publisher.Current()
}

// forPhones is serverInfo.discovery: who this server is and where, or nil before it is known.
func forPhones() *model.ServerDiscovery {
	d := thisServer.Load()
	if d == nil {
		return nil
	}
	out := &model.ServerDiscovery{Fingerprint: d.fingerprint, ID: discovery.FormatID(d.fingerprint),
		Addresses: d.publisher.Current()}
	if out.Addresses == nil {
		out.Addresses = []string{}
	}
	if key := d.dhtKey(); key != "" {
		out.Key = &key
	}
	return out
}

// discoveryCommand: news-server discovery. Works out where this server is, publishes it in the DHT and looks it up
// again, to check discovery works from this network. The running server does the same by itself.
func discoveryCommand(config auth.AppConfig) error {
	d, err := openDiscovery(config)
	if err != nil {
		return err
	}
	if d.key == nil {
		return errors.New(`discovery is off in config.json ("discovery": false)`)
	}
	defer dhtNode.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fmt.Printf("Server id %s\nDHT key   %s\n\n", discovery.FormatID(d.fingerprint), d.dhtKey())
	addresses := d.publisher.Addresses(ctx)
	fmt.Printf("This server's addresses (without the router's mapping, which only the running server holds):\n")
	for _, a := range addresses {
		fmt.Printf("  %s\n", a)
	}
	fmt.Printf("\nPublishing them in the DHT (joining it takes a few seconds)...\n")
	if err := dhtNode.Publish(ctx, d.key, d.fingerprint, discovery.Record{Addresses: addresses, At: time.Now().UTC()}); err != nil {
		fmt.Printf("  failed: %v\n", err)
	} else {
		fmt.Printf("  done\n")
	}
	if record, err := dhtNode.Lookup(ctx, d.key.Public().(ed25519.PublicKey), d.fingerprint); err != nil {
		fmt.Printf("  looking it up again: %v\n", err)
	} else {
		fmt.Printf("  found again: %s\n", strings.Join(record.Addresses, ", "))
	}
	return nil
}

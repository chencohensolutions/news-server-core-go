package newsserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/discovery"
	"github.com/chencohensolutions/news-server-core-go/graph/model"
)

// writeServerCert writes a self-signed server certificate and its key, as the scripts make them.
func writeServerCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "news"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "news"}}, &key.PublicKey, key)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

func TestTheServerIsKnownByItsKeyInTheDHT(t *testing.T) {
	certPath, keyPath := writeServerCert(t)
	config := auth.AppConfig{TLSCert: certPath, TLSKey: keyPath, PublicURL: "https://news.example:56399"}.WithDefaults()
	d, err := openDiscovery(config)
	if err != nil {
		t.Fatal(err)
	}
	// The fingerprint is the one the link pins, and the DHT key comes from the same TLS key, every time.
	if want, _ := serverFingerprint(certPath); d.fingerprint != want {
		t.Fatalf("fingerprint %s, the link's %s", d.fingerprint, want)
	}
	again, _ := openDiscovery(config)
	if d.dhtKey() == "" || d.dhtKey() != again.dhtKey() {
		t.Fatalf("DHT key %q, then %q", d.dhtKey(), again.dhtKey())
	}
	if _, err := discovery.ParseKey(d.dhtKey()); err != nil {
		t.Fatal(err)
	}
	// Off: no key, so nothing is published.
	off := false
	config.Discovery = &off
	if d, _ := openDiscovery(config); d.key != nil || d.publisher.Node != nil {
		t.Fatal("published with discovery off")
	}
}

func TestTheAppReadsWhereTheServerIs(t *testing.T) {
	env := newTestEnv(t)
	key := "cd34"
	env.api.Discovery = func() *model.ServerDiscovery {
		return &model.ServerDiscovery{Fingerprint: "ab12", ID: "X", Key: &key, Addresses: []string{"https://203.0.113.7:56400"}}
	}
	resp := mustQuery(t, env, `{ serverInfo { discovery { fingerprint key addresses } } }`, nil)
	var info struct {
		Discovery struct {
			Fingerprint string
			Key         string
			Addresses   []string
		}
	}
	json.Unmarshal(resp.Data["serverInfo"], &info)
	if info.Discovery.Key != "cd34" || len(info.Discovery.Addresses) != 1 || info.Discovery.Fingerprint != "ab12" {
		t.Fatalf("%s", resp.Data["serverInfo"])
	}
}

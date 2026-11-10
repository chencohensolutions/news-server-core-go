package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chencohensolutions/news-server-core-go/auth"
	"github.com/chencohensolutions/news-server-core-go/enroll"
	"github.com/chencohensolutions/news-server-core-go/graph/resolver"
	"github.com/chencohensolutions/news-server-core-go/sites"
	"github.com/chencohensolutions/news-server-core-go/store"
)

// testEnv is a full mTLS server with a generated CA, as the journal server's tests have it: real certificates, a
// real device registry in a temporary folder, no mocks.
type testEnv struct {
	server     *httptest.Server
	enrollment *enroll.Service
	roots      *x509.CertPool
	// The server's database, in the temporary folder, and its resolver, whose Stories a test may set.
	db  *store.Store
	api *resolver.Resolver
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	now := time.Now()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)

	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverDER, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, caCert, &serverKey.PublicKey, caKey)

	dir := t.TempDir()
	config := auth.AppConfig{
		PublicURL: "https://news.test:56399",
		Users:     []auth.UserConfig{{Username: "hen"}},
	}.WithDefaults()
	registry, err := enroll.OpenRegistry(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	enrollment := enroll.NewService(enroll.NewIssuer(caCert, caKey, time.Now), registry, time.Now)
	found := []sites.Source{{ID: "siteh", Name: "siteh", BaseURL: "https://www.siteh.example", Language: "he"}}
	db, err := store.Open(filepath.Join(dir, "news.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	settings, err := sites.NewSettings(context.Background(), db, found, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := &resolver.Resolver{Enrollment: enrollment, Sites: found, Settings: settings, Version: version, Store: db}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	server := httptest.NewUnstartedServer(newMux(config, api))
	server.TLS = tlsConfigFor(tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}, pool)
	server.StartTLS()
	t.Cleanup(server.Close)
	return &testEnv{server: server, enrollment: enrollment, roots: pool, db: db, api: api}
}

// client is an HTTPS client that trusts the test CA and, with cert, presents it.
func (e *testEnv) client(cert *tls.Certificate) *http.Client {
	config := &tls.Config{RootCAs: e.roots}
	if cert != nil {
		config.Certificates = []tls.Certificate{*cert}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: config}}
}

type gqlResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func (e *testEnv) query(t *testing.T, cert *tls.Certificate, query string, variables map[string]any) (int, gqlResponse) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	resp, err := e.client(cert).Post(e.server.URL+"/query", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out gqlResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

// enrollDevice enrolls a new key for user through the public API, as the app does, and returns its certificate.
func (e *testEnv) enrollDevice(t *testing.T, user string) (*tls.Certificate, string) {
	t.Helper()
	code, _ := e.enrollment.Codes.Create(user)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	status, resp := e.query(t, nil, `mutation($input: EnrollDeviceInput!) {
		enrollDevice(input: $input) {
			__typename
			... on EnrolledDevice { user certificate serial }
			... on EnrollmentRejected { reason }
		}
	}`, map[string]any{"input": map[string]any{
		"code": code, "deviceName": "Pixel", "csr": base64.StdEncoding.EncodeToString(csr), "keyProtection": "SOFTWARE",
	}})
	if status != http.StatusOK || len(resp.Errors) > 0 {
		t.Fatalf("enrollDevice: %d %+v", status, resp.Errors)
	}
	var enrolled struct {
		Typename    string `json:"__typename"`
		User        string `json:"user"`
		Certificate string `json:"certificate"`
		Serial      string `json:"serial"`
	}
	json.Unmarshal(resp.Data["enrollDevice"], &enrolled)
	if enrolled.Typename != "EnrolledDevice" || enrolled.User != user {
		t.Fatalf("enrollDevice: %s", resp.Data["enrollDevice"])
	}
	der, _ := base64.StdEncoding.DecodeString(enrolled.Certificate)
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, enrolled.Serial
}

func TestAnEnrolledDeviceReadsTheSites(t *testing.T) {
	env := newTestEnv(t)
	cert, _ := env.enrollDevice(t, "hen")
	status, resp := env.query(t, cert, `{ sources { id name baseUrl language } serverInfo { api renewCertificates stories } }`, nil)
	if status != http.StatusOK || len(resp.Errors) > 0 {
		t.Fatalf("%d %+v", status, resp.Errors)
	}
	if got := string(resp.Data["sources"]); got != `[{"id":"siteh","name":"siteh","baseUrl":"https://www.siteh.example","language":"he"}]` {
		t.Fatalf("sources = %s", got)
	}
	if got := string(resp.Data["serverInfo"]); got != `{"api":1,"renewCertificates":true,"stories":false}` {
		t.Fatalf("serverInfo = %s", got)
	}
}

func TestWithoutACertificateOnlyEnrollmentAnswers(t *testing.T) {
	env := newTestEnv(t)
	status, resp := env.query(t, nil, `{ sources { id } }`, nil)
	if status != http.StatusOK || len(resp.Errors) != 1 || resp.Errors[0].Extensions["code"] != "UNAUTHORIZED" {
		t.Fatalf("%d %+v", status, resp.Errors)
	}
	// Introspection is a root field too.
	_, resp = env.query(t, nil, `{ __schema { types { name } } }`, nil)
	if len(resp.Errors) == 0 || !strings.Contains(resp.Errors[0].Message, "client certificate required") {
		t.Fatalf("introspection answered without a certificate: %+v", resp)
	}
	code, _ := env.enrollment.Codes.Create("hen")
	_, resp = env.query(t, nil, `query($code: String!) { enrollmentPreview(code: $code) { ... on EnrollmentPreview { user } } }`,
		map[string]any{"code": code})
	if len(resp.Errors) > 0 || string(resp.Data["enrollmentPreview"]) != `{"user":"hen"}` {
		t.Fatalf("enrollmentPreview: %+v %s", resp.Errors, resp.Data["enrollmentPreview"])
	}
}

func TestARevokedDeviceIsRefused(t *testing.T) {
	env := newTestEnv(t)
	cert, serial := env.enrollDevice(t, "hen")
	if _, err := env.enrollment.Registry.Revoke(serial, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if status, _ := env.query(t, cert, `{ sources { id } }`, nil); status != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", status)
	}
}

func TestEnrollmentLinkOpensTheNewsApp(t *testing.T) {
	server := "https://news.example:56399"
	found := enrollDiscovery{Fingerprint: "ab12", Key: "cd34", Addresses: []string{"https://203.0.113.7:56399", "https://192.168.1.20:56399"}}
	if got := enrollmentLink("K7QM4XPD", &server, found); got != "news://enroll?address=https%3A%2F%2F203.0.113.7%3A56399&address=https%3A%2F%2F192.168.1.20%3A56399&code=K7QM4XPD&fingerprint=ab12&key=cd34&server=https%3A%2F%2Fnews.example%3A56399" {
		t.Fatal(got)
	}
	if got := enrollmentLink("K7QM4XPD", &server, enrollDiscovery{Fingerprint: "ab12"}); got != "news://enroll?code=K7QM4XPD&fingerprint=ab12&server=https%3A%2F%2Fnews.example%3A56399" {
		t.Fatal(got)
	}
	if got := enrollmentLink("K7QM4XPD", nil, enrollDiscovery{}); got != "news://enroll?code=K7QM4XPD" {
		t.Fatal(got)
	}
}

// The link's fingerprint is the key the phone sees in the TLS handshake.
func TestTheLinksFingerprintIsTheKeyTheServerPresents(t *testing.T) {
	env := newTestEnv(t)
	resp, err := env.client(nil).Get(env.server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	seen := Fingerprint(resp.TLS.PeerCertificates[0])
	path := filepath.Join(t.TempDir(), "server.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: resp.TLS.PeerCertificates[0].Raw}), 0o644)
	if got, err := serverFingerprint(path); err != nil || got != seen || len(got) != 64 {
		t.Fatalf("%q, %v; the handshake has %q", got, err, seen)
	}
	if got := shortFingerprint(seen); len(got) != 19 || got[:4] != seen[:4] {
		t.Fatalf("short %q", got)
	}
}

func TestCheckRunsTheFixturesOfEverySource(t *testing.T) {
	var out bytes.Buffer
	if _, err := runSiteCommand("check", []string{filepath.Join("news", "sources", "_example")}, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.HasPrefix(out.String(), "ok   _example") {
		t.Fatal(out.String())
	}
}
